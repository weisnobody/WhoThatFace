package clustering

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
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

// Run groups faces with embeddings. Scope is all, unresolved-only, or subset;
// subset is reserved for callers that provide a prefiltered face set.
func (p Processor) Run(ctx context.Context, scope string) (string, error) {
	if scope == "" {
		scope = "all"
	}
	query := `SELECT f.id, f.embedding, f.embedding_dimension, COALESCE(ca.person_id, '') FROM faces f LEFT JOIN current_face_assignments ca ON ca.face_id = f.id WHERE f.embedding IS NOT NULL`
	if scope == "unresolved-only" {
		query += ` AND COALESCE(ca.state, 'unassigned') NOT IN ('assigned-system', 'assigned-user')`
	}
	rows, err := p.db.QueryContext(ctx, query)
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
	faceIDs := make([]string, 0, len(faces))
	for _, face := range faces {
		faceIDs = append(faceIDs, face.ID)
	}
	runID, err := p.store.CreateRun(ctx, p.config, scope, faceIDs)
	if err != nil {
		return "", err
	}
	result, err := Group(faces, p.config)
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
			personID, createErr := createProvisionalPerson(ctx, p.db)
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
			if currentErr != nil || current.State != review.Unassigned {
				continue
			}
			purity, connectivity := cluster.Purity, cluster.Connectivity
			var candidatePerson string
			agreementValue := false
			candidateErr := p.db.QueryRowContext(ctx, `SELECT person_id FROM recognition_candidates WHERE face_id = ? ORDER BY created_at DESC, rank LIMIT 1`, faceID).Scan(&candidatePerson)
			var agreement *bool
			if candidateErr == nil {
				agreementValue = candidatePerson == cluster.ProposedPersonID
				agreement = &agreementValue
			}
			if _, err := review.NewStore(p.db).Assign(ctx, faceID, cluster.ProposedPersonID, review.Provisional, review.Actor{Type: "system"}, review.Evidence{ClusteringRunID: runID, Similarity: nil, Purity: &purity, Connectivity: &connectivity, Agreement: agreement}, "cluster-provisional"); err != nil {
				return "", err
			}
		}
	}
	if err := p.store.Activate(ctx, runID); err != nil {
		return "", err
	}
	return runID, nil
}

func createProvisionalPerson(ctx context.Context, database *sql.DB) (string, error) {
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
