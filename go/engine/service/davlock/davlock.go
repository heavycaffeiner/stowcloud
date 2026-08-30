//go:build linux

// Package davlock is the WebDAV lock table: taking a lock, refreshing it,
// releasing it, and the guard every mutating method runs before it writes.
//
// The durable half lives in store/state, which admits a lock only if nothing
// live conflicts, inside one write transaction. What is here is the part above
// that: minting a token, clamping a lease, deciding who may refresh or release,
// and turning a held lock into the shape a LOCK response and the lockdiscovery
// property render.
//
// Locks are durable because a client holds one across a save. An office suite
// that takes a lock, writes, and finds the server forgot it mid-save reports a
// conflict against a file nobody else touched.
package davlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The refusals this package makes.
var (
	// ErrLocked is a resource held by a token the request did not submit, or
	// an operation attempted by somebody other than the holder.
	ErrLocked = errors.New("the resource is locked")
	// ErrNoSuchLock is a token naming no live lock.
	ErrNoSuchLock = errors.New("no such lock")
)

// Lease bounds. A client asks and the server decides: an unbounded lease on a
// durable table is a row a client can pin for free.
const (
	// DefaultTimeout is what a request without a usable Timeout header gets.
	DefaultTimeout = 5 * time.Minute
	// MaxTimeout is the ceiling, whatever was asked for.
	MaxTimeout = 60 * time.Minute
)

// tokenURN is the scheme a lock token is written with on the wire.
const tokenURN = "urn:uuid:" //nolint:gosec // G101 reads this as a credential: it is a URI scheme, and the token itself is minted per lock and never in this tree.

// Store is the durable half. An interface so a test can drive the manager
// without a database, not to admit a second implementation.
type Store interface {
	DavLocks(ctx context.Context, nowNs int64) ([]state.DavLock, error)
	AdmitDavLock(ctx context.Context, l state.DavLock, nowNs int64) error
	RefreshDavLock(ctx context.Context, token string, expiresNs, timeoutS, nowNs int64) error
	DropDavLock(ctx context.Context, token string) error
}

// Locks is the lock table.
type Locks struct {
	store Store
	clk   clock.Clock
}

// New builds the manager.
func New(store Store, clk clock.Clock) *Locks {
	return &Locks{store: store, clk: clk}
}

// Active is one live lock, as the LOCK response and lockdiscovery render it.
type Active struct {
	Token     string
	Path      string
	Owner     string
	Depth     int64
	Principal int64
	ExpiresNs int64
	TimeoutS  int64
	// Shared is the scope. A client told "exclusive" for a shared lock would
	// believe it holds something nobody else can take, so this is rendered
	// rather than assumed.
	Shared bool
}

// Request is what a LOCK method asks for.
type Request struct {
	// Ident is the file's identity, which moves with it across a rename.
	Ident ident.Ident
	// Path is the virtual path, which is what a depth-infinity lock's
	// coverage is computed from. The identity names the file; the path is
	// what the client asked about.
	Path      string
	Principal int64
	Owner     string
	// Depth is state.LockDepthZero or state.LockDepthInfinity.
	Depth   int64
	Timeout time.Duration
	// Shared asks for a shared lock rather than an exclusive one.
	Shared bool
}

// Take creates a lock, exclusive or shared.
//
// The conflict decision belongs to the store, which makes it inside the same
// transaction as the insert. Deciding here would mean scanning and then
// inserting as two statements, and two conflicting LOCKs arriving together
// would each scan before either inserted, so both would be granted.
func (l *Locks) Take(ctx context.Context, req Request) (Active, error) {
	token, err := newToken()
	if err != nil {
		return Active{}, err
	}

	now := l.clk.Now().UnixNano()
	timeout := clampTimeout(req.Timeout)
	scope := state.LockExclusive
	if req.Shared {
		scope = state.LockShared
	}

	row := state.DavLock{
		Token:     token,
		Ident:     req.Ident,
		Path:      req.Path,
		Principal: req.Principal,
		Owner:     req.Owner,
		Depth:     req.Depth,
		Scope:     scope,
		ExpiresNs: now + timeout.Nanoseconds(),
		TimeoutS:  int64(timeout.Seconds()),
	}
	if aerr := l.store.AdmitDavLock(ctx, row, now); aerr != nil {
		if errors.Is(aerr, state.ErrLockConflict) {
			return Active{}, fmt.Errorf("%w: %s is held", ErrLocked, req.Path)
		}
		return Active{}, aerr
	}
	return activeOf(row), nil
}

