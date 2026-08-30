//go:build linux

package davlock_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/davlock"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The manager is driven against the real table rather than a stub of it.
//
// The conflict decision belongs to the store and happens inside the insert's
// own transaction, so a fake store would test the half that does not decide
// and would pass with the refusal removed.
func newLocks(t *testing.T) (*davlock.Locks, *state.DB) {
	t.Helper()
	ctx := context.Background()

	f, err := dbfile.Open(ctx, state.Spec(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})

	d := state.New(f)
	seedUser(t, d, 1)
	seedUser(t, d, 2)
	return davlock.New(d, clock.System()), d
}

// seedUser satisfies the foreign key the principal column carries.
//
// The name is derived from the id because it carries its own unique index, so
// a fixed one seeds the first account and fails the second.
func seedUser(t *testing.T, d *state.DB, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)
			 ON CONFLICT(id) DO NOTHING`, id, fmt.Sprintf("u%d", id))
		return err
	}); err != nil {
		t.Fatalf("seeding user %d: %v", id, err)
	}
}

func req(path string, principal int64) davlock.Request {
	return davlock.Request{
		Ident:     ident.Ident{Share: 1, Dev: 1, Ino: uint64(len(path)) + 1},
		Path:      path,
		Principal: principal,
		Owner:     "somebody",
		Depth:     state.LockDepthZero,
	}
}

// An exclusive lock keeps everyone else out, which is the whole of what it is
// for. A second one granted over the first is two clients writing the same
// file believing they are alone.
func TestASecondExclusiveLockIsRefused(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	if _, err := l.Take(ctx, req("/report.docx", 1)); err != nil {
		t.Fatalf("the first lock was refused: %v", err)
	}
	_, err := l.Take(ctx, req("/report.docx", 2))
	if !errors.Is(err, davlock.ErrLocked) {
		t.Fatalf("the second lock answered %v, want ErrLocked", err)
	}
}

// Two shared locks coexist. That is the only thing separating shared from
// exclusive, so it is what the scope has to actually do.
func TestTwoSharedLocksCoexist(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	first := req("/notes.txt", 1)
	first.Shared = true
	second := req("/notes.txt", 2)
	second.Shared = true

	a, err := l.Take(ctx, first)
	if err != nil {
		t.Fatalf("the first shared lock was refused: %v", err)
	}
	if !a.Shared {
		t.Error("a shared lock came back reporting itself exclusive")
	}
	if _, err := l.Take(ctx, second); err != nil {
		t.Errorf("the second shared lock was refused: %v", err)
	}
}

// A shared lock and an exclusive one do not coexist, in either order.
func TestSharedAndExclusiveConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, c := range []struct {
		name         string
		firstShared  bool
		secondShared bool
	}{
		{"exclusive then shared", false, true},
		{"shared then exclusive", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newLocks(t)

			first := req("/f", 1)
			first.Shared = c.firstShared
			if _, err := l.Take(ctx, first); err != nil {
				t.Fatalf("the first lock was refused: %v", err)
			}

			second := req("/f", 2)
			second.Shared = c.secondShared
			if _, err := l.Take(ctx, second); !errors.Is(err, davlock.ErrLocked) {
				t.Errorf("the second lock answered %v, want ErrLocked", err)
			}
		})
	}
}

// The guard is what every write goes through. A request that submitted no
// token is refused, and the same request carrying the token proceeds.
func TestTheGuardAdmitsOnlyTheSubmittedToken(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	held, err := l.Take(ctx, req("/held.txt", 1))
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if gerr := l.Guard(ctx, 1, "/held.txt", 1, nil); !errors.Is(gerr, davlock.ErrLocked) {
		t.Errorf("a write with no token answered %v, want ErrLocked", gerr)
	}
	if gerr := l.Guard(ctx, 1, "/held.txt", 1, []string{held.Token}); gerr != nil {
		t.Errorf("the holder's own write was refused: %v", gerr)
	}
	// Clients send the URN form, so both have to be accepted.
	if gerr := l.Guard(ctx, 1, "/held.txt", 1, []string{davlock.TokenURN(held.Token)}); gerr != nil {
		t.Errorf("the URN form of the token was refused: %v", gerr)
	}
	// An unrelated path is not covered.
	if gerr := l.Guard(ctx, 1, "/other.txt", 2, nil); gerr != nil {
		t.Errorf("an unlocked path was refused: %v", gerr)
	}
}

// A token that reached another account does not let them write. The token is
// unguessable, so this is the second barrier rather than the first.
func TestALeakedTokenDoesNotLetAnotherUserWrite(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	held, err := l.Take(ctx, req("/mine.txt", 1))
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if gerr := l.Guard(ctx, 1, "/mine.txt", 2, []string{held.Token}); !errors.Is(gerr, davlock.ErrLocked) {
		t.Errorf("another user writing with the token answered %v, want ErrLocked", gerr)
	}
}

// A depth-infinity lock covers what is under it, and stops at a sibling whose
// name merely begins with the same letters.
func TestDepthInfinityCoversDescendantsAndNotSiblings(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	deep := req("/dir", 1)
	deep.Depth = state.LockDepthInfinity
	if _, err := l.Take(ctx, deep); err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if gerr := l.Guard(ctx, 1, "/dir/child.txt", 1, nil); !errors.Is(gerr, davlock.ErrLocked) {
		t.Errorf("a descendant answered %v, want ErrLocked", gerr)
	}
	// "/dirty" is not under "/dir": a prefix test without the separator would
	// lock every sibling sharing the first three letters.
	if gerr := l.Guard(ctx, 1, "/dirty.txt", 1, nil); gerr != nil {
		t.Errorf("a sibling sharing the prefix was refused: %v", gerr)
	}
}

// A lock in one share does not reach into another.
func TestALockDoesNotCrossShares(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	if _, err := l.Take(ctx, req("/f.txt", 1)); err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if gerr := l.Guard(ctx, 2, "/f.txt", 1, nil); gerr != nil {
		t.Errorf("the same path in another share was refused: %v", gerr)
	}
}

// Only the holder may release or refresh. Otherwise a second client can drop
// a lock the first is relying on, or keep alive one it stopped renewing.
func TestOnlyTheHolderMayRefreshOrRelease(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	held, err := l.Take(ctx, req("/f.txt", 1))
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if _, rerr := l.Refresh(ctx, held.Token, 2, time.Minute); !errors.Is(rerr, davlock.ErrLocked) {
		t.Errorf("a refresh by another user answered %v, want ErrLocked", rerr)
	}
	if rerr := l.Release(ctx, held.Token, 2); !errors.Is(rerr, davlock.ErrLocked) {
		t.Errorf("a release by another user answered %v, want ErrLocked", rerr)
	}
	if rerr := l.Release(ctx, held.Token, 1); rerr != nil {
		t.Errorf("the holder's own release failed: %v", rerr)
	}
	// Released, so the path is writable again.
	if gerr := l.Guard(ctx, 1, "/f.txt", 2, nil); gerr != nil {
		t.Errorf("the path stayed locked after release: %v", gerr)
	}
}

// An unknown token is reported as unknown rather than as a permission
// failure, since the two send the client to different places.
func TestAnUnknownTokenIsNotFound(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	if _, err := l.Refresh(ctx, "nothing", 1, time.Minute); !errors.Is(err, davlock.ErrNoSuchLock) {
		t.Errorf("refreshing an unknown token answered %v, want ErrNoSuchLock", err)
	}
	if err := l.Release(ctx, "nothing", 1); !errors.Is(err, davlock.ErrNoSuchLock) {
		t.Errorf("releasing an unknown token answered %v, want ErrNoSuchLock", err)
	}
}

// The lease is clamped at both ends: an absent or absurd request does not
// become an unbounded row a client can pin for free.
func TestTheLeaseIsClamped(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	zero, err := l.Take(ctx, req("/a.txt", 1))
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if want := int64(davlock.DefaultTimeout.Seconds()); zero.TimeoutS != want {
		t.Errorf("an unspecified timeout became %ds, want %ds", zero.TimeoutS, want)
	}

	huge := req("/b.txt", 1)
	huge.Timeout = 500 * time.Hour
	got, err := l.Take(ctx, huge)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if want := int64(davlock.MaxTimeout.Seconds()); got.TimeoutS != want {
		t.Errorf("a 500-hour request became %ds, want %ds", got.TimeoutS, want)
	}
}

// At is what lockdiscovery renders, so it reports the lock covering a path
// including through a depth-infinity ancestor.
func TestAtReportsCoveringLocks(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	deep := req("/dir", 1)
	deep.Depth = state.LockDepthInfinity
	if _, err := l.Take(ctx, deep); err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	at, err := l.At(ctx, 1, "/dir/child.txt")
	if err != nil {
		t.Fatalf("reading the covering locks: %v", err)
	}
	if len(at) != 1 {
		t.Fatalf("a covered path reported %d locks, want 1", len(at))
	}
	if at[0].Path != "/dir" {
		t.Errorf("the covering lock is on %q, want /dir", at[0].Path)
	}

	none, err := l.At(ctx, 1, "/elsewhere.txt")
	if err != nil {
		t.Fatalf("reading the covering locks: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an uncovered path reported %d locks, want 0", len(none))
	}
}

// Two tokens are never the same. They are the only thing between a request
// and somebody else's lock.
func TestTokensAreUnique(t *testing.T) {
	t.Parallel()
	l, _ := newLocks(t)
	ctx := context.Background()

	seen := make(map[string]bool)
	for i := range 32 {
		got, err := l.Take(ctx, req("/f"+string(rune('a'+i)), 1))
		if err != nil {
			t.Fatalf("taking a lock: %v", err)
		}
		if seen[got.Token] {
			t.Fatalf("token %q was minted twice", got.Token)
		}
		seen[got.Token] = true
	}
}
