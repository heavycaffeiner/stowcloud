//go:build linux

package dav

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The handler every method hangs off, and the few things they all share.
//
// It holds no routing. A mount resolves the request path against a share,
// evaluates the grant, and hands a resolution in; what is here decides what a
// method does with one. That split is why no method below re-checks a
// permission.

// Locks is the lock table, as this package needs it.
//
// An interface rather than the service type: this tier may name a service, but
// a narrow contract states what the protocol actually depends on and lets a
// test drive a method without a database behind it.
type Locks interface {
	// Guard reports whether a write may proceed against the locks covering a
	// path, given the tokens the request submitted.
	Guard(ctx context.Context, share uint32, path string, principal int64, submitted []string) error
}

// Options configures a handler.
type Options struct {
	// Core is the domain. Required.
	Core *core.Core
	// Locks guards writes. Nil serves a deployment with no lock table, where
	// every write proceeds: a server that cannot record a lock must not
	// refuse every write on the grounds that it might be locked.
	Locks Locks
	// TokensAt lists the lock tokens covering a path, for evaluating an If
	// header that names one. Nil leaves every resource holding none, which
	// makes a condition asserting a token fail rather than pass unchecked.
	TokensAt func(ctx context.Context, share uint32, path string) []string
	// LocksAt describes the locks covering a path, which lockdiscovery
	// renders. Nil reports every resource as unlocked, which is what a
	// deployment with no lock table is.
	LocksAt func(ctx context.Context, share uint32, path string) []Lock
	// Taker mints and releases locks, for LOCK and UNLOCK. Nil refuses both
	// rather than pretending: a client told it holds a lock that was never
	// recorded writes on the strength of it.
	Taker LockTaker
	// InfinityEntries bounds what a Depth: infinity walk will attempt. Zero
	// takes DefaultInfinityEntries.
	InfinityEntries int
	// Store holds the dead properties. Nil is a deployment that keeps none,
	// and then a delete has nothing to clean up.
	Store Store
	// KeyOf names the resource a property belongs to. Nil disables the
	// property store, since rows nothing can key are rows nothing can read.
	KeyOf KeyOf
	// Uploads backs the chunked upload collection. Nil is a deployment
	// without one, which answers 405 there rather than half-serving it.
	Uploads Uploads
	// UploadHeaders names the headers that collection reads. A partly filled
	// set disables it, since honouring one header and ignoring another is how
	// a client's declared length disappears without a word.
	UploadHeaders UploadHeaders
	// Sources answer SEARCH and REPORT for the vocabularies they claim. Empty
	// is a deployment that answers neither, which the method refusal states
	// rather than an empty result hiding.
	Sources []QuerySource
	// VendorProps contributes properties in vocabularies this package does
	// not own, such as a sync client's namespace. Consulted for named
	// requests; a name the source does not answer falls through to the
	// missing list, which reports it as a 404 inside the document.
	VendorProps func(ctx context.Context, res core.Resolved, e core.Entry, want []xml.Name) []Prop
	// Limits bound what one request may carry. The zero value takes
	// DefaultLimits, because a zero bound is not "unbounded" here: several of
	// these are counts a parser compares against, so leaving them at zero
	// refuses the first condition of every If header.
	Limits Limits
	// Logger receives what cannot be reported in a response, such as a body
	// that stopped after the status was sent.
	Logger *slog.Logger
}

// Handler answers the WebDAV methods.
type Handler struct {
	core            *core.Core
	locks           Locks
	store           Store
	keyOf           KeyOf
	tokensAt        func(ctx context.Context, share uint32, path string) []string
	locksAt         func(ctx context.Context, share uint32, path string) []Lock
	taker           LockTaker
	uploads         Uploads
	uploadHeaders   UploadHeaders
	sources         []QuerySource
	vendorProps     func(ctx context.Context, res core.Resolved, e core.Entry, want []xml.Name) []Prop
	limits          Limits
	infinityEntries int
	logger          *slog.Logger
}

// New builds a handler.
func New(opt Options) *Handler {
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limits := opt.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	infinity := opt.InfinityEntries
	if infinity <= 0 {
		infinity = DefaultInfinityEntries
	}
	return &Handler{
		core:            opt.Core,
		locks:           opt.Locks,
		store:           opt.Store,
		keyOf:           opt.KeyOf,
		tokensAt:        opt.TokensAt,
		locksAt:         opt.LocksAt,
		taker:           opt.Taker,
		uploads:         opt.Uploads,
		uploadHeaders:   opt.UploadHeaders,
		sources:         opt.Sources,
		vendorProps:     opt.VendorProps,
		limits:          limits,
		infinityEntries: infinity,
		logger:          logger,
	}
}