// Refresh extends a lock its holder already has.
//
// Only the holder may. A refresh by anyone else would keep alive a lock the
// original holder has stopped renewing, which is how a lease that exists to
// expire stops expiring.
func (l *Locks) Refresh(ctx context.Context, token string, principal int64, d time.Duration) (Active, error) {
	now := l.clk.Now().UnixNano()
	held, err := l.find(ctx, token, now)
	if err != nil {
		return Active{}, err
	}
	if held.Principal != principal {
		return Active{}, ErrLocked
	}

	timeout := clampTimeout(d)
	expires := now + timeout.Nanoseconds()
	if rerr := l.store.RefreshDavLock(ctx, held.Token, expires, int64(timeout.Seconds()), now); rerr != nil {
		if errors.Is(rerr, state.ErrNoSuchLock) {
			return Active{}, ErrNoSuchLock
		}
		return Active{}, rerr
	}

	held.ExpiresNs = expires
	held.TimeoutS = int64(timeout.Seconds())
	return activeOf(held), nil
}

// Release drops a lock. Only the principal holding it may.
func (l *Locks) Release(ctx context.Context, token string, principal int64) error {
	held, err := l.find(ctx, token, l.clk.Now().UnixNano())
	if err != nil {
		return err
	}
	if held.Principal != principal {
		return ErrLocked
	}
	return l.store.DropDavLock(ctx, held.Token)
}

// At returns the live locks covering one path, which is what lockdiscovery
// renders and what an If header evaluation reads.
func (l *Locks) At(ctx context.Context, share uint32, path string) ([]Active, error) {
	held, err := l.store.DavLocks(ctx, l.clk.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	var out []Active
	for _, h := range held {
		if covers(h, share, path) {
			out = append(out, activeOf(h))
		}
	}
	return out, nil
}

// Guard is the check every mutating method runs before it writes.
//
// A covered resource may only be written by a request that submitted the
// token, which is what the If header carries. Holding the token is not enough
// on its own: the lock belongs to a principal, so a token that leaked to
// another account does not let them write. The token is unguessable, which
// makes the second check defence in depth rather than the barrier.
func (l *Locks) Guard(ctx context.Context, share uint32, path string, principal int64, submitted []string) error {
	held, err := l.store.DavLocks(ctx, l.clk.Now().UnixNano())
	if err != nil {
		return err
	}
	for _, h := range held {
		if !covers(h, share, path) {
			continue
		}
		if !submittedHas(submitted, h.Token) {
			return fmt.Errorf("%w: %s is held by another request", ErrLocked, h.Path)
		}
		if h.Principal != principal {
			return fmt.Errorf("%w: the lock on %s belongs to another user", ErrLocked, h.Path)
		}
	}
	return nil
}

// find resolves a token, in either its bare or its URN form.
func (l *Locks) find(ctx context.Context, token string, nowNs int64) (state.DavLock, error) {
	held, err := l.store.DavLocks(ctx, nowNs)
	if err != nil {
		return state.DavLock{}, err
	}
	bare := strings.TrimPrefix(token, tokenURN)
	for _, h := range held {
		if h.Token == bare {
			return h, nil
		}
	}
	return state.DavLock{}, ErrNoSuchLock
}

// covers reports whether a lock applies to a path: the locked path itself, or
// anything beneath it when the lock is depth-infinity.
func covers(l state.DavLock, share uint32, path string) bool {
	if uint32(l.Ident.Share) != share {
		return false
	}
	if l.Path == path {
		return true
	}
	if l.Depth != state.LockDepthInfinity {
		return false
	}
	return isDescendant(path, l.Path)
}

// isDescendant reports whether child is strictly below parent.
//
// The separator check is what stops "/ab" counting as a child of "/a". Without
// it a lock on one directory would cover every sibling whose name begins with
// the same letters.
func isDescendant(child, parent string) bool {
	if parent == "" || parent == "/" {
		return child != parent
	}
	trimmed := strings.TrimSuffix(parent, "/")
	return strings.HasPrefix(child, trimmed+"/")
}

// submittedHas reports whether the request carried this token, in either form.
func submittedHas(submitted []string, token string) bool {
	for _, s := range submitted {
		if s == token || s == tokenURN+token {
			return true
		}
	}
	return false
}

// TokenURN is the wire form of a lock token.
func TokenURN(token string) string { return tokenURN + token }

func activeOf(h state.DavLock) Active {
	return Active{
		Token:     h.Token,
		Path:      h.Path,
		Owner:     h.Owner,
		Depth:     h.Depth,
		Principal: h.Principal,
		ExpiresNs: h.ExpiresNs,
		TimeoutS:  h.TimeoutS,
		Shared:    h.Scope == state.LockShared,
	}
}

func clampTimeout(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultTimeout
	case d > MaxTimeout:
		return MaxTimeout
	default:
		return d
	}
}

// newToken mints an unguessable token. It is the only thing standing between a
// request and somebody else's lock, so it comes from the CSPRNG.
func newToken() (string, error) {
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
