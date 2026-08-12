// Package task holds the only goroutine spawn in this tree. A bare go statement
// anywhere else is rejected by the vetgo analyser in the gate.
//
// The reason is the release build's shape rather than taste: a panic in a
// goroutine with no recover takes the process down, and every request in flight
// with it, wherever that goroutine came from.
package task

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine with a recover installed. A panic is logged
// with the stack and fails that unit of work; the process survives. The context
// is passed to the logger rather than used for cancellation, so a handler that
// carries a request id renders it alongside the stack.
//
// Nothing else in this tree may start a goroutine.
func Go(ctx context.Context, name string, fn func()) {
	go func() {
		defer Recover(ctx, name)
		fn()
	}()
}

// Recover is the same protection for a goroutine this package cannot start:
// the one in Go above, and an HTTP handler, which runs on a goroutine
// net/http started. Deferred, never called directly.
func Recover(ctx context.Context, name string) {
	v := recover()
	if v == nil {
		return
	}
	slog.ErrorContext(ctx, "recovered from a panic",
		slog.String("task", name),
		slog.Any("panic", v),
		slog.String("stack", string(debug.Stack())))
}
