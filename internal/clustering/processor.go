package clustering

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
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
	if err := p.store.PersistResult(ctx, runID, result); err != nil {
		return "", err
	}
	if err := p.store.Activate(ctx, runID); err != nil {
		return "", err
	}
	return runID, nil
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
