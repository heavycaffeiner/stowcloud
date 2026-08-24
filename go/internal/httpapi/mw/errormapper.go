package mw

import (
	"bufio"
	"context"
	"net"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
)

// errKey is the per-request holder a handler's error lands in. The handler
// wrapper writes into it, and ErrorMapper reads it after the handler returns,
// which is what keeps the response a single construction: the handler returns
// a typed error and never chooses a status.
type errKey struct{}

// RecordError stores the handler's failure for ErrorMapper to render. It is
// called by the route dispatch wrapper, which is inside this middleware.
func RecordError(r *http.Request, err error) {
	if h, ok := r.Context().Value(errKey{}).(*errHolder); ok {
		h.err = err
	}
}

type errHolder struct{ err error }

// ErrorMapper is step 11. It is the innermost layer of the chain: it wraps the
// dispatch, so it sees the handler's returned error before anything above it
// can, and it is the only place on the native REST surface that turns a domain
// error into a status and a body.
//
// It also recovers a panicking handler. A panic is a 500 like any other
// unhandled failure: the trace id is already in the context, so the log line
// and the response correlate, and the process survives a bug.
func ErrorMapper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holder := &errHolder{}
		r = r.WithContext(context.WithValue(r.Context(), errKey{}, holder))
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				rec.status = http.StatusInternalServerError
				apierr.Write(w, http.StatusInternalServerError, apierr.Internal())
				slogPanic(r, p)
			}
		}()
		next.ServeHTTP(rec, r)
		if holder.err != nil {
			status, aerr := apierr.Map(holder.err)
			if rec.status == 0 {
				apierr.Write(w, status, aerr)
			}
			// A handler that both wrote and failed has already answered;
			// the response stands and the failure is this layer's to log.
			slogHandlerError(r, holder.err)
		}
	})
}

// statusRecorder tracks whether the inner handler wrote a response, so
// ErrorMapper knows it may still choose the status.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Hijack passes the connection through to the WebSocket upgrader. The change
// channel sits inside this chain, so the recorder has to be a transparent
// link in the hijack path or an upgraded socket never reaches the handler.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// Flush passes the flush through; a response that must reach a slow client
// without buffering cannot be blocked by the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
