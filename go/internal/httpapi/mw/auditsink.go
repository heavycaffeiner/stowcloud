package mw

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// AuditSink is step 12. It sits outside ErrorMapper so the line it writes
// carries the final status after the mapper chose it, and it writes nothing
// for the ordinary successful read, which is the request that has no story.
//
// The line is a log line, not a durable row: the durable audit log is where
// an action with an actor is recorded by the handler that performed it. This
// layer's job is the shape of the request traffic, and a structured log line
// is that shape.
func AuditSink(log *slog.Logger, clk clock.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 400 || stateChanging(r.Method) {
				log.Info("request",
					"trace", TraceFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"duration", clk.Since(start).Round(time.Microsecond).String(),
				)
			}
		})
	}
}

// slogPanic logs a recovered panic with the trace that correlates it to the
// 500 the client received.
func slogPanic(r *http.Request, p any) {
	slog.Error("panic in a handler",
		"trace", TraceFrom(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"panic", p,
	)
}

// slogHandlerError logs a handler failure whose response ErrorMapper already
// rendered. The status is known to the mapper; here only the cause is logged.
func slogHandlerError(r *http.Request, err error) {
	slog.Warn("handler failed",
		"trace", TraceFrom(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
}
