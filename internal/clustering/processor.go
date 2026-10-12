package clustering

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
	"github.com/pjg/face_me_bro/internal/recognition"
	"github.com/pjg/face_me_bro/internal/review"
)

type Processor struct {
	db     *sql.DB
	store  RunStore
	config Config
}

func NewProcessor(database *sql.DB, config Config) Processor {
	return Processor{db: database, store: NewRunStore(database), config: config}
}

// Run groups all faces with embeddings. Partial clustering is intentionally
// unavailable until active-run invalidation and fixed-anchor semantics are
// implemented.
func (p Processor) Run(ctx context.Context, scope string) (string, error) {
	if scope != "" && scope != "all" {
		return "", fmt.Errorf("clustering only supports scope all until subset invalidation is implemented")
	}
	return p.run(ctx, "all", nil)
}

func (p Processor) RunFaces(ctx context.Context, faceIDs []string) (string, error) {
	return "", fmt.Errorf("subset clustering is not available until active-run invalidation is implemented")
}

func (p Processor) run(ctx context.Context, scope string, faceIDs []string) (string, error) {
	if scope == "" {
		scope = "all"
	}
	// User-confirmed faces participate as fixed identity anchors. They are
	// skipped later when assignments are applied, so clustering cannot change
	// them, but excluding them here would make newly reviewed faces lose the
	// person identity that should anchor their surrounding group.
	query := `SELECT f.id, f.embedding, f.embedding_dimension, COALESCE(ca.person_id, '') FROM faces f LEFT JOIN current_face_assignments ca ON ca.face_id = f.id WHERE f.embedding IS NOT NULL`
	queryArgs := []any{}
	if len(faceIDs) > 0 {
		placeholders := make([]string, len(faceIDs))
		for index, faceID := range faceIDs {
			placeholders[index] = "?"
			queryArgs = append(queryArgs, faceID)
		}
		query += ` AND f.id IN (` + strings.Join(placeholders, ",") + `)`
	}
	if scope == "unresolved-only" {
		query += ` AND COALESCE(ca.state, 'unassigned') NOT IN ('assigned-system', 'assigned-user')`
	}
	rows, err := p.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var faces []Face
	for rows.Next() {
		var face Face
		var blob []byte
		var dimension int
		if err := rows.Scan(&face.ID, &blob, &dimension, &face.PersonID); err != nil {
			return "", err
		}
		face.Embedding, err = decode(blob, dimension)
		if err != nil {
			return "", fmt.Errorf("face %s: %w", face.ID, err)
		}
		faces = append(faces, face)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	inputFaceIDs := make([]string, 0, len(faces))
	for _, face := range faces {
		inputFaceIDs = append(inputFaceIDs, face.ID)
	}
	runID, err := p.store.CreateRun(ctx, p.config, scope, inputFaceIDs)
	if err != nil {
		return "", err
	}
	result, err := GroupContext(ctx, faces, p.config)
	if err != nil {
		return "", err
	}
	for index := range result.Clusters {
		cluster := &result.Clusters[index]
		personIDs := map[string]struct{}{}
		for _, faceID := range cluster.FaceIDs {
			for _, face := range faces {
				if face.ID == faceID && face.PersonID != "" {
					personIDs[face.PersonID] = struct{}{}
				}
			}
		}
		if len(personIDs) == 1 {
			for personID := range personIDs {
				cluster.ProposedPersonID = personID
			}
		} else if len(personIDs) == 0 && !cluster.Mixed {
			personID, createErr := findOrCreateProvisionalPerson(ctx, p.db, cluster.FaceIDs)
			if createErr != nil {
				return "", createErr
			}
			cluster.ProposedPersonID = personID
		}
	}
	if err := p.store.PersistResult(ctx, runID, result); err != nil {
		return "", err
	}
	for _, cluster := range result.Clusters {
		if cluster.ProposedPersonID == "" || cluster.Mixed {
			continue
		}
		for _, faceID := range cluster.FaceIDs {
			current, currentErr := review.NewStore(p.db).Current(ctx, faceID)
			if currentErr == sql.ErrNoRows {
				if err := review.NewStore(p.db).InitializeFace(ctx, faceID, review.Actor{Type: "system"}); err != nil {
					return "", err
				}
				current, currentErr = review.NewStore(p.db).Current(ctx, faceID)
			}
			if currentErr != nil || current.State == review.AssignedUser {
				continue
			}
			purity, connectivity := cluster.Purity, cluster.Connectivity
			var candidatePerson string
			var candidate recognition.Candidate
			agreementValue := false
			candidateErr := p.db.QueryRowContext(ctx, `SELECT person_id, similarity, margin, support_count, quality, detection_confidence FROM recognition_candidates WHERE face_id = ? ORDER BY created_at DESC, rank LIMIT 1`, faceID).Scan(&candidatePerson, &candidate.Similarity, &candidate.Margin, &candidate.Support, &candidate.Quality, &candidate.Detection)
			var agreement *bool
			state := review.Provisional
			if candidateErr == nil {
				agreementValue = candidatePerson == cluster.ProposedPersonID
				agreement = &agreementValue
				candidate.PersonID, candidate.Purity, candidate.Connectivity, candidate.References, candidate.Agreement = candidatePerson, cluster.Purity, cluster.Connectivity, true, agreementValue
				thresholds := p.config.RecognitionThresholds
				if thresholds == (recognition.Thresholds{}) {
					thresholds = recognition.DefaultThresholds()
				}
				level := recognition.Classify(candidate, thresholds)
				if !agreementValue {
					state = review.Unknown
				} else if level == recognition.EvidenceHigh {
					state = review.AssignedSystem
				}
				if level == recognition.EvidenceManual {
					state = review.Unknown
				}
			}
			if state == review.Unknown {
				candidatePerson = ""
			}
			if _, err := review.NewStore(p.db).Assign(ctx, faceID, candidatePersonOr(cluster.ProposedPersonID, state), state, review.Actor{Type: "system"}, review.Evidence{ClusteringRunID: runID, Similarity: &candidate.Similarity, Margin: &candidate.Margin, Support: &candidate.Support, Quality: &candidate.Quality, Detection: &candidate.Detection, Purity: &purity, Connectivity: &connectivity, Agreement: agreement}, "cluster-provisional"); err != nil {
				return "", err
			}
		}
	}
	if err := p.store.Activate(ctx, runID); err != nil {
		return "", err
	}
	people := make(map[string]struct{})
	for _, cluster := range result.Clusters {
		if cluster.ProposedPersonID != "" {
			people[cluster.ProposedPersonID] = struct{}{}
		}
	}
	thresholds := p.config.RecognitionThresholds
	if thresholds == (recognition.Thresholds{}) {
		thresholds = recognition.DefaultThresholds()
	}
	recognitionProcessor := recognition.NewProcessor(p.db, thresholds)
	for personID := range people {
		if err := recognitionProcessor.RefreshRecommendations(ctx, personID); err != nil {
			return "", err
		}
	}
	return runID, nil
}

func candidatePersonOr(personID string, state review.AssignmentState) string {
	if state == review.Unknown {
		return ""
	}
	return personID
}

func findOrCreateProvisionalPerson(ctx context.Context, database *sql.DB, faceIDs []string) (string, error) {
	for _, faceID := range faceIDs {
		var personID string
		err := database.QueryRowContext(ctx, `
			SELECT c.proposed_person_id
			FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id
			JOIN clustering_runs r ON r.id = c.clustering_run_id
			JOIN people p ON p.id = c.proposed_person_id
			WHERE cm.face_id = ? AND r.status = 'active' AND p.is_provisional = 1
			ORDER BY r.accepted_at DESC LIMIT 1`, faceID).Scan(&personID)
		if err == nil && personID != "" {
			return personID, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	personID, err := id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = database.ExecContext(ctx, `INSERT INTO people (id, name, is_provisional, dont_track, created_at, updated_at) VALUES (?, NULL, 1, 0, ?, ?)`, personID, now, now)
	return personID, err
}

func decode(blob []byte, dimension int) ([]float32, error) {
	if dimension <= 0 || len(blob) != dimension*4 {
		return nil, fmt.Errorf("invalid embedding dimension or size")
	}
	values := make([]float32, dimension)
	for index := range values {
		values[index] = math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4:]))
	}
	return values, nil
}
