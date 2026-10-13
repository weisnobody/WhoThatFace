package candidates

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/recognition"
)

type Request struct {
	Scope               string
	ScopeID             string
	ReferenceSetVersion string
	Attempt             int
}

type JobPayload struct {
	Scope               string `json:"scope"`
	ScopeID             string `json:"scope_id,omitempty"`
	ReferenceSetVersion string `json:"reference_set_version"`
	Policy              Config `json:"policy"`
}

type Progress struct {
	RunID      string
	Processed  int
	Total      int
	Candidates int
	Skipped    int
}

type targetFace struct {
	ID                string
	Embedding         []float32
	Quality           float32
	Detection         float32
	EligibilityReason string
}

type Generator struct {
	db         *sql.DB
	store      Store
	config     Config
	thresholds recognition.Thresholds
	pipelineID string
}

func NewGenerator(database *sql.DB, config Config, thresholds recognition.Thresholds, pipelineID string) Generator {
	return Generator{db: database, store: NewStore(database), config: config, thresholds: thresholds, pipelineID: pipelineID}
}

func (g Generator) Run(ctx context.Context, request Request) (result Run, err error) {
	return g.run(ctx, request, nil)
}

func (g Generator) RunWithProgress(ctx context.Context, request Request, progress func(Progress) error) (result Run, err error) {
	return g.run(ctx, request, progress)
}

