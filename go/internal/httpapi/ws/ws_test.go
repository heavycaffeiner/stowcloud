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
	c := &conn{
		pending: map[string]time.Time{},
		// Only a path this connection asked for is recorded, so the
		// subscriptions are what turn an event into a pending summary.
		subs: map[string]subscription{
			"photos/a": {share: 1, dir: "a"},
			"photos/b": {share: 1, dir: "b"},
			"video/a":  {share: 2, dir: "a"},
		},
		hub: &Hub{clk: mut},
	}
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

// A client is never told about a path it did not ask for. An event for a
// directory nobody is looking at costs nothing and reaches nobody.
func TestAnUnsubscribedPathProducesNothing(t *testing.T) {
	mut := &moveClock{t: time.Unix(0, 1_700_000_000_000_000_000)}
	c := &conn{
		pending: map[string]time.Time{},
		subs:    map[string]subscription{"photos/a": {share: 1, dir: "a"}},
		hub:     &Hub{clk: mut},
	}

	c.invalidate(watch.InvalEvent{Share: 1, Dir: "somewhere-else"})
	c.invalidate(watch.InvalEvent{Share: 9, Dir: "a"})
	if len(c.pending) != 0 {
		t.Fatalf("an unsubscribed path produced %d pending", len(c.pending))
	}

	// And the one that was asked for still arrives.
	c.invalidate(watch.InvalEvent{Share: 1, Dir: "a"})
	if len(c.pending) != 1 {
		t.Fatalf("the subscribed path produced %d pending, want 1", len(c.pending))
	}
}

// A lost-events report has no directory of its own, so it reaches everything
// this connection watches in that share: what was missed is exactly what is
// unknown, and telling only some of the subscriptions would leave the rest
// showing a stale view forever.
func TestALostEventReportReachesEverySubscriptionInTheShare(t *testing.T) {
	mut := &moveClock{t: time.Unix(0, 1_700_000_000_000_000_000)}
	c := &conn{
		pending: map[string]time.Time{},
		subs: map[string]subscription{
			"photos/a":   {share: 1, dir: "a"},
			"photos/b/c": {share: 1, dir: "b/c"},
			"video/a":    {share: 2, dir: "a"},
		},
		hub: &Hub{clk: mut},
	}

	c.invalidate(watch.InvalEvent{Share: 1, All: true})
	if len(c.pending) != 2 {
		t.Fatalf("a lost-event report reached %d subscriptions, want both in that share", len(c.pending))
	}
	if _, ok := c.pending["video/a"]; ok {
		t.Fatal("it reached a subscription in another share")
	}
}

type moveClock struct{ t time.Time }

func (m *moveClock) Now() time.Time                  { return m.t }
func (m *moveClock) Since(t time.Time) time.Duration { return m.t.Sub(t) }
func (m *moveClock) Nanos() int64                    { return m.t.UnixNano() }
