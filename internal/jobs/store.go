// Package jobs provides persistence for resumable background work.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

type Status string

const (
	Pending            Status = "pending"
	Running            Status = "running"
	Paused             Status = "paused"
	Cancelling         Status = "cancelling"
	Cancelled          Status = "cancelled"
	Completed          Status = "completed"
	CompletedWithError Status = "completed-with-errors"
	Failed             Status = "failed"
)

type Job struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Priority    int             `json:"priority"`
	Status      Status          `json:"status"`
	Payload     json.RawMessage `json:"payload"`
	Progress    json.RawMessage `json:"progress,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Error       string          `json:"error,omitempty"`
	Summary     json.RawMessage `json:"summary,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	AvailableAt *time.Time      `json:"available_at,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
}

type Item struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	Key       string    `json:"item_key"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) Enqueue(ctx context.Context, kind string, priority int, payload any) (Job, error) {
	return s.enqueueAt(ctx, kind, priority, payload, time.Now().UTC())
}

func (s Store) enqueueAt(ctx context.Context, kind string, priority int, payload any, availableAt time.Time) (Job, error) {
	jobID, err := id.NewUUIDv7()
	if err != nil {
		return Job{}, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Job{}, fmt.Errorf("marshal job payload: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, priority, status, payload_json, attempts, max_attempts, created_at, available_at)
		VALUES (?, ?, ?, ?, ?, 0, 3, ?, ?)`, jobID, kind, priority, Pending, payloadJSON, now.Format(time.RFC3339Nano), availableAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Job{}, fmt.Errorf("enqueue job: %w", err)
	}
	return s.Get(ctx, jobID)
}

// EnqueueReferenceReprocess queues one delayed reprocess for a person. A
// pending job for the same person absorbs subsequent reference changes during
// the debounce window. No job is created until the person has at least one
// approved reference face, which is the minimum needed for matching.
func (s Store) EnqueueReferenceReprocess(ctx context.Context, personID string) (Job, bool, error) {
	var references int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM current_face_references WHERE person_id = ? AND reference = 1`, personID).Scan(&references); err != nil {
		return Job{}, false, err
	}
	if references == 0 {
		return Job{}, false, nil
	}
	var existingID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE kind = 'reference-reprocess'
		  AND status = 'pending'
		  AND json_extract(payload_json, '$.person_id') = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, personID).Scan(&existingID)
	if err == nil {
		job, getErr := s.Get(ctx, existingID)
		return job, true, getErr
	}
	if err != sql.ErrNoRows {
		return Job{}, false, err
	}
	job, err := s.enqueueAt(ctx, "reference-reprocess", 1, map[string]string{"person_id": personID}, time.Now().UTC().Add(time.Minute))
	return job, true, err
}

func (s Store) Get(ctx context.Context, jobID string) (Job, error) {
	var job Job
	var payload, progress, summary, errorCode, errorMessage, started, completed sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, kind, priority, status, payload_json, progress_json, summary_json,
		       error_code, error_message, attempts, max_attempts, created_at, started_at, completed_at
		FROM jobs WHERE id = ?`, jobID).Scan(
		&job.ID, &job.Kind, &job.Priority, &job.Status, &payload, &progress, &summary,
		&errorCode, &errorMessage, &job.Attempts, &job.MaxAttempts, &created, &started, &completed)
	if err != nil {
		return Job{}, err
	}
	job.Payload = json.RawMessage(payload.String)
	job.ErrorCode = errorCode.String
	job.Error = errorMessage.String
	if progress.Valid {
		job.Progress = json.RawMessage(progress.String)
	}
	if summary.Valid {
		job.Summary = json.RawMessage(summary.String)
	}
	if job.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Job{}, fmt.Errorf("parse job created_at: %w", err)
	}
	var available string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(available_at, '') FROM jobs WHERE id = ?`, jobID).Scan(&available); err == nil && available != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, available)
		if parseErr != nil {
			return Job{}, fmt.Errorf("parse job available_at: %w", parseErr)
		}
		job.AvailableAt = &parsed
	}
	job.StartedAt, err = parseNullableTime(started)
	if err != nil {
		return Job{}, err
	}
	job.CompletedAt, err = parseNullableTime(completed)
	return job, err
}

// SetSummary persists the completed (or partial, on failure) operational
// summary for a job. Summaries are versioned by the job status output rather
// than being interpreted by the worker protocol.
func (s Store) SetSummary(ctx context.Context, jobID string, summary any) error {
	data, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal job summary: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE jobs SET summary_json = ? WHERE id = ?`, data, jobID)
	return err
}