func (g Generator) run(ctx context.Context, request Request, progress func(Progress) error) (result Run, err error) {
	if request.Scope != "person" && request.Scope != "collection" && request.Scope != "all" {
		return Run{}, fmt.Errorf("invalid possible-candidate scope %q", request.Scope)
	}
	version, err := ReferenceSetVersion(ctx, g.db)
	if err != nil {
		return Run{}, err
	}
	if request.ReferenceSetVersion != "" && request.ReferenceSetVersion != version {
		return Run{}, fmt.Errorf("possible-candidate reference set changed before generation: queued=%s current=%s", request.ReferenceSetVersion, version)
	}
	run, err := g.store.CreateRun(ctx, request.Scope, request.ScopeID, g.pipelineID, version, g.config)
	if err != nil {
		return Run{}, err
	}
	if err := audit.NewStore(g.db).Record(ctx, audit.Event{Action: "possible-candidate-run", EntityType: "candidate-run", EntityID: run.ID, New: map[string]any{
		"status": "created", "scope": request.Scope, "scope_id": request.ScopeID,
		"reference_set_version": version, "pipeline_id": g.pipelineID, "attempt": request.Attempt,
	}}); err != nil {
		return Run{}, err
	}
	if err := g.store.StartRun(ctx, run.ID); err != nil {
		return Run{}, err
	}
	if err := audit.NewStore(g.db).Record(ctx, audit.Event{Action: "possible-candidate-run", EntityType: "candidate-run", EntityID: run.ID, New: map[string]any{
		"status": "running", "scope": request.Scope, "scope_id": request.ScopeID,
	}}); err != nil {
		return Run{}, err
	}
	finished := false
	candidateCount := 0
	skippedCount := 0
	processedCount := 0
	var targets []targetFace
	report := func() error {
		if progress == nil {
			return nil
		}
		return progress(Progress{RunID: run.ID, Processed: processedCount, Total: len(targets), Candidates: candidateCount, Skipped: skippedCount})
	}
	defer func() {
		if finished || run.ID == "" {
			return
		}
		message := "possible-candidate generation failed"
		if err != nil {
			message = err.Error()
		}
		status := "failed"
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "cancelled"
		}
		_ = g.store.FinishRun(context.Background(), run.ID, status, len(targets), candidateCount, skippedCount, message)
		_ = audit.NewStore(g.db).Record(context.Background(), audit.Event{Action: "possible-candidate-run", EntityType: "candidate-run", EntityID: run.ID, New: map[string]any{
			"status": status, "error": message,
		}})
	}()
	if err := g.store.MarkRowsStaleForProvenance(ctx, version, g.pipelineID, g.config); err != nil {
		_ = g.store.FinishRun(context.Background(), run.ID, "failed", 0, 0, 0, err.Error())
		return Run{}, err
	}
	targets, err = g.loadTargets(ctx, request)
	if err != nil {
		_ = g.store.FinishRun(context.Background(), run.ID, "failed", 0, 0, 0, err.Error())
		return Run{}, err
	}
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		targetIDs = append(targetIDs, target.ID)
	}
	if err := g.store.MarkFaceRowsStale(ctx, targetIDs); err != nil {
		_ = g.store.FinishRun(context.Background(), run.ID, "failed", len(targets), 0, 0, err.Error())
		return Run{}, err
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return Run{}, err
		}
		targetStatus := "eligible"
		targetSkipReason := ""
		if len(target.Embedding) == 0 {
			targetStatus = "skipped"
			targetSkipReason = "incompatible-embedding"
			skippedCount++
		}
		if err := g.store.InsertTarget(ctx, Target{RunID: run.ID, FaceID: target.ID, EligibilityReason: target.EligibilityReason, Status: targetStatus, SkipReason: targetSkipReason}); err != nil {
			return Run{}, err
		}
		if targetStatus == "skipped" {
			processedCount++
			if err := report(); err != nil {
				return Run{}, err
			}
			continue
		}
		references, refErr := g.loadReferences(ctx, target.ID)
		if refErr != nil {
			return Run{}, refErr
		}
		compatibleReferences := make(map[string][][]float32, len(references))
		for personID, embeddings := range references {
			for _, embedding := range embeddings {
				if len(embedding) == len(target.Embedding) {
					compatibleReferences[personID] = append(compatibleReferences[personID], embedding)
				}
			}
		}
		matches, matchErr := recognition.Match(target.Embedding, compatibleReferences, target.Quality, target.Detection)
		if matchErr != nil {
			_ = g.store.FinishRun(context.Background(), run.ID, "failed", len(targets), candidateCount, skippedCount, matchErr.Error())
			return Run{}, matchErr
		}
		if len(matches) == 0 {
			skippedCount++
			if err := g.store.MarkTargetStatus(ctx, run.ID, target.ID, "no-candidates", "no-compatible-approved-reference"); err != nil {
				return Run{}, err
			}
			processedCount++
			if err := report(); err != nil {
				return Run{}, err
			}
			continue
		}
		limit := g.config.MaxCandidates
		if limit <= 0 {
			limit = 5
		}
		if len(matches) > limit {
			matches = matches[:limit]
		}
		for rank, match := range matches {
			reason := "top-ranked-below-threshold"
			if match.Similarity >= g.config.MinSimilarity {
				reason = "above-floor"
			}
			if err := g.store.InsertRow(ctx, Row{
				RunID: run.ID, FaceID: target.ID, PersonID: match.PersonID, Rank: rank + 1,
				Similarity: match.Similarity, Margin: match.Margin, Support: match.Support,
				Quality: match.Quality, Detection: match.Detection,
				Evidence:        string(recognition.ClassifyStandalone(match, g.thresholds)),
				RetentionReason: reason, Current: false,
			}); err != nil {
				_ = g.store.FinishRun(context.Background(), run.ID, "failed", len(targets), candidateCount, skippedCount, err.Error())
				return Run{}, err
			}
			candidateCount++
		}
		processedCount++
		if err := report(); err != nil {
			return Run{}, err
		}
	}
	if err := g.store.ActivateRunRows(ctx, run.ID); err != nil {
		return Run{}, err
	}
	if err := g.store.FinishRun(ctx, run.ID, "completed", len(targets), candidateCount, skippedCount, ""); err != nil {
		return Run{}, err
	}
	finished = true
	if err := audit.NewStore(g.db).Record(ctx, audit.Event{Action: "possible-candidate-run", EntityType: "candidate-run", EntityID: run.ID, New: map[string]any{
		"status": "completed", "targets": len(targets), "candidates": candidateCount, "skipped": skippedCount,
	}}); err != nil {
		return Run{}, err
	}
	return g.store.GetRun(ctx, run.ID)
}

