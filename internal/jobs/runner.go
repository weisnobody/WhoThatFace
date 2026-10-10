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
	Policy   ResourcePolicy
}

func (r Runner) RunOnce(ctx context.Context) (bool, error) {
	mode := r.Policy.EffectiveMode()
	if persisted, err := r.Store.ThrottleMode(ctx); err == nil && persisted != ThrottleAutomatic {
		mode = persisted
	}
	if mode == ThrottlePaused {
		return false, nil
	}
	if mode == ThrottleReduced {
		delay := r.Policy.ReducedDelay
		if delay <= 0 {
			delay = 250 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
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
	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- handler(workContext, *job) }()
	interval := r.Policy.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	controlled := Status("")
	var handlerErr error
	completed := false
	for !completed {
		select {
		case handlerErr = <-result:
			completed = true
		case <-ctx.Done():
			controlled = Cancelled
			cancel()
		case <-ticker.C:
			current, getErr := r.Store.Get(context.Background(), job.ID)
			if getErr == nil && current.Status == Cancelling {
				controlled = Cancelled
				cancel()
			} else if getErr == nil && current.Status == Paused {
				controlled = Paused
				cancel()
			}
		}
	}
	if controlled != "" {
		if controlled == Cancelled {
			_ = r.Store.SetStatus(context.Background(), job.ID, Cancelled, "cancelled", "job cancelled")
		} else {
			_ = r.Store.SetStatus(context.Background(), job.ID, Paused, "paused", "job paused")
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, handlerErr
	}
	if handlerErr != nil {
		status := Failed
		if ctx.Err() != nil {
			status = Cancelled
		}
		if status == Cancelled {
			_ = r.Store.SetStatus(context.Background(), job.ID, status, "handler_error", handlerErr.Error())
		} else {
			_, _ = r.Store.RecordFailure(context.Background(), job.ID, "handler_error", handlerErr.Error(), true)
		}
		return true, handlerErr
	}
	finalStatus := Completed
	if items, itemsErr := r.Store.Items(ctx, job.ID); itemsErr == nil {
		for _, item := range items {
			if item.Status == "failed" {
				finalStatus = CompletedWithError
				break
			}
		}
	}
	if err := r.Store.SetStatus(ctx, job.ID, finalStatus, "", ""); err != nil {
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