// log returns the logger, annotated with the request it is reporting about.
func (h *Handler) log(r *http.Request) *slog.Logger {
	return h.logger.With("method", r.Method, "path", r.URL.Path)
}

// fail writes the response for an error.
//
// A refused write is logged. A client shows whoever is holding it the status
// phrase and nothing else, so without this an operator answering "the upload
// says method not allowed" cannot see which request that was, or whether it
// reached this server at all. Reads stay quiet: a sync client probes for
// absent paths as a matter of course, and logging those buries the one line
// that matters.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, _ := StatusOf(err)
	switch {
	case status >= http.StatusInternalServerError:
		h.log(r).Error("the request failed", "error", err)
	case status >= http.StatusBadRequest && writes(r.Method):
		h.log(r).Warn("the write was refused", "status", status, "error", err)
	}
	if werr := WriteError(w, err); werr != nil {
		h.log(r).Warn("the refusal did not reach the client", "error", werr)
	}
}

// writes reports whether a method changes something, which is what makes its
// refusal worth a line in the log.
func writes(method string) bool {
	req, served := MethodRequirement(method)
	return served && (req.Source&^acl.Read != 0 || req.HasDest())
}

// failAllowing writes the refusal, and states what the resource does accept
// when the answer is that this method is not one of them.
//
// A 405 without the header is incomplete: the reference client does not map
// the status for a write, so it surfaces the reason phrase to whoever is
// holding the phone, and neither they nor a proxy is told what the resource
// would have taken.
func (h *Handler) failAllowing(
	w http.ResponseWriter, r *http.Request, res core.Resolved, err error,
) {
	if status, _ := StatusOf(err); status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", h.allowFor(res))
	}
	h.fail(w, r, err)
}

// closing releases something whose close failure has nowhere to go.
func (h *Handler) closing(r *http.Request, c io.Closer, what string) {
	if err := c.Close(); err != nil {
		h.log(r).Warn("closing "+what, "error", err)
	}
}

// guard is what every mutating method runs before it touches anything.
//
// Two questions in order, answering differently. An If header that parsed and
// did not hold is 412, because the client stated a condition about the
// server's state that was false. A lock the request submitted no token for is
// 423, because the state is fine and the caller lacks the key.
//
// A deployment with no lock table admits the write. Refusing everything
// because a lock cannot be recorded would turn an absent feature into an
// outage, and "an unrecorded lock might exist" is true of every write there.
func (h *Handler) guard(r *http.Request, res core.Resolved) error {
	submitted, err := h.precondition(r, res)
	if err != nil {
		return err
	}
	if h.locks == nil {
		return nil
	}
	if gerr := h.locks.Guard(r.Context(),
		uint32(res.Share()), res.Path().String(), int64(res.User()), submitted); gerr != nil {
		// The service's sentinel becomes the protocol's, so the status table
		// answers 423 carrying its precondition element.
		return ErrLocked
	}
	return nil
}

// precondition evaluates the If header and reports the tokens it submitted.
//
// Tokens come from evaluation rather than from the parse. A token named inside
// a list that did not hold was never submitted, and one behind a Not was named
// to assert the lock's absence: counting either would let a request unlock
// something by mentioning it.
func (h *Handler) precondition(r *http.Request, res core.Resolved) ([]string, error) {
	header := r.Header.Get("If")
	if header == "" {
		return nil, nil
	}

	parsed, err := ParseIf(header, h.limits, r.Host)
	if err != nil {
		return nil, err
	}

	satisfied, submitted := EvaluateIf(parsed, res.Path().Components(), h.stateOf(r, res))
	if !satisfied {
		return nil, ErrPreconditionFailed
	}
	return submitted, nil
}

// stateOf reads a resource's validator and held tokens, for the If evaluation.
//
// Only the request's own target is answered. A tagged list naming another
// resource gets the zero state, which holds no token and matches no tag, so a
// condition about a path this request did not resolve fails rather than being
// assumed true.
func (h *Handler) stateOf(r *http.Request, res core.Resolved) StateOf {
	target := res.Path().Components()
	return func(path []string) ResourceState {
		if !sameComponents(path, target) {
			return ResourceState{}
		}
		st, err := res.Root().Stat(res.Path())
		if err != nil {
			return ResourceState{}
		}
		entry := h.core.EntryAt(res, st)
		state := ResourceState{
			Exists: true,
			ETag:   ETag{Value: entry.ETag, Weak: entry.ETagWeak},
		}
		if h.tokensAt != nil {
			state.Tokens = h.tokensAt(r.Context(), uint32(res.Share()), res.Path().String())
		}
		return state
	}
}

// sameComponents compares two split paths.
func sameComponents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// httpDateOf renders a timestamp as an HTTP date.
func httpDateOf(ns int64) string {
	return time.Unix(0, ns).UTC().Format(httpDate)
}