func (g Generator) loadTargets(ctx context.Context, request Request) ([]targetFace, error) {
	query := `
		WITH provisional_counts AS (
			SELECT person_id, COUNT(*) AS face_count
			FROM current_face_assignments
			WHERE state = 'provisional' AND person_id IS NOT NULL
			GROUP BY person_id
		)
		SELECT f.id, f.embedding, f.embedding_dimension,
		       COALESCE(f.embedding_dtype, ''), COALESCE(f.embedding_normalized, 0),
		       pipe.embedding_dimension, pipe.embedding_dtype, pipe.normalization,
		       CASE
		         WHEN COALESCE(ca.state, 'unassigned') IN ('unknown', 'ambiguous', 'unassigned') THEN COALESCE(ca.state, 'unassigned')
		         WHEN COALESCE(ca.state, '') = 'assigned-system' THEN 'assigned-system'
		         ELSE 'provisional-assignment,small-provisional-person'
		       END,
		       COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0)
		FROM faces f
		JOIN analyses a ON a.id = f.analysis_id
		JOIN pipelines pipe ON pipe.id = a.pipeline_id
		LEFT JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN people p ON p.id = ca.person_id
		LEFT JOIN provisional_counts pc ON pc.person_id = ca.person_id
		WHERE a.pipeline_id = ?
		  AND f.embedding IS NOT NULL
		  AND COALESCE(ca.state, 'unassigned') <> 'assigned-user'
		  AND (
			COALESCE(ca.state, 'unassigned') IN ('unknown', 'ambiguous', 'unassigned')
			OR (COALESCE(ca.state, '') = 'provisional' AND COALESCE(p.is_provisional, 0) = 1 AND pc.face_count BETWEEN 1 AND ?)
			OR (? = 1 AND COALESCE(ca.state, '') = 'assigned-system')
		  )
		  AND (? = 1 OR NOT EXISTS (
			SELECT 1 FROM current_face_dispositions fd
			WHERE fd.face_id = f.id AND fd.disposition = 'dont-track'
		  ))`
	args := []any{g.pipelineID, g.config.MaxProvisionalFaces, boolInt(g.config.IncludeAssignedSystem), boolInt(g.config.IncludeDontTrack)}
	if request.Scope == "person" {
		query += ` AND ca.person_id = ?`
		args = append(args, request.ScopeID)
	} else if request.Scope == "collection" {
		query += ` AND EXISTS (
			SELECT 1 FROM photo_paths pp
			WHERE pp.asset_id = a.asset_id AND pp.collection_id = ?)`
		args = append(args, request.ScopeID)
	}
	query += ` ORDER BY f.id`
	rows, err := g.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []targetFace
	for rows.Next() {
		var target targetFace
		var blob []byte
		var dimension, normalized, expectedDimension int
		var dtype, expectedDtype, normalization string
		if err := rows.Scan(&target.ID, &blob, &dimension, &dtype, &normalized, &expectedDimension, &expectedDtype, &normalization, &target.EligibilityReason, &target.Quality, &target.Detection); err != nil {
			return nil, err
		}
		compatible := dimension == expectedDimension && dtype == expectedDtype && (normalization != "l2" || normalized == 1)
		if compatible {
			target.Embedding, err = decodeEmbedding(blob, dimension)
		}
		if !compatible || err != nil {
			// Preserve the eligible face in the run snapshot and record it as
			// skipped instead of aborting the complete run.
			target.Embedding = nil
		}
		result = append(result, target)
	}
	return result, rows.Err()
}

func (g Generator) loadReferences(ctx context.Context, excludeFaceID string) (map[string][][]float32, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT r.person_id, f.embedding, f.embedding_dimension
		FROM current_face_references r
		JOIN faces f ON f.id = r.face_id
		JOIN analyses a ON a.id = f.analysis_id
		JOIN people p ON p.id = r.person_id
		JOIN pipelines pipe ON pipe.id = a.pipeline_id
		WHERE r.reference = 1 AND p.is_provisional = 0 AND r.face_id <> ?
		  AND f.embedding IS NOT NULL AND a.pipeline_id = ?
		  AND f.embedding_dimension = pipe.embedding_dimension
		  AND f.embedding_dtype = pipe.embedding_dtype
		  AND (pipe.normalization <> 'l2' OR f.embedding_normalized = 1)
		ORDER BY r.person_id, r.face_id`, excludeFaceID, g.pipelineID)
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
			continue
		}
		result[personID] = append(result[personID], embedding)
	}
	return result, rows.Err()
}

func ReferenceSetVersion(ctx context.Context, database *sql.DB) (string, error) {
	rows, err := database.QueryContext(ctx, `SELECT face_id, person_id, updated_at FROM current_face_references WHERE reference = 1 ORDER BY face_id, person_id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var faceID, personID, updated string
		if err := rows.Scan(&faceID, &personID, &updated); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", faceID, personID, updated)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
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
