package jobs

import (
	"context"
	"fmt"
	"time"
)

type Handler func(context.Context, Job) error

type Runner struct {
	Store    Store
	Handlers map[string]Handler
	IdleWait time.Duration
}

func (r Runner) RunOnce(ctx context.Context) (bool, error) {
	job, err := r.Store.Claim(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	handler, ok := r.Handlers[job.Kind]
	if !ok {
		_ = r.Store.SetStatus(ctx, job.ID, Failed, "unknown_job_kind", fmt.Sprintf("no handler registered for %s", job.Kind))
		return true, fmt.Errorf("no handler registered for job kind %q", job.Kind)
	}
	if err := handler(ctx, *job); err != nil {
		status := Failed
		if ctx.Err() != nil {
			status = Cancelled
		}
		_ = r.Store.SetStatus(context.Background(), job.ID, status, "handler_error", err.Error())
		return true, err
	}
	if err := r.Store.SetStatus(ctx, job.ID, Completed, "", ""); err != nil {
		return true, err
	}
	return true, nil
}

func (r Runner) Run(ctx context.Context) error {
	wait := r.IdleWait
	if wait <= 0 {
		wait = time.Second
	}
	for {
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if !worked {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}