type Metrics struct {
	Faces                    int `json:"faces"`
	People                   int `json:"people"`
	Clusters                 int `json:"clusters"`
	ClusterMemberships       int `json:"cluster_memberships"`
	RecognitionCandidates    int `json:"recognition_candidates"`
	Assignments              int `json:"assignments"`
	ReferenceRecommendations int `json:"reference_recommendations"`
}

// CaptureActivity summarizes audit and child-job activity attributable to a
// job since its start time. It complements catalog-count deltas, which cannot
// show updates to rows such as current assignments.
func (s Store) CaptureActivity(ctx context.Context, jobID string, since time.Time) (map[string]any, error) {
	start := since.UTC().Format(time.RFC3339Nano)
	activity := map[string]any{}
	queries := []struct {
		name   string
		query  string
		result *int
	}{
		{"faces_changed", `SELECT COUNT(DISTINCT entity_id) FROM audit_events WHERE action = 'assignment' AND entity_type = 'face' AND created_at >= ?`, new(int)},
		{"assignment_changes", `SELECT COUNT(*) FROM audit_events WHERE action = 'assignment' AND entity_type = 'face' AND created_at >= ?`, new(int)},
		{"reference_selections", `SELECT COUNT(*) FROM audit_events WHERE action = 'reference-selection' AND entity_type = 'face' AND created_at >= ?`, new(int)},
		{"child_jobs_created", `SELECT COUNT(*) FROM jobs WHERE id <> ? AND created_at >= ?`, new(int)},
		{"people_affected", `SELECT COUNT(DISTINCT json_extract(new_json, '$.person_id')) FROM audit_events WHERE action = 'assignment' AND entity_type = 'face' AND created_at >= ? AND json_extract(new_json, '$.person_id') IS NOT NULL AND json_extract(new_json, '$.person_id') <> ''`, new(int)},
	}
	for _, item := range queries {
		var err error
		if item.name == "child_jobs_created" {
			err = s.db.QueryRowContext(ctx, item.query, jobID, start).Scan(item.result)
		} else {
			err = s.db.QueryRowContext(ctx, item.query, start).Scan(item.result)
		}
		if err != nil {
			return nil, err
		}
		activity[item.name] = *item.result
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(json_extract(new_json, '$.state'), ''), COUNT(*)
		FROM audit_events
		WHERE action = 'assignment' AND entity_type = 'face' AND created_at >= ?
		GROUP BY json_extract(new_json, '$.state')`, start)
	if err != nil {
		return nil, err
	}
	byState := map[string]int{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return nil, err
		}
		byState[state] = count
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	activity["assignment_changes_by_state"] = byState
	return activity, nil
}

// CaptureMetrics returns inexpensive catalog counts used in job summaries.
// It deliberately reports counts rather than exposing model-specific details
// so every job kind receives a consistent summary.
func (s Store) CaptureMetrics(ctx context.Context) (Metrics, error) {
	var metrics Metrics
	queries := []struct {
		target *int
		query  string
	}{
		{&metrics.Faces, `SELECT COUNT(*) FROM faces`},
		{&metrics.People, `SELECT COUNT(*) FROM people`},
		{&metrics.Clusters, `SELECT COUNT(*) FROM clusters`},
		{&metrics.ClusterMemberships, `SELECT COUNT(*) FROM cluster_memberships`},
		{&metrics.RecognitionCandidates, `SELECT COUNT(*) FROM recognition_candidates`},
		{&metrics.Assignments, `SELECT COUNT(*) FROM current_face_assignments`},
		{&metrics.ReferenceRecommendations, `SELECT COUNT(*) FROM reference_recommendations`},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.target); err != nil {
			return Metrics{}, err
		}
	}
	return metrics, nil
}

// RecordFailure persists one handler failure and either requeues the job or
// marks it permanently failed once its bounded retry budget is exhausted.
func (s Store) RecordFailure(ctx context.Context, jobID, code, message string, retryable bool) (Status, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET attempts = attempts + 1,
		 status = CASE WHEN ? AND attempts + 1 < max_attempts THEN ? ELSE ? END,
		 error_code = ?, error_message = ?,
		 started_at = CASE WHEN ? AND attempts + 1 < max_attempts THEN NULL ELSE started_at END,
		 completed_at = CASE WHEN ? AND attempts + 1 < max_attempts THEN NULL ELSE ? END
		WHERE id = ?`, retryable, Pending, Failed, code, message, retryable, retryable, time.Now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return "", sql.ErrNoRows
	}
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

// Claim atomically claims the highest-priority pending job. A nil result means
// no work is currently available.
func (s Store) Claim(ctx context.Context) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var jobID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE status = ? AND (available_at IS NULL OR available_at <= ?)
		ORDER BY priority ASC, created_at ASC, id ASC
		LIMIT 1`, Pending, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&jobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select pending job: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = ? WHERE id = ? AND status = ?`, Running, now, jobID, Pending); err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s Store) UpdateProgress(ctx context.Context, jobID string, progress any) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("marshal job progress: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE jobs SET progress_json = ? WHERE id = ?`, data, jobID)
	return err
}

