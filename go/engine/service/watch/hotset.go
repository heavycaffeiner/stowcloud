package watch

import (
	"container/list"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// key identifies a watched directory relative to its share. Host paths never
// appear here; only the transport requires one.
type key struct {
	share vfs.ShareID
	dir   string
}

// hotSet tracks which directories presently hold a kernel watch. It has two
// halves, and only one behaves as an LRU.
//
// The recent half is bounded and evicts, which is the portion that resembles
// the whole feature. The pinned half is reference counted, never evicted
// automatically, and driven externally: subscribing pins the directory being
// watched and unsubscribing releases it.
//
// Implementing only the LRU half fails silently. Under load an LRU evicts
// exactly the directory a user is currently viewing, so invalidations stop
// reaching the folder in front of them while everything else continues
// working.
type hotSet struct {
	cap int

	// sticky associates each pinned key with how many subscribers hold it.
	sticky map[key]uint32

	// order lists keys by recency with the newest first, and recent indexes into
	// it. Its length does not enforce the cap: eviction runs solely through
	// evictFor, which also unregisters the kernel watch. A self-evicting
	// structure would strand watches that nothing ever removes.
	order  *list.List
	recent map[key]*list.Element

	// registered holds every key with a live watch, pinned or recent. The cap
	// applies to this set.
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

// stickyCount is how many keys a subscriber holds pinned.
func (h *hotSet) stickyCount() int { return len(h.sticky) }

func (h *hotSet) isRegistered(k key) bool {
	_, ok := h.registered[k]
	return ok
}

func (h *hotSet) markRegistered(k key)   { h.registered[k] = struct{}{} }
func (h *hotSet) markUnregistered(k key) { delete(h.registered, k) }

// registeredKeys returns a snapshot for the periodic rescan, which revisits
// only already-tracked directories and never walks a tree.
func (h *hotSet) registeredKeys() []key {
	out := make([]key, 0, len(h.registered))
	for k := range h.registered {
		out = append(out, k)
	}
	return out
}

// addSticky pins k, lifting it out of the recent half. The return value says
// whether the caller must now register a kernel watch.
func (h *hotSet) addSticky(k key) bool {
	h.dropRecent(k)
	h.sticky[k]++
	return !h.isRegistered(k)
}

// removeSticky drops one subscriber. Reaching zero returns the key to the
// recent half, still registered but now evictable, rather than tearing it down
// while another reader might be a moment behind.
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

// touch refreshes k's recency and reports whether the caller must register a
// kernel watch. Pinned keys are untouched: they already sit in the half that
// never evicts, and relocating one would return it to the evictable set.
func (h *hotSet) touch(k key) bool {
	if _, pinned := h.sticky[k]; pinned {
		return false
	}
	isNew := !h.isRegistered(k)
	h.pushRecent(k)
	return isNew
}

// evictFor selects keys to unregister, oldest first, making room for one more
// registration under the cap. It does not remove the kernel watch itself: the
// caller does that and then calls markUnregistered, so a failed removal cannot
// leave the two views inconsistent.
func (h *hotSet) evictFor(k key) []key {
	if h.isRegistered(k) {
		return nil
	}
	var out []key
	for len(h.registered)+1 > h.cap {
		back := h.order.Back()
		if back == nil {
			// Only pinned keys remain. Slightly overshooting the cap is
			// preferable to discarding a watch a user is actively reading.
			break
		}
		evicted, ok := back.Value.(key)
		if !ok {
			h.order.Remove(back)
			continue
		}
		h.dropRecent(evicted)
		out = append(out, evicted)
		// Unregistering is the caller's job; counting it here terminates the
		// loop.
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
