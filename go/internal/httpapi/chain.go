package httpapi

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
)

// Step is one entry in the chain table. The table is data so the order test
// can replay it with recording stubs, and Chain composes the same table the
// test asserts, so a reorder is caught by both.
type Step struct {
	Name string
	Wrap func(*State, http.Handler) http.Handler
}

// steps is the execution order, and the source order is the execution order:
// the list is the code, not a comment about the code. The two deliberate
// inversions against the numbered list are the ones the proposal names:
// AuditSink wraps ErrorMapper so it sees the response after the mapper chose
// its status, and ErrorMapper is innermost so the handler's error never
// escapes the one place that maps it.
func steps() []Step {
	return []Step{
		{Name: "RequestID", Wrap: func(_ *State, h http.Handler) http.Handler { return mw.RequestID(h) }},
		{Name: "TrustedProxy", Wrap: func(s *State, h http.Handler) http.Handler { return mw.TrustedProxy(s.Trusted)(h) }},
		{Name: "HostGuard", Wrap: func(s *State, h http.Handler) http.Handler { return mw.HostGuard(s.AppHosts, s.ContentHosts)(h) }},
		{Name: "SecurityHeaders", Wrap: func(_ *State, h http.Handler) http.Handler { return mw.SecurityHeaders(h) }},
		{Name: "RateLimit", Wrap: func(s *State, h http.Handler) http.Handler { return mw.RateLimit(s.Limiter)(h) }},
		{Name: "BodyLimit", Wrap: func(s *State, h http.Handler) http.Handler { return mw.BodyLimit(int64(1<<20), h) }},
		{Name: "Auth", Wrap: func(s *State, h http.Handler) http.Handler { return mw.Auth(s.Auth, mw.PublicPaths)(h) }},
		{Name: "CSRF", Wrap: func(s *State, h http.Handler) http.Handler { return mw.CSRF(s.CSRFKey, s.AppHosts)(h) }},
		{Name: "ACLScope", Wrap: func(s *State, h http.Handler) http.Handler { return mw.ACLScope(s.ScopeLookup)(h) }},
		{Name: "AuditSink", Wrap: func(s *State, h http.Handler) http.Handler { return mw.AuditSink(s.Log, s.Clock)(h) }},
		{Name: "ErrorMapper", Wrap: func(_ *State, h http.Handler) http.Handler { return mw.ErrorMapper(h) }},
	}
}

// Chain composes the steps in request order around a handler. The outermost
// step runs first, and the composition below wraps from the innermost, which
// is the end of the table: ErrorMapper, then AuditSink, then everything out
// to RequestID.
func Chain(s *State) func(http.Handler) http.Handler {
	st := steps()
	return func(next http.Handler) http.Handler {
		h := next
		for i := len(st) - 1; i >= 0; i-- {
			h = st[i].Wrap(s, h)
		}
		return h
	}
}

// ScopeLookup is the route table's requirement resolver, installed by the
// wiring that builds the mux from the same table. It is a field here so the
// chain and the mux share one source of truth.
func (s *State) ScopeLookup(method, path string) (route.Requirement, bool) {
	if s.lookup == nil {
		return route.Requirement{}, false
	}
	return s.lookup(method, path)
}

// SetLookup installs the table-backed resolver. Only the wiring calls it,
// once, before the server starts.
func (s *State) SetLookup(l route.Lookup) { s.lookup = l }
