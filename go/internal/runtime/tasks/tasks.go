//go:build linux

// Package tasks runs generic recurring work owned by the application.
package tasks

import (
	"context"
	"log/slog"
	"sync"
	"time"

	kitTask "github.com/heavycaffeiner/stowcloud/go/internal/kit/task"
)

// Task is one recurring job.
type Task struct {
	// Name identifies the task in failure logs.
	Name string
	// Every is the delay between runs. The caller's preflight checks validate it.
	Every time.Duration
	// Run performs one pass. It receives the runner's cancellation context.
	Run func(context.Context) error
}

// Runner starts a set of recurring tasks once and drains them at shutdown.
//
// The zero value is usable except that it logs through slog.Default. A runner
// cannot be restarted after Stop: recurring work belongs to one engine
// lifetime, and allowing a second start would make a close race the old set.
type Runner struct {
	start sync.Once
	group kitTask.Group

	mu   sync.Mutex
	stop context.CancelFunc

	logger *slog.Logger
}

// New constructs a runner using logger for task failures and shutdown warnings.
// A nil logger uses the process default.
func New(logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{logger: logger}
}

// Start starts every task once. Each task runs immediately before waiting for
// its first interval.
func (r *Runner) Start(items []Task) {
	if r == nil {
		return
	}
	r.start.Do(func() {
		ctx, stop := context.WithCancel(context.Background())
		r.mu.Lock()
		r.stop = stop
		r.mu.Unlock()
		for _, item := range items {
			item := item
			r.group.Go(ctx, "periodic: "+item.Name, func() {
				r.run(ctx, item)
			})
		}
	})
}

// Stop cancels recurring work and waits for it to drain. The caller supplies
// the shutdown deadline; a deadline error means a task is still running.
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	stop := r.stop
	r.stop = nil
	r.mu.Unlock()
	if stop == nil {
		return nil
	}
	stop()
	if err := r.group.Wait(ctx); err != nil {
		r.log().Warn("periodic tasks did not stop before shutdown", "error", err)
		return err
	}
	return nil
}

func (r *Runner) run(ctx context.Context, item Task) {
	for {
		if err := item.Run(ctx); err != nil && ctx.Err() == nil {
			r.log().Warn("a periodic task failed", "task", item.Name, "error", err)
		}
		timer := time.NewTimer(item.Every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *Runner) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}
