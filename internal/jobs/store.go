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
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) Enqueue(ctx context.Context, kind string, priority int, payload any) (Job, error) {
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
		INSERT INTO jobs (id, kind, priority, status, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, jobID, kind, priority, Pending, payloadJSON, now.Format(time.RFC3339Nano))
	if err != nil {
		return Job{}, fmt.Errorf("enqueue job: %w", err)
	}
	return s.Get(ctx, jobID)
}

func (s Store) Get(ctx context.Context, jobID string) (Job, error) {
	var job Job
	var payload, progress, errorCode, errorMessage, started, completed sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, kind, priority, status, payload_json, progress_json,
		       error_code, error_message, created_at, started_at, completed_at
		FROM jobs WHERE id = ?`, jobID).Scan(
		&job.ID, &job.Kind, &job.Priority, &job.Status, &payload, &progress,
		&errorCode, &errorMessage, &created, &started, &completed)
	if err != nil {
		return Job{}, err
	}
	job.Payload = json.RawMessage(payload.String)
	job.ErrorCode = errorCode.String
	job.Error = errorMessage.String
	if progress.Valid {
		job.Progress = json.RawMessage(progress.String)
	}
	if job.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Job{}, fmt.Errorf("parse job created_at: %w", err)
	}
	job.StartedAt, err = parseNullableTime(started)
	if err != nil {
		return Job{}, err
	}
	job.CompletedAt, err = parseNullableTime(completed)
	return job, err
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
		WHERE status = ?
		ORDER BY priority ASC, created_at ASC, id ASC
		LIMIT 1`, Pending).Scan(&jobID)
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
