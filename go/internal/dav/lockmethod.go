//go:build linux

package dav

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// LOCK and UNLOCK, and the write guard every mutating method runs.

func (h *Handler) lock(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if h.locks == nil {
		h.methodNotAllowed(w, r, res)
		return
	}
	if err := res.Require(acl.Write); err != nil {
		h.fail(w, r, err)
		return
	}

	timeout := ParseTimeout(r.Header.Get("Timeout"))
	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// An empty body is a refresh, and the token comes from the If header.
	if len(body) == 0 || isAllSpace(body) {
		h.refreshLock(w, r, res, timeout)
		return
	}

	owner, perr := ParseLockInfo(body, h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}
	if depth != DepthZero && depth != DepthInfinity {
		h.fail(w, r, errors.New("dav: a lock depth other than 0 or infinity"))
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	e := h.core.EntryAt(res, st)

	got, lerr := h.locks.Create(r.Context(), LockRequest{
		Ident:     identOf(e),
		Path:      string(e.Path.String()),
		Principal: res.User(),
		Owner:     owner,
		Depth:     depth,
		Timeout:   timeout,
	})
	if lerr != nil {
		h.fail(w, r, lerr)
		return
	}

	h.writeLockResponse(w, r, got, http.StatusOK)
}

func (h *Handler) refreshLock(w http.ResponseWriter, r *http.Request, res core.Resolved, timeout time.Duration) {
	tokens := h.submittedTokens(r)
	if len(tokens) == 0 {
		h.fail(w, r, errors.New("dav: a lock refresh with no token"))
		return
	}
	got, err := h.locks.Refresh(r.Context(), tokens[0], res.User(), timeout)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeLockResponse(w, r, got, http.StatusOK)
}

func (h *Handler) writeLockResponse(w http.ResponseWriter, r *http.Request, l ActiveLock, code int) {
	// The token goes in the header as well as the body: RFC 4918 requires it,
	// and a client that only reads the header still learns what it holds.
	w.Header().Set("Lock-Token", "<"+TokenURN(l.Token)+">")
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(code)

	// The body goes out through the same property writer a multistatus uses,
	// so the owner text and the lock root are escaped by the one piece of code
	// that does escaping rather than by a second path that has to be trusted to
	// match it.
	if err := writePropDocument(w, Prop{
		Name: DavName("lockdiscovery"),
		Raw:  lockDiscovery([]ActiveLock{l}),
	}); err != nil {
		h.logger(r).Warn("the lock response could not be written", "error", err)
	}
}

func (h *Handler) unlock(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if h.locks == nil {
		h.methodNotAllowed(w, r, res)
		return
	}
	raw := strings.TrimSpace(r.Header.Get("Lock-Token"))
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	if raw == "" {
		h.fail(w, r, errors.New("dav: UNLOCK with no Lock-Token"))
		return
	}
	if err := h.locks.Unlock(r.Context(), strings.TrimPrefix(raw, "urn:uuid:"), res.User()); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// submittedTokens is every state token the request's If header asserts.
func (h *Handler) submittedTokens(r *http.Request) []string {
	raw := r.Header.Get("If")
	if raw == "" {
		return nil
	}
	parsed, err := ParseIf(raw)
	if err != nil {
		return nil
	}
	out := parsed.Tokens()
	for i, t := range out {
		out[i] = strings.TrimPrefix(t, "urn:uuid:")
	}
	return out
}

// guardWrite is what every mutating method runs before it touches anything.
//
// It applies the If header as a precondition and then checks the lock table.
// The two produce different statuses: an If header that parsed and did not
// hold is 412, and a lock the request did not submit a token for is 423.
func (h *Handler) guardWrite(r *http.Request, res core.Resolved, path string) error {
	if raw := r.Header.Get("If"); raw != "" {
		parsed, err := ParseIf(raw)
		if err != nil {
			return err
		}
		st, serr := h.resourceState(r, res, path)
		if serr != nil {
			return serr
		}
		if !parsed.Evaluate(func(_ string, _ bool) ResourceState { return st }) {
			return ErrPreconditionFailed
		}
	}
	if h.locks == nil {
		return nil
	}
	return h.locks.Guard(r.Context(), int64(res.Share()), path, res.User(), h.submittedTokens(r))
}

// resourceState is what an If condition is evaluated against.
func (h *Handler) resourceState(r *http.Request, res core.Resolved, path string) (ResourceState, error) {
	out := ResourceState{}
	st, err := res.Root().Stat(res.Path())
	if err == nil {
		out.Exists = true
		e := h.core.EntryAt(res, st)
		out.ETag, out.Weak = e.ETag, e.ETagWeak
	}
	if h.locks != nil {
		held, lerr := h.locks.At(r.Context(), int64(res.Share()), path)
		if lerr != nil {
			return ResourceState{}, lerr
		}
		for _, l := range held {
			out.Tokens = append(out.Tokens, TokenURN(l.Token), l.Token)
		}
	}
	return out, nil
}
