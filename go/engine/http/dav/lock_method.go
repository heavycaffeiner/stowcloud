//go:build linux

package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// LOCK and UNLOCK.
//
// The lock table itself lives in the service tier; what is here is the
// protocol around it: which body means a new lock and which means a refresh,
// what a lock on an unmapped URL creates, and how the answer is written.

// LockTaker is the lock table, as these two methods need it.
//
// Separate from the Locks interface the write guard uses, because a deployment
// can guard writes against a table it cannot mint into: the guard needs a
// read, and these need a write.
type LockTaker interface {
	// Take creates a lock and reports it.
	Take(ctx context.Context, req LockRequest) (Lock, error)
	// Refresh extends one its holder already has.
	Refresh(ctx context.Context, token string, principal int64, d time.Duration) (Lock, error)
	// Release drops one.
	Release(ctx context.Context, token string, principal int64) error
}

// LockRequest is what a LOCK asks the table for.
type LockRequest struct {
	// Share and Path address the resource.
	Share uint32
	Path  string
	// Key is the resource's durable identity, so the lock follows a rename.
	Key ResourceKey
	// Principal is the account taking it.
	Principal int64
	// Owner is the client's own description, as text.
	Owner string
	// Infinite is a depth-infinity lock, covering everything beneath.
	Infinite bool
	// Shared asks for the cooperative scope.
	Shared bool
	// Timeout is the lease asked for. Zero means the client expressed no
	// preference, and the table picks.
	Timeout time.Duration
}

// The refusals these two methods make.
var (
	// ErrNoLockTable reports a deployment that records no locks, on a request
	// that asked for one.
	ErrNoLockTable = errors.New("this deployment records no locks")
	// ErrNoLockToken is a refresh or an UNLOCK naming no token.
	ErrNoLockToken = errors.New("no lock token was submitted")
	// ErrBadLockDepth is a LOCK asking for depth one, which RFC 4918 does not
	// define for a lock: a lock covers a resource or a whole subtree.
	ErrBadLockDepth = errors.New("a lock depth other than 0 or infinity")
)

// Lock answers LOCK, both a new lock and a refresh.
func (h *Handler) Lock(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if h.taker == nil {
		h.failAllowing(w, r, res, ErrNoLockTable)
		return
	}
	if err := res.Require(acl.Write); err != nil {
		h.fail(w, r, err)
		return
	}

	timeout := ParseTimeout(r.Header.Get("Timeout"))

	// An empty body is a refresh: the token comes from the If header and there
	// is no document describing a lock to take.
	body, berr := io.ReadAll(http.MaxBytesReader(w, r.Body, h.limits.Bytes))
	if berr != nil {
		h.fail(w, r, berr)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		h.refresh(w, r, res, timeout)
		return
	}

	info, perr := ParseLockInfo(strings.NewReader(string(body)), h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}

	// Zero and infinity are the only depths a lock takes. Depth one would
	// cover a collection's members and not their members, which is a shape the
	// lock table has no way to record.
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity, DepthZero, DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}

	// Locking a path that resolves to nothing brings an empty resource into
	// being and locks that. The specification asks for this, and it is the
	// mechanism behind reserving a name ahead of writing to it. Answer 404
	// instead and the reservation cannot happen, leaving a client that takes a
	// lock before each PUT unable to create anything.
	status := http.StatusOK
	var entry core.Entry
	if st, serr := res.Root().Stat(res.Path()); serr == nil {
		entry = h.core.EntryAt(res, st)
	} else {
		created, cerr := h.createLockNull(r, res)
		if cerr != nil {
			h.fail(w, r, cerr)
			return
		}
		entry = created
		status = http.StatusCreated
	}

	var key ResourceKey
	if h.keyOf != nil {
		key = h.keyOf(entry)
	}

	got, lerr := h.taker.Take(r.Context(), LockRequest{
		Share:     uint32(res.Share()),
		Path:      res.Path().String(),
		Key:       key,
		Principal: int64(res.User()),
		Owner:     info.Owner,
		Infinite:  depth == DepthInfinity,
		Shared:    info.Shared,
		Timeout:   timeout,
	})
	if lerr != nil {
		h.fail(w, r, lerr)
		return
	}

	h.writeLock(w, r, got, status)
}

// createLockNull brings into being the placeholder a lock over an unresolved
// path needs.
//
// What it demands is creation rights, not the rights a lock over something
// existing would need. Permission to lock what is already there does not carry
// permission to make something new, and this puts a file into the share.
//
// The return is a domain entry and not a filesystem stat, keeping the protocol
// tier clear of types belonging under the domain.
func (h *Handler) createLockNull(r *http.Request, res core.Resolved) (core.Entry, error) {
	if err := res.Require(acl.Write | acl.Create); err != nil {
		return core.Entry{}, err
	}
	if err := h.guard(r, res); err != nil {
		return core.Entry{}, err
	}
	// No validator: the resource does not exist, so there is nothing to
	// compare against and a nil condition is what says "create it".
	return h.core.WriteStream(r.Context(), res, emptyReader{}, nil)
}

// refresh extends a lock the request already holds.
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request, res core.Resolved, timeout time.Duration) {
	tokens, err := h.precondition(r, res)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(tokens) == 0 {
		h.fail(w, r, ErrNoLockToken)
		return
	}

	got, rerr := h.taker.Refresh(r.Context(), tokens[0], int64(res.User()), timeout)
	if rerr != nil {
		h.fail(w, r, rerr)
		return
	}
	h.writeLock(w, r, got, http.StatusOK)
}

// Unlock answers UNLOCK.
func (h *Handler) Unlock(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if h.taker == nil {
		h.failAllowing(w, r, res, ErrNoLockTable)
		return
	}

	token := strings.TrimSpace(r.Header.Get("Lock-Token"))
	token = strings.TrimPrefix(token, "<")
	token = strings.TrimSuffix(token, ">")
	if token == "" {
		h.fail(w, r, ErrNoLockToken)
		return
	}

	if err := h.taker.Release(r.Context(), token, int64(res.User())); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeLock writes the answer both LOCK forms share.
func (h *Handler) writeLock(w http.ResponseWriter, r *http.Request, l Lock, status int) {
	// The token goes in the header as well as the body. RFC 4918 requires it,
	// and a client reading only the header still learns what it holds.
	w.Header().Set("Lock-Token", "<"+l.Token+">")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)

	// Written through the same element machinery a multistatus uses, so the
	// owner text goes out through the one piece of code that escapes rather
	// than through a second path that has to be trusted to match it.
	doc := NewPropDocument(w)
	doc.Write(Prop{
		Name:     davName("lockdiscovery"),
		Children: lockDiscovery([]Lock{l}),
	})
	if err := doc.Close(); err != nil {
		h.log(r).Warn("the lock response did not complete", "error", err)
	}
}

// emptyReader is the body of the empty resource a lock-null creates.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
