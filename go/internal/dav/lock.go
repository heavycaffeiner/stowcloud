//go:build linux

package dav

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// Exclusive write locks, stored in state.db so a restart does not silently
// drop every lock a client believes it holds. Office clients in particular
// hold a lock across a save and get confused when the server forgets.

// Lock depth. Only zero and infinity exist in RFC 4918.
const (
	DepthZero     = 0
	DepthInfinity = -1
)

// Lock timeouts. A client asks and the server decides; an unbounded lease on
// a durable table is a resource a client can pin for free.
const (
	defaultLockTimeout = 5 * time.Minute
	maxLockTimeout     = 60 * time.Minute
)

// ActiveLock is one live lock, as the lockdiscovery property and the LOCK
// response render it.
type ActiveLock struct {
	Token     string
	Path      string
	Owner     string
	Depth     int
	Principal core.UserID
	ExpiresNs int64
	TimeoutS  int64
	// Shared is the scope, which lockdiscovery renders: a client that asked
	// for a shared lock and was told "exclusive" would believe it holds
	// something nobody else can take.
	Shared bool
}

// LockStore is the durable half. It is an interface so the tests can drive the
// manager without a database, not to admit a second implementation.
type LockStore interface {
	DavLocks(ctx context.Context, nowNs int64) ([]state.DavLock, error)
	PutDavLock(ctx context.Context, l state.DavLock, nowNs int64) error
	RefreshDavLock(ctx context.Context, token string, expiresNs, timeoutS, nowNs int64) error
	DropDavLock(ctx context.Context, token string) error
}

// Locks is the lock table.
type Locks struct {
	store LockStore
	clk   clock.Clock
}

// NewLocks builds the lock manager.
func NewLocks(store LockStore, clk clock.Clock) *Locks {
	return &Locks{store: store, clk: clk}
}

// The lock scopes RFC 4918 defines, as stored in the scope column.
const (
	// ScopeExclusive is one holder. A second lock of either scope is refused.
	ScopeExclusive int64 = 0
	// ScopeShared is many holders. Another shared lock is admitted and an
	// exclusive one is refused, which is the whole of the difference.
	ScopeShared int64 = 1
)

// LockRequest is what a LOCK method asks for.
type LockRequest struct {
	Ident state.Ident
	// Path is the virtual path, which is what a depth-infinity lock's
	// descendant check reads. The identity moves with the file; the path is
	// what the client asked about.
	Path      string
	Principal core.UserID
	Owner     string
	Depth     int
	Timeout   time.Duration
	// Shared asks for a shared lock rather than an exclusive one.
	Shared bool
}

// Create takes a write lock, exclusive or shared.
//
// An exclusive lock refuses when the resource, an ancestor of it, or (for a
// depth-infinity request) anything under it is already locked by anyone. A
// shared lock refuses only against an exclusive one: two shared locks on the
// same resource is what shared means, and it is how two clients cooperate on
// one file without either being able to lock the other out.
func (l *Locks) Create(ctx context.Context, req LockRequest) (ActiveLock, error) {
	now := l.clk.Now().UnixNano()
	held, err := l.store.DavLocks(ctx, now)
	if err != nil {
		return ActiveLock{}, err
	}

	scope := ScopeExclusive
	if req.Shared {
		scope = ScopeShared
	}

	for _, h := range held {
		// Two shared locks coexist. Every other pairing conflicts, which is
		// exactly the table RFC 4918 gives.
		if scope == ScopeShared && h.Scope == ScopeShared {
			continue
		}
		if covers(h, req.Ident.Share, req.Path) {
			return ActiveLock{}, fmt.Errorf("%w: %s is held", ErrLocked, h.Path)
		}
		if req.Depth == DepthInfinity && isUnder(h, req.Ident.Share, req.Path) {
			return ActiveLock{}, fmt.Errorf(
				"%w: a depth-infinity lock cannot be taken over the locked %s", ErrLocked, h.Path)
		}
	}

	token, err := newLockToken()
	if err != nil {
		return ActiveLock{}, err
	}
	timeout := clampTimeout(req.Timeout)
	expires := now + timeout.Nanoseconds()

	row := state.DavLock{
		Token:     token,
		Ident:     req.Ident,
		Path:      req.Path,
		Principal: int64(req.Principal),
		Owner:     req.Owner,
		Depth:     int64(req.Depth),
		Scope:     scope,
		ExpiresNs: expires,
		TimeoutS:  int64(timeout.Seconds()),
	}
	if err := l.store.PutDavLock(ctx, row, now); err != nil {
		return ActiveLock{}, err
	}
	return ActiveLock{
		Token: token, Path: req.Path, Owner: req.Owner, Depth: req.Depth,
		Principal: req.Principal, ExpiresNs: expires, TimeoutS: row.TimeoutS,
		Shared: req.Shared,
	}, nil
}

