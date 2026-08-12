//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// The per-user quota ledger is enforced through a reserve-then-commit seam: a
// write reserves before it starts and commits or releases when it ends, so two
// concurrent writes cannot both pass a check against the same headroom. That
// is the property this test proves: the reserve is one guarded UPDATE, never a
// read-then-write.

// addUser inserts a row with a quota cap and a zero ledger.
func addUser(t *testing.T, c *Core, id int64, quota, usage int64) {
	t.Helper()
	err := c.state.Write(context.Background(), func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(context.Background(),
			`INSERT INTO user(id, name, pw_hash, quota_bytes, usage_bytes, created_ns)
			 VALUES (?, ?, 'x', ?, ?, 1)`, id, "u", quota, usage)
		return ierr
	})
	if err != nil {
		t.Fatalf("addUser: %v", err)
	}
}

func newSQLQuotaFromCore(t *testing.T, c *Core) QuotaSink {
	t.Helper()
	return NewSQLQuota(c.state)
}

// TestTwoConcurrentWritesCannotBothPassTheSameHeadroom is the "Done when"
// proof. A cap of 100, each write wants 80: only one can reserve, because the
// second's guarded update finds usage+80 > 100.
func TestTwoConcurrentWritesCannotBothPassTheSameHeadroom(t *testing.T) {
	c, _, _ := testCore(t)
	addUser(t, c, 1, 100, 0)
	sink := newSQLQuotaFromCore(t, c)
	ctx := context.Background()

	const additional = uint64(80)
	var passed, refused sync.Mutex
	var pass, fail int
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		task.Go(ctx, "core test: reserve", func() {
			defer wg.Done()
			err := sink.Reserve(ctx, UserID(1), additional)
			if err == nil {
				passed.Lock()
				pass++
				passed.Unlock()
			} else if errors.Is(err, ErrQuotaExceeded) {
				refused.Lock()
				fail++
				refused.Unlock()
			} else {
				t.Errorf("Reserve = %v, want success or ErrQuotaExceeded", err)
			}
		})
	}
	wg.Wait()

	if pass != 1 || fail != 1 {
		t.Fatalf("two concurrent writes against headroom 100 for two 80s: pass=%d fail=%d, want 1 and 1",
			pass, fail)
	}
}

// TestReserveThenReleaseFreesTheHeadroom is the release side: a reservation
// that does not land is returned, so a later write can use the room.
func TestReserveThenReleaseFreesTheHeadroom(t *testing.T) {
	c, _, _ := testCore(t)
	addUser(t, c, 2, 100, 0)
	sink := newSQLQuotaFromCore(t, c)
	ctx := context.Background()

	if err := sink.Reserve(ctx, UserID(2), 90); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := sink.Reserve(ctx, UserID(2), 20); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("a second reserve over the cap = %v, want ErrQuotaExceeded", err)
	}
	if err := sink.Release(ctx, UserID(2), 90); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := sink.Reserve(ctx, UserID(2), 20); err != nil {
		t.Fatalf("after release a 20 fits: %v", err)
	}
}

// TestReleaseNeverGoesBelowZero clamps the ledger so a freed delete of a size
// larger than the tracked usage cannot go negative.
func TestReleaseNeverGoesBelowZero(t *testing.T) {
	c, _, _ := testCore(t)
	addUser(t, c, 3, 100, 10)
	sink := newSQLQuotaFromCore(t, c)
	ctx := context.Background()

	if err := sink.Release(ctx, UserID(3), 500); err != nil {
		t.Fatalf("Release: %v", err)
	}
	var usage int64
	if err := c.state.SQL().QueryRowContext(ctx,
		`SELECT usage_bytes FROM user WHERE id = 3`).Scan(&usage); err != nil {
		t.Fatalf("reading usage: %v", err)
	}
	if usage != 0 {
		t.Fatalf("usage after an oversized release = %d, want 0", usage)
	}
}
