package ws

import (
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// The debounce is the load-bearing coalescing: a recursive copy produces
// thousands of events for one directory and the tab needs one. Two events for
// the same path within the window collapse into one pending entry, and the
// flush releases each path at most once.
func TestDebounceCoalescesPerPath(t *testing.T) {
	mut := &moveClock{t: time.Unix(0, 1_700_000_000_000_000_000)}
	c := &conn{pending: map[string]time.Time{}, hub: &Hub{clk: mut}}
	ev := watch.InvalEvent{Share: 1, Dir: "a"}

	c.invalidate(ev)
	c.invalidate(ev) // same path again, still inside the window
	if len(c.pending) != 1 {
		t.Fatalf("two events for one path = %d pending, want 1", len(c.pending))
	}

	// Still inside the window: nothing is due yet.
	if got := c.due(); len(got) != 0 {
		t.Fatalf("due before the window = %v, want empty", got)
	}
	if len(c.pending) != 1 {
		t.Fatalf("an unexpired flush emptied the pending set: %d", len(c.pending))
	}

	// Past the window: the path is released.
	mut.t = mut.t.Add(debounce + time.Millisecond)
	if got := c.due(); len(got) != 1 {
		t.Fatalf("due after the window = %v, want the one path", got)
	}
	if len(c.pending) != 0 {
		t.Fatalf("flush after the window left %d pending, want 0", len(c.pending))
	}

	// A different path is a different bucket.
	c.invalidate(watch.InvalEvent{Share: 1, Dir: "b"})
	c.invalidate(watch.InvalEvent{Share: 2, Dir: "a"})
	if len(c.pending) != 2 {
		t.Fatalf("two distinct (share, dir) pairs = %d pending, want 2", len(c.pending))
	}
}

type moveClock struct{ t time.Time }

func (m *moveClock) Now() time.Time                  { return m.t }
func (m *moveClock) Since(t time.Time) time.Duration { return m.t.Sub(t) }
func (m *moveClock) Nanos() int64                    { return m.t.UnixNano() }