// Refresh extends a lock the caller already holds.
func (l *Locks) Refresh(ctx context.Context, token string, principal core.UserID, d time.Duration) (ActiveLock, error) {
	now := l.clk.Now().UnixNano()
	held, err := l.find(ctx, token, now)
	if err != nil {
		return ActiveLock{}, err
	}
	// A refresh by anyone but the holder would let a second client keep a lock
	// alive that the first one has stopped renewing.
	if held.Principal != int64(principal) {
		return ActiveLock{}, ErrLocked
	}

	timeout := clampTimeout(d)
	expires := now + timeout.Nanoseconds()
	if err := l.store.RefreshDavLock(ctx, token, expires, int64(timeout.Seconds()), now); err != nil {
		if errors.Is(err, state.ErrNoSuchLock) {
			return ActiveLock{}, ErrNotFound
		}
		return ActiveLock{}, err
	}
	return ActiveLock{
		Token: token, Path: held.Path, Owner: held.Owner, Depth: int(held.Depth),
		Principal: principal, ExpiresNs: expires, TimeoutS: int64(timeout.Seconds()),
	}, nil
}

// Unlock releases a lock. Only the principal holding it may.
func (l *Locks) Unlock(ctx context.Context, token string, principal core.UserID) error {
	now := l.clk.Now().UnixNano()
	held, err := l.find(ctx, token, now)
	if err != nil {
		return err
	}
	if held.Principal != int64(principal) {
		return ErrLocked
	}
	return l.store.DropDavLock(ctx, token)
}

// At returns the live locks covering one path, which is what lockdiscovery
// renders and what an If evaluation reads.
func (l *Locks) At(ctx context.Context, share int64, path string) ([]ActiveLock, error) {
	now := l.clk.Now().UnixNano()
	held, err := l.store.DavLocks(ctx, now)
	if err != nil {
		return nil, err
	}
	var out []ActiveLock
	for _, h := range held {
		if covers(h, share, path) {
			out = append(out, toActive(h))
		}
	}
	return out, nil
}

// Guard is the write check every mutating method runs.
//
// A locked resource may only be written by a request that submitted the token,
// which is what the If header carries. The distinction the status codes make:
// a request with no token at all is 423, and a request whose If header parsed
// but did not hold is 412.
func (l *Locks) Guard(ctx context.Context, share int64, path string, principal core.UserID, submitted []string) error {
	now := l.clk.Now().UnixNano()
	held, err := l.store.DavLocks(ctx, now)
	if err != nil {
		return err
	}
	for _, h := range held {
		if !covers(h, share, path) {
			continue
		}
		if !containsString(submitted, h.Token) && !containsString(submitted, "urn:uuid:"+h.Token) {
			return fmt.Errorf("%w: %s is held by another request", ErrLocked, h.Path)
		}
		// Holding the token is not enough on its own: the lock belongs to a
		// principal, and a token leaked to another user must not let them
		// write. The token is unguessable, so this is defence in depth.
		if h.Principal != int64(principal) {
			return fmt.Errorf("%w: the lock on %s belongs to another user", ErrLocked, h.Path)
		}
	}
	return nil
}

func (l *Locks) find(ctx context.Context, token string, nowNs int64) (state.DavLock, error) {
	held, err := l.store.DavLocks(ctx, nowNs)
	if err != nil {
		return state.DavLock{}, err
	}
	bare := strings.TrimPrefix(token, "urn:uuid:")
	for _, h := range held {
		if h.Token == bare {
			return h, nil
		}
	}
	return state.DavLock{}, ErrNotFound
}

// covers reports whether a lock applies to a path: the locked path itself, or
// anything under it when the lock is depth-infinity.
func covers(l state.DavLock, share int64, path string) bool {
	if l.Ident.Share != share {
		return false
	}
	if l.Path == path {
		return true
	}
	if l.Depth != DepthInfinity {
		return false
	}
	return isDescendant(path, l.Path)
}

// isUnder reports whether a lock sits at or beneath a path, which is what a
// depth-infinity request has to refuse over.
func isUnder(l state.DavLock, share int64, path string) bool {
	if l.Ident.Share != share {
		return false
	}
	if l.Path == path {
		return true
	}
	return isDescendant(l.Path, path)
}

// isDescendant reports whether child is strictly below parent.
//
// The separator check is what stops "/ab" from counting as a child of "/a": a
// bare prefix test would let a lock on one directory cover its sibling.
func isDescendant(child, parent string) bool {
	if parent == "" || parent == "/" {
		return child != parent
	}
	if !strings.HasPrefix(child, parent) {
		return false
	}
	return len(child) > len(parent) && child[len(parent)] == '/'
}

func toActive(h state.DavLock) ActiveLock {
	return ActiveLock{
		Token: h.Token, Path: h.Path, Owner: h.Owner, Depth: int(h.Depth),
		Principal: core.UserID(h.Principal), ExpiresNs: h.ExpiresNs, TimeoutS: h.TimeoutS,
	}
}

func containsString(set []string, s string) bool {
	for _, have := range set {
		if have == s {
			return true
		}
	}
	return false
}

func clampTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultLockTimeout
	}
	if d > maxLockTimeout {
		return maxLockTimeout
	}
	return d
}

// newLockToken mints an unguessable token. It is the only thing standing
// between a request and someone else's lock, so it comes from the CSPRNG.
func newLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a lock token: %w", err)
	}
	// UUID-shaped, because clients log these and a familiar shape helps.
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16])), nil
}