func (s Store) SetStatus(ctx context.Context, jobID string, status Status, code, message string) error {
	completed := ""
	if status == Completed || status == CompletedWithError || status == Failed || status == Cancelled {
		completed = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, error_code = ?, error_message = ?, completed_at = NULLIF(?, '') WHERE id = ?`, status, code, message, completed, jobID)
	return err
}

func (s Store) Pause(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ? AND status IN (?, ?)`, Paused, jobID, Pending, Running)
	return err
}

func (s Store) Cancel(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ? AND status IN (?, ?, ?)`, Cancelling, jobID, Pending, Running, Paused)
	return err
}

func (s Store) Resume(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ? AND status = ?`, Pending, jobID, Paused)
	return err
}

// Retry returns a failed, cancelled, or completed-with-errors job to the
// pending queue while retaining its original payload and history timestamps.
func (s Store) Retry(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, attempts = 0, error_code = NULL, error_message = NULL, started_at = NULL, completed_at = NULL WHERE id = ? AND status IN (?, ?, ?)`, Pending, jobID, Failed, Cancelled, CompletedWithError)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE job_items SET status = 'pending', attempts = 0, error_code = NULL, error_message = NULL, updated_at = ? WHERE job_id = ? AND status IN ('failed', 'running', 'pending')`, time.Now().UTC().Format(time.RFC3339Nano), jobID)
	return err
}

// EnsureItems creates resumable item records without disturbing completed
// work. Item keys are caller-defined stable identifiers such as asset IDs.
func (s Store) EnsureItems(ctx context.Context, jobID string, keys []string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, key := range keys {
		itemID, err := id.NewUUIDv7()
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO job_items (id, job_id, item_key, status, updated_at) VALUES (?, ?, ?, 'pending', ?) ON CONFLICT(job_id, item_key) DO NOTHING`, itemID, jobID, key, now); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) ClaimItem(ctx context.Context, jobID, key string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE job_items SET status = 'running', attempts = attempts + 1, updated_at = ? WHERE job_id = ? AND item_key = ? AND status = 'pending'`, time.Now().UTC().Format(time.RFC3339Nano), jobID, key)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count == 1, nil
}

func (s Store) CompleteItem(ctx context.Context, jobID, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET status = 'completed', error_code = NULL, error_message = NULL, updated_at = ? WHERE job_id = ? AND item_key = ?`, time.Now().UTC().Format(time.RFC3339Nano), jobID, key)
	return err
}

func (s Store) FailItem(ctx context.Context, jobID, key, code, message string, permanent bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET status = CASE WHEN ? OR attempts >= 3 THEN 'failed' ELSE 'pending' END, error_code = ?, error_message = ?, updated_at = ? WHERE job_id = ? AND item_key = ?`, permanent, code, message, time.Now().UTC().Format(time.RFC3339Nano), jobID, key)
	return err
}

func (s Store) Items(ctx context.Context, jobID string) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, item_key, status, attempts, COALESCE(error_code, ''), COALESCE(error_message, ''), updated_at FROM job_items WHERE job_id = ? ORDER BY item_key`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Item
	for rows.Next() {
		var item Item
		var updated string
		if err := rows.Scan(&item.ID, &item.JobID, &item.Key, &item.Status, &item.Attempts, &item.ErrorCode, &item.Error, &updated); err != nil {
			return nil, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("parse job timestamp: %w", err)
	}
	return &parsed, nil
}
