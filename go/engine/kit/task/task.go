// Package task holds the only place in the engine that may start a goroutine
// with a bare go statement. Everywhere else that needs concurrency goes
// through Go, so a panic on that goroutine never takes the whole process
// down with whatever else was in flight.
package task

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// Go starts fn on a new goroutine with a panic recovery installed. If fn
// panics, the panic is logged with its stack trace and name, and the
// goroutine ends there; nothing propagates past this function.
//
// ctx is used only to carry request-scoped values into the log record on a
// panic (a request id, for example). It has no effect on fn's lifetime: Go
// does not cancel fn when ctx is canceled.
func Go(ctx context.Context, name string, fn func()) {
	go func() {
		defer Recover(ctx, name)
		fn()
	}()
}

// Group is a set of goroutines something else has to wait for.
//
// Detached work that writes to a file the process is about to close needs an
// answer to "is it finished". A copy started by a request outlives that
// request by design, and without this it also outlived the databases: the
// close ran first and the copy then wrote its outcome into a closed one.
//
// The zero value is usable. A nil Group starts the work anyway, untracked, so
// a caller with nothing to wait on is not a caller that has to branch.
type Group struct {
	wg sync.WaitGroup
}

// Go starts fn like the package function does, counted so Wait can see it.
func (g *Group) Go(ctx context.Context, name string, fn func()) {
	if g == nil {
		Go(ctx, name, fn)
		return
	}
	g.wg.Add(1)
	Go(ctx, name, func() {
		defer g.wg.Done()
		fn()
	})
}

// Wait blocks until every goroutine started through this group has returned,
// or until ctx ends.
//
// A deadline that passes returns ctx's error rather than blocking forever:
// work that will not finish is a reason to say so, not a reason to hang a
// shutdown. Those goroutines keep running, which is the honest outcome, and
// the caller decides what to do about it.
func (g *Group) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	done := make(chan struct{})
	Go(ctx, "task: group wait", func() {
		g.wg.Wait()
		close(done)
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recover stops a panic from unwinding past the goroutine it runs on. Call it
// only as a defer, at the top of a goroutine this package did not start (an
// HTTP handler's own goroutine, for instance); calling it directly does
// nothing because recover only works inside a deferred function.
func Recover(ctx context.Context, name string) {
	r := recover()
	if r == nil {
		return
	}
	slog.ErrorContext(ctx, "recovered panic",
		slog.String("task", name),
		slog.Any("panic", r),
		slog.String("stack", string(debug.Stack())),
	)
}
