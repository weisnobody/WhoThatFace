package candidates

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

type Run struct {
	ID                  string
	Scope               string
	ScopeID             string
	Policy              json.RawMessage
	PipelineID          string
	ReferenceSetVersion string
	Status              string
	TargetCount         int
	CandidateCount      int
	SkippedCount        int
	Error               string
	CreatedAt           time.Time
	StartedAt           *time.Time
	CompletedAt         *time.Time
}

type Row struct {
	ID              string
	RunID           string
	FaceID          string
	PersonID        string
	Rank            int
	Similarity      float32
	Margin          float32
	Support         int
	Quality         float32
	Detection       float32
	Evidence        string
	RetentionReason string
	Current         bool
	CreatedAt       time.Time
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) CreateRun(ctx context.Context, scope, scopeID, pipelineID, referenceSetVersion string, policy Config) (Run, error) {
	runID, err := id.NewUUIDv7()
	if err != nil {
		return Run{}, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return Run{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO possible_candidate_runs
		(id, scope, scope_id, policy_json, pipeline_id, reference_set_version, status, created_at)
		VALUES (?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, 'pending', ?)`,
		runID, scope, scopeID, policyJSON, pipelineID, referenceSetVersion, now); err != nil {
		return Run{}, fmt.Errorf("create possible-candidate run: %w", err)
	}
	return s.GetRun(ctx, runID)
}

func (s Store) GetRun(ctx context.Context, runID string) (Run, error) {
	var run Run
	var policy, scopeID, pipelineID, referenceVersion, errorMessage, created, started, completed string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, scope, COALESCE(scope_id, ''), policy_json, COALESCE(pipeline_id, ''),
		       reference_set_version, status, target_count, candidate_count,
		       skipped_count, COALESCE(error_message, ''), created_at,
		       COALESCE(started_at, ''), COALESCE(completed_at, '')
		FROM possible_candidate_runs WHERE id = ?`, runID).Scan(
		&run.ID, &run.Scope, &scopeID, &policy, &pipelineID, &referenceVersion,
		&run.Status, &run.TargetCount, &run.CandidateCount, &run.SkippedCount,
		&errorMessage, &created, &started, &completed)
	if err != nil {
		return Run{}, err
	}
	run.ScopeID, run.Policy, run.PipelineID = scopeID, json.RawMessage(policy), pipelineID
	run.ReferenceSetVersion, run.Error = referenceVersion, errorMessage
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Run{}, fmt.Errorf("parse possible-candidate created_at: %w", err)
	}
	run.StartedAt, err = nullableTime(started)
	if err != nil {
		return Run{}, err
	}
	run.CompletedAt, err = nullableTime(completed)
	return run, err
}

func (s Store) StartRun(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE possible_candidate_runs SET status = 'running', started_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), runID)
	return err
}

func (s Store) FinishRun(ctx context.Context, runID, status string, targetCount, candidateCount, skippedCount int, message string) error {
	if status != "completed" && status != "cancelled" && status != "failed" {
		return fmt.Errorf("invalid possible-candidate run status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE possible_candidate_runs SET status = ?, target_count = ?, candidate_count = ?, skipped_count = ?, error_message = NULLIF(?, ''), completed_at = ? WHERE id = ?`, status, targetCount, candidateCount, skippedCount, message, time.Now().UTC().Format(time.RFC3339Nano), runID)
	return err
}

func (s Store) MarkFaceRowsStale(ctx context.Context, faceIDs []string) error {
	for _, faceID := range faceIDs {
		if _, err := s.db.ExecContext(ctx, `UPDATE possible_face_candidates SET is_current = 0 WHERE face_id = ? AND is_current = 1`, faceID); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) InsertRow(ctx context.Context, row Row) error {
	if row.ID == "" {
		var err error
		row.ID, err = id.NewUUIDv7()
		if err != nil {
			return err
		}
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO possible_face_candidates
		(id, run_id, face_id, person_id, rank, similarity, margin, support_count,
		 quality, detection_confidence, evidence, retention_reason, is_current, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.ID, row.RunID,
		row.FaceID, row.PersonID, row.Rank, row.Similarity, row.Margin, row.Support,
		row.Quality, row.Detection, row.Evidence, row.RetentionReason, boolInt(row.Current), row.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return &parsed, err
}
