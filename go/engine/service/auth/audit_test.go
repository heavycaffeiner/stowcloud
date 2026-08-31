// Linux only, because the audit log it pages is stored by a Linux-only engine.
//go:build linux

package auth_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// Paging the log while it is being appended to neither skips nor repeats a
// row. The cursor is a row id rather than an offset, so a row arriving between
// two pages lands ahead of the cursor instead of shifting the window under it.
//
// An offset-based reader is the failure this pins: with rows arriving at the
// head, page two of an offset reader re-serves what page one already showed.
func TestTheCursorPagesAConcurrentlyAppendedLog(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	actor := f.account(t, "alice")

	const seeded = 40
	for i := range seeded {
		f.svc.Record(ctx, actor, "login", fmt.Sprintf("seed-%02d", i), "192.0.2.1", "client", true)
	}

	// One writer appends throughout the read, so every page after the first is
	// taken against a log that has grown since the page before it.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	task.Go(ctx, "auth: audit log writer during a paged read", func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.svc.Record(ctx, actor, "login", fmt.Sprintf("live-%02d", i), "192.0.2.1", "client", true)
		}
	})

	seen := map[int64]bool{}
	var order []int64
	var cursor int64
	for range 6 {
		rows, next, err := f.svc.AuditPage(ctx, auth.AuditFilter{Limit: 7, Before: cursor})
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("AuditPage: %v", err)
		}
		for _, r := range rows {
			if seen[r.RowID] {
				close(stop)
				wg.Wait()
				t.Fatalf("row %d was served twice across pages", r.RowID)
			}
			seen[r.RowID] = true
			order = append(order, r.RowID)
		}
		if next == nil {
			break
		}
		cursor = *next
	}
	close(stop)
	wg.Wait()

	// Descending by row id with no gap in what was seeded. A skipped row shows
	// up as a seeded target missing from the walk, which is the half an
	// offset-based cursor gets wrong in the other direction.
	for i := 1; i < len(order); i++ {
		if order[i] >= order[i-1] {
			t.Fatalf("the walk is not descending at %d: %d then %d", i, order[i-1], order[i])
		}
	}
	if len(order) < 40 {
		t.Fatalf("six pages of seven returned %d rows", len(order))
	}
}
