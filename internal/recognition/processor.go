package recognition

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
	db         *sql.DB
	review     review.Store
	thresholds Thresholds
}

func NewProcessor(database *sql.DB, thresholds Thresholds) Processor {
	return Processor{db: database, review: review.NewStore(database), thresholds: thresholds}
}

// ProcessAll evaluates every face with the current approved reference gallery.
// User-confirmed assignments are never automatically changed.
func (p Processor) ProcessAll(ctx context.Context) error {
	references, err := p.loadReferences(ctx)
	if err != nil {
		return err
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT f.id, f.embedding, f.embedding_dimension, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0)
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		LEFT JOIN current_face_assignments ca ON ca.face_id = f.id
		WHERE COALESCE(ca.state, 'unassigned') <> 'assigned-user'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var faceID string
		var blob []byte
		var dimension int
		var quality, detection float64
		if err := rows.Scan(&faceID, &blob, &dimension, &quality, &detection); err != nil {
			return err
		}
		embedding, err := decodeEmbedding(blob, dimension)
		if err != nil {
			return fmt.Errorf("face %s: %w", faceID, err)
		}
		candidates, err := Match(embedding, references, float32(quality), float32(detection))
		if err != nil {
			return err
		}
		if err := p.persistCandidates(ctx, faceID, candidates); err != nil {
			return err
		}
		if len(candidates) == 0 {
			if _, err := p.review.Assign(ctx, faceID, "", review.Unknown, review.Actor{Type: "system"}, review.Evidence{}, "no-reference-candidate"); err != nil {
				return err
			}
			continue
		}
		candidate := candidates[0]
		level := ClassifyStandalone(candidate, p.thresholds)
		state := review.Unknown
		personID := ""
		if level == EvidenceHigh {
			state = review.AssignedSystem
			personID = candidate.PersonID
		} else if level == EvidenceMedium {
			state = review.Provisional
			personID = candidate.PersonID
		}
		similarity, margin := candidate.Similarity, candidate.Margin
		support, candidateQuality, candidateDetection := candidate.Support, candidate.Quality, candidate.Detection
		if _, err := p.review.Assign(ctx, faceID, personID, state, review.Actor{Type: "system"}, review.Evidence{Similarity: &similarity, Margin: &margin, Support: &support, Quality: &candidateQuality, Detection: &candidateDetection}, "recognition"); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p Processor) persistCandidates(ctx context.Context, faceID string, candidates []Candidate) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, candidate := range candidates {
		candidateID, err := id.NewUUIDv7()
		if err != nil {
			return err
		}
		evidence := string(ClassifyStandalone(candidate, p.thresholds))
		if _, err := p.db.ExecContext(ctx, `INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidateID, faceID, candidate.PersonID, index+1, candidate.Similarity, candidate.Margin, candidate.Support, candidate.Quality, candidate.Detection, evidence, now); err != nil {
			return err
		}
	}
	return nil
}

func (p Processor) loadReferences(ctx context.Context) (map[string][][]float32, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT r.person_id, f.embedding, f.embedding_dimension
		FROM current_face_references r JOIN faces f ON f.id = r.face_id
		WHERE r.reference = 1 AND f.embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][][]float32{}
	for rows.Next() {
		var personID string
		var blob []byte
		var dimension int
		if err := rows.Scan(&personID, &blob, &dimension); err != nil {
			return nil, err
		}
		embedding, err := decodeEmbedding(blob, dimension)
		if err != nil {
			return nil, err
		}
		result[personID] = append(result[personID], embedding)
	}
	return result, rows.Err()
}

func decodeEmbedding(blob []byte, dimension int) ([]float32, error) {
	if dimension <= 0 || len(blob) != dimension*4 {
		return nil, fmt.Errorf("invalid embedding dimension or size")
	}
	result := make([]float32, dimension)
	for index := range result {
		result[index] = math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4:]))
	}
	return result, nil
}
