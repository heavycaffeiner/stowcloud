//go:build linux

// Package tasks runs generic recurring work owned by the application.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	kitTask "github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
)

// Task is one recurring job.
type Task struct {
	// Name identifies the task in failure logs.
	Name string
	// Every is the delay between runs. Runtime schedule construction sets it;
	// the caller's preflight checks validate it before starting work.
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

// Policy contains the application-owned operations used by the recurring
// schedule. The runtime package owns cadence and task identity, while the
// application supplies narrow callbacks for its stores and feature services.
type Policy struct {
	Now                    func() int64
	SweepDavLocks          func(context.Context, int64) error
	SweepLoginFlowMaterial func(context.Context, int64) (int64, error)
	SweepLoginFlows        func(context.Context, int64) (int64, error)
	ProbeShares            func(context.Context) error
	SweepUploads           func(context.Context) error
	SweepDirectTransfers   func(context.Context) error
	RecoverSearch          func(context.Context) error
}

// RequiredTasks names every recurring operation in the application schedule,
// with the reason it exists. Keeping this beside Schedule prevents the HTTP
// transport from becoming a second owner of task identity.
func RequiredTasks() map[string]string {
	return map[string]string{
		"share.probe":           "rechecks share roots so a vanished mount shows as broken rather than as an empty directory",
		"dav.locks.sweep":       "expires WebDAV locks whose holder never came back",
		"login.flow.sweep":      "expires single-sign-on flows that were started and never finished",
		"upload.sweep":          "collects abandoned upload sessions and their part files",
		"direct-transfer.sweep": "collects expired S3 multipart reservations and releases their quota",
		"search.maintenance":    "keeps the index in step with the corpus",
	}
}

// Validate reports every way a recurring task table is malformed or differs
// from the schedule contract.
func Validate(items []Task) error {
	var problems []string
	seen := map[string]int{}
	for i, item := range items {
		switch {
		case strings.TrimSpace(item.Name) == "":
			problems = append(problems, fmt.Sprintf("the task at position %d has no name", i))
			continue
		case item.Every <= 0:
			problems = append(problems, fmt.Sprintf("the task %s has no interval", item.Name))
		case item.Run == nil:
			problems = append(problems, fmt.Sprintf("the task %s has no function", item.Name))
		}
		seen[item.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			problems = append(problems, fmt.Sprintf("the task %s appears %d times", name, n))
		}
	}
	required := RequiredTasks()
	for name, why := range required {
		if seen[name] == 0 {
			problems = append(problems, fmt.Sprintf("the task %s is missing: it %s", name, why))
		}
	}
	for name := range seen {
		if _, ok := required[name]; !ok {
			problems = append(problems, fmt.Sprintf("the task %s is not required by the application schedule", name))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("periodic tasks: %s", joinProblems(problems))
}

// TaskNames lists task names in sorted order for diagnostics.
func TaskNames(items []Task) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func joinProblems(problems []string) string {
	slices.Sort(problems)
	return strings.Join(problems, "; ")
}

const (
	sweepInterval     = 5 * time.Minute
	probeInterval     = time.Minute
	loginFlowLifetime = 20 * time.Minute
)

// Schedule builds the recurring work table. Optional feature callbacks are
// represented by their owning task because a deployment may legitimately have
// no instance of that feature; missing work is never a fabricated task.
func Schedule(p Policy) []Task {
	if p.Now == nil {
		panic("scheduled work requires a clock")
	}
	now := p.Now
	sweepLogin := func(ctx context.Context) error {
		if p.SweepLoginFlowMaterial == nil || p.SweepLoginFlows == nil {
			return fmt.Errorf("login flow sweep callback unavailable")
		}
		cutoff := now() - int64(loginFlowLifetime)
		if _, err := p.SweepLoginFlowMaterial(ctx, cutoff); err != nil {
			return fmt.Errorf("clearing login flow material: %w", err)
		}
		if _, err := p.SweepLoginFlows(ctx, cutoff); err != nil {
			return fmt.Errorf("sweeping login flows: %w", err)
		}
		return nil
	}
	call := func(name string, fn func(context.Context) error) func(context.Context) error {
		if fn == nil {
			return func(context.Context) error { return fmt.Errorf("%s callback unavailable", name) }
		}
		return fn
	}
	return []Task{
		{Name: "dav.locks.sweep", Every: sweepInterval, Run: func(ctx context.Context) error {
			if p.SweepDavLocks == nil {
				return fmt.Errorf("dav lock sweep callback unavailable")
			}
			if err := p.SweepDavLocks(ctx, now()); err != nil {
				return fmt.Errorf("sweeping WebDAV locks: %w", err)
			}
			return nil
		}},
		{Name: "login.flow.sweep", Every: sweepInterval, Run: sweepLogin},
		{Name: "share.probe", Every: probeInterval, Run: call("share probe", p.ProbeShares)},
		{Name: "upload.sweep", Every: sweepInterval, Run: call("upload sweep", p.SweepUploads)},
		{Name: "direct-transfer.sweep", Every: sweepInterval, Run: call("direct transfer sweep", p.SweepDirectTransfers)},
		{Name: "search.maintenance", Every: probeInterval, Run: call("search maintenance", p.RecoverSearch)},
	}
}