// TokenURN is the wire form of a lock token.
func TokenURN(token string) string { return "urn:uuid:" + token }

// ParseTimeout reads a Timeout header. An unparseable value is the client's
// preference being ignored, not an error: RFC 4918 lets the server choose.
func ParseTimeout(h string) time.Duration {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "Infinite") {
			return maxLockTimeout
		}
		if !strings.HasPrefix(strings.ToLower(part), "second-") {
			continue
		}
		n, err := strconv.ParseInt(part[len("Second-"):], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		// The multiplication is bounded before it happens: a huge Second-
		// value would otherwise overflow into a negative duration.
		if n > int64(maxLockTimeout/time.Second) {
			return maxLockTimeout
		}
		return time.Duration(n) * time.Second
	}
	return defaultLockTimeout
}

// lockDiscovery renders the lockdiscovery property.
func lockDiscovery(locks []ActiveLock) string {
	if len(locks) == 0 {
		return ""
	}
	out := ""
	for _, l := range locks {
		depth := "0"
		if l.Depth == DepthInfinity {
			depth = "infinity"
		}
		// The scope as it was taken, not a fixed word: a client that asked for
		// a shared lock and is told "exclusive" believes it holds something
		// nobody else can take.
		scope := "exclusive"
		if l.Shared {
			scope = "shared"
		}
		out += "<" + davPrefix + ":activelock>" +
			"<" + davPrefix + ":locktype><" + davPrefix + ":write/></" + davPrefix + ":locktype>" +
			"<" + davPrefix + ":lockscope><" + davPrefix + ":" + scope + "/></" + davPrefix + ":lockscope>" +
			"<" + davPrefix + ":depth>" + depth + "</" + davPrefix + ":depth>"
		if l.Owner != "" {
			// Re-serialised from stored text, never the client's markup.
			out += "<" + davPrefix + ":owner>" + EscapeText(l.Owner) + "</" + davPrefix + ":owner>"
		}
		out += "<" + davPrefix + ":timeout>Second-" + strconv.FormatInt(l.TimeoutS, 10) +
			"</" + davPrefix + ":timeout>" +
			"<" + davPrefix + ":locktoken><" + davPrefix + ":href>" +
			EscapeText(TokenURN(l.Token)) +
			"</" + davPrefix + ":href></" + davPrefix + ":locktoken>" +
			"<" + davPrefix + ":lockroot><" + davPrefix + ":href>" +
			EscapeHref(l.Path) +
			"</" + davPrefix + ":href></" + davPrefix + ":lockroot>" +
			"</" + davPrefix + ":activelock>"
	}
	return out
}

// ParseLockInfo reads a LOCK body: the owner text, the scope, and a refusal
// for anything this server does not offer.
func ParseLockInfo(body []byte, lim Limits) (owner string, shared bool, err error) {
	sc := newScanner(body, lim)
	lim = lim.withDefaults()

	var (
		stack     []Name
		sawRoot   bool
		sawWrite  bool
		sawShared bool
		capturing bool
		capDepth  int
		capText   textAccumulator
	)

	for {
		n, serr := sc.startNode()
		if serr != nil {
			return "", false, serr
		}
		if n == nil {
			break
		}
		switch n.kind {
		case nodeStart:
			if !sawRoot {
				if !n.name.IsDav("lockinfo") {
					return "", false, fmt.Errorf("%w: the root element is %s, want DAV:lockinfo",
						ErrBadXML, n.name)
				}
				sawRoot = true
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			switch {
			case n.name.IsDav("write"):
				sawWrite = true
			case n.name.IsDav("shared"):
				sawShared = true
			case n.name.IsDav("owner") && !capturing:
				if n.empty {
					continue
				}
				capturing, capDepth = true, len(stack)
				capText = textAccumulator{limit: lim.TextBytes}
				stack = append(stack, n.name)
				continue
			}
			if !n.empty {
				stack = append(stack, n.name)
			}
		case nodeEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if capturing && len(stack) == capDepth {
				owner = capText.value()
				capturing = false
			}
		case nodeText:
			if capturing {
				if aerr := capText.add(n.text); aerr != nil {
					return "", false, aerr
				}
			}
		}
	}

	if !sawRoot {
		return "", false, fmt.Errorf("%w: the document has no elements", ErrBadXML)
	}
	if !sawWrite {
		return "", false, fmt.Errorf("%w: only write locks are offered", ErrBadRequest)
	}
	return owner, sawShared, nil
}

// ParseDepth reads a Depth header. WebDAV's default differs per method, so the
// caller supplies it rather than this guessing.
func ParseDepth(h string, def int) (int, error) {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "":
		return def, nil
	case "0":
		return DepthZero, nil
	case "1":
		return 1, nil
	case "infinity":
		return DepthInfinity, nil
	}
	return 0, fmt.Errorf("%w: a Depth header of %q", ErrBadRequest, h)
}

// ErrNotFound is a resource, or a lock token, that is not there.
var ErrNotFound = errors.New("dav: not found")
