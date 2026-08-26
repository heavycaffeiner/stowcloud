// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

// The health surface.
//
// A degraded server is a configuration state rather than something a restart
// fixes, so the process check exits zero for both states and this endpoint
// carries the difference. Mapping degraded to unhealthy would make a container
// runtime restart-loop a problem forever without resolving it.
//
// Every degradation names itself. "Degraded" with no reason is a status an
// operator cannot act on, and the reasons are the whole value of the endpoint.

// The health states.
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
)

// The reason kinds this server can report. They are constants because a
// reason spelled differently by two callers is two reasons to whoever is
// reading the output.
const (
	// ReasonShareRejected is a share the filesystem gate refused, or one whose
	// nested mount it refused.
	ReasonShareRejected = "share_rejected"
	// ReasonSMBBindFailed is the sidecar configuration failing to render or
	// the sidecar being unreachable.
	ReasonSMBBindFailed = "smb_bind_failed"
	// ReasonSMBStale is a change this server made that did not reach the SMB
	// sidecar. It is reported rather than failing the write, because the write
	// itself succeeded and this server enforces it; what is behind is the
	// second surface. A revoked grant still live over SMB is the case that
	// makes this worth an operator's attention.
	ReasonSMBStale = "smb_stale"
	// ReasonDatabaseSizeGuard is the store's size bound having tripped.
	ReasonDatabaseSizeGuard = "database_size_guard"
	// ReasonPreviewPoolUnavailable is the preview workers being unusable.
	ReasonPreviewPoolUnavailable = "preview_pool_unavailable"
	// ReasonHardening is a sandbox layer that was asked for and not applied.
	// New in this port: without it, a server running with a layer missing
	// looks exactly like one running with all of them.
	ReasonHardening = "hardening"
	// ReasonSearchIndexDisabled is a corrupt index that was reported and
	// switched off rather than being allowed to fail every query.
	ReasonSearchIndexDisabled = "search_index_disabled"
)

// HealthReason is one degradation.
type HealthReason struct {
	// Kind is one of the constants above.
	Kind string `json:"kind"`
	// Detail says which share, which layer or which bound, because the kind
	// alone does not tell an operator where to look.
	//
	// It is a token rather than a sentence, for the same reason every other
	// refusal in this tree is: a sentence is a thing to translate and a thing
	// whose wording a caller starts matching on.
	Detail string `json:"detail"`
}

// HealthState collects the degradations the running server has. It is a value
// the server owns and hands to the handler, rather than package state, because
// a status every caller can reach into is a status any of them can rewrite.
type HealthState struct {
	mu      sync.Mutex
	reasons map[string]HealthReason
}

// NewHealthState builds an empty one, which reports ok.
func NewHealthState() *HealthState {
	return &HealthState{reasons: map[string]HealthReason{}}
}

// Degrade records a degradation. Recording the same kind and detail twice is
// one reason, so a check that runs on a timer does not grow the list.
func (h *HealthState) Degrade(kind, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reasons[kind+"\x00"+detail] = HealthReason{Kind: kind, Detail: detail}
}

// Resolve removes a degradation that has been fixed.
//
// A share that was refused stays refused until it is reconfigured, but a
// preview pool that came back is not still down, and a status that only ever
// gets worse stops being read.
func (h *HealthState) Resolve(kind, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.reasons, kind+"\x00"+detail)
}

// ResolveKind removes every degradation of one kind, whatever detail each
// carries.
//
// For a condition whose detail names which of several bounds tripped: the
// size guard reports the free-space floor or the size ceiling, and a caller
// clearing it knows the condition is over without knowing which one it was.
// Resolving by the exact detail would leave the other one standing forever.
func (h *HealthState) ResolveKind(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, r := range h.reasons {
		if r.Kind == kind {
			delete(h.reasons, k)
		}
	}
}

// ResolveShare clears every degradation recorded against one share.
//
// The detail a share is degraded under is "name:reason", and the reason it
// broke with is not necessarily the one it is carrying now: a mount that went
// missing and came back on a filesystem this server refuses would have been
// recorded twice under different reasons. Clearing by name is what makes a
// recovered share actually stop being reported.
func (h *HealthState) ResolveShare(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prefix := name + ":"
	for k, r := range h.reasons {
		if r.Kind == ReasonShareRejected && strings.HasPrefix(r.Detail, prefix) {
			delete(h.reasons, k)
		}
	}
}

// Reasons returns the current degradations, in a stable order so two reads of
// an unchanged state are the same answer.
func (h *HealthState) Reasons() []HealthReason {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]HealthReason, 0, len(h.reasons))
	for _, r := range h.reasons {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// Status is the state the endpoint reports.
func (h *HealthState) Status() string {
	if len(h.Reasons()) == 0 {
		return HealthOK
	}
	return HealthDegraded
}

// Health answers GET /api/health.
//
// It is reachable without a session, because a health endpoint that needs a
// credential is one the container runtime cannot use. It therefore says what is
// degraded and never anything about the deployment beyond that: no host names,
// no paths outside a share's configured name, no version.
func Health(state *HealthState) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, _ *http.Request) error {
		reasons := state.Reasons()
		status := HealthOK
		if len(reasons) > 0 {
			status = HealthDegraded
		}
		// The list is always present, empty rather than absent, so a client
		// reading it does not have to handle two shapes for "nothing wrong".
		return writeJSON(w, http.StatusOK, map[string]any{
			"status":  status,
			"reasons": reasons,
		})
	})
}
