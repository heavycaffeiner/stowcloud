package watch

import (
	"container/list"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// key is one watched directory, in share-relative terms. Never a host path: the
// transport is the only thing that needs one of those.
type key struct {
	share vfs.ShareID
	dir   string
}

// hotSet is which directories currently carry a kernel watch, and it has two
// halves. Only one of them is an LRU.
//
// The recent half is capped and evicts, which is the part that sounds like the
// whole feature. The sticky half is refcounted, never auto-evicted, and fed
// from outside: a subscription pins the directory it is watching and releases
// it on unsubscribe.
//
// Building only the LRU half is the failure mode, and it is silent. The
// directory a user is currently looking at is exactly the one an LRU evicts
// under load, so invalidations stop arriving for the folder in front of them
// while everything else keeps working.
type hotSet struct {
	cap int

	// sticky maps a pinned key to its subscriber count.
	sticky map[key]uint32

	// order is the recency list, most recent at the front, and recent indexes
	// into it. Its own length is not the cap: eviction happens only through
	// evictFor, which also unregisters the kernel watch, and a structure that
	// self-evicts would leave a watch nothing ever removes.
	order  *list.List
	recent map[key]*list.Element

	// registered is every key with a live watch, sticky or recent. This is the
	// set the cap applies to.
	registered map[key]struct{}
}

func newHotSet(capacity int) *hotSet {
	capacity = max(capacity, 1)
	return &hotSet{
		cap:        capacity,
		sticky:     make(map[key]uint32),
		order:      list.New(),
		recent:     make(map[key]*list.Element),
		registered: make(map[key]struct{}),
	}
}

func (h *hotSet) registeredCount() int { return len(h.registered) }

func (h *hotSet) isRegistered(k key) bool {
	_, ok := h.registered[k]
	return ok
}

func (h *hotSet) markRegistered(k key)   { h.registered[k] = struct{}{} }
func (h *hotSet) markUnregistered(k key) { delete(h.registered, k) }

// registeredKeys is a snapshot for the periodic rescan, which only revisits
// directories already tracked and never walks a tree.
func (h *hotSet) registeredKeys() []key {
	out := make([]key, 0, len(h.registered))
	for k := range h.registered {
		out = append(out, k)
	}
	return out
}

// addSticky pins k, promoting it out of the recent half. It reports whether the
// caller now has to register a kernel watch.
func (h *hotSet) addSticky(k key) bool {
	h.dropRecent(k)
	h.sticky[k]++
	return !h.isRegistered(k)
}

// removeSticky releases one subscriber. At zero the key falls back into the
// recent half, still registered and now evictable, rather than being torn down
// while a second reader may be a moment behind.
func (h *hotSet) removeSticky(k key) {
	n, ok := h.sticky[k]
	if !ok {
		return
	}
	n--
	if n > 0 {
		h.sticky[k] = n
		return
	}
	delete(h.sticky, k)
	if h.isRegistered(k) {
		h.pushRecent(k)
	}
}

// touch bumps k's recency and reports whether the caller has to register a
// kernel watch for it. A pinned key is left alone: it is already in the half
// that never evicts, and moving it would put it back among the evictable.
func (h *hotSet) touch(k key) bool {
	if _, pinned := h.sticky[k]; pinned {
		return false
	}
	isNew := !h.isRegistered(k)
	h.pushRecent(k)
	return isNew
}

// evictFor names the keys to unregister, oldest first, so that one more
// registration fits under the cap. It does not remove the kernel watch: the
// caller does that first and then calls markUnregistered, so a failed removal
// cannot leave the two disagreeing.
func (h *hotSet) evictFor(k key) []key {
	if h.isRegistered(k) {
		return nil
	}
	var out []key
	for len(h.registered)+1 > h.cap {
		back := h.order.Back()
		if back == nil {
			// Everything left is pinned. Exceeding the cap slightly beats
			// dropping the watch a user is actively reading.
			break
		}
		evicted, ok := back.Value.(key)
		if !ok {
			h.order.Remove(back)
			continue
		}
		h.dropRecent(evicted)
		out = append(out, evicted)
		// The caller marks it unregistered; count it here so the loop ends.
		delete(h.registered, evicted)
	}
	return out
}

func (h *hotSet) pushRecent(k key) {
	if e, ok := h.recent[k]; ok {
		h.order.MoveToFront(e)
		return
	}
	h.recent[k] = h.order.PushFront(k)
}

func (h *hotSet) dropRecent(k key) {
	if e, ok := h.recent[k]; ok {
		h.order.Remove(e)
		delete(h.recent, k)
	}
}
