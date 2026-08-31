// Package task holds the only place in the engine that may start a goroutine
// with a bare go statement. Everywhere else that needs concurrency goes
// through Go, so a panic on that goroutine never takes the whole process
// down with whatever else was in flight.
package task

import (
	"context"
	"log/slog"
	"runtime/debug"
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
