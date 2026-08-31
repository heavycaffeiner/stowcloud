//go:build linux

package watch

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"golang.org/x/sys/unix"
)

// watchMask enumerates the events a directory watch requests from the kernel:
// entries created, removed, written to, or altered in metadata. Together those
// cover every way an already-served listing can go out of date.
const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_MOVE_SELF |
	unix.IN_DELETE_SELF | unix.IN_ATTRIB

// eventBufBytes sizes the buffer for a batch of inotify records. Names cap at
// 255 bytes, so a busy tree yields many events per syscall.
const eventBufBytes = 16 << 10

// flushInterval sets how frequently the debounce loop scans for entries whose
// window has elapsed.
const flushInterval = 50 * time.Millisecond

// inotifyEventHeader is the fixed-size prefix of struct inotify_event: the wd,
// mask, cookie and len fields, each 32 bits, trailed by len bytes holding a
// NUL-padded name.
const inotifyEventHeader = 16

// Field offsets inside one record, so the parser reads them by name.
const (
	offWd      = 0
	offMask    = 4
	offNameLen = 12
)

// share holds a registered share root, expressed the only way the transport can
// consume it.
type share struct {
	host string
	// rescan marks filesystems whose modifications inotify never reports, which
	// covers every network and FUSE mount. On those the periodic sweep is the
	// sole mechanism that detects a change made by another host.
	rescan bool
}

// Watcher detects changes using a single kernel watch descriptor, a single
// reader, a single debounce loop and a single rescan loop.
type Watcher struct {
	cfg  Config
	clk  clock.Clock
	sink chan<- InvalEvent

	inotify *os.File

	mu      sync.Mutex
	hot     *hotSet
	wdToKey map[int]key
	keyToWd map[key]int
	shares  map[vfs.ShareID]share
	pending map[key]time.Time

	overflow atomic.Bool
	degraded atomic.Int64

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Start launches the watcher. Each debounced directory produces one event on
// the sink, which the caller owns.
func Start(ctx context.Context, cfg Config, clk clock.Clock, sink chan<- InvalEvent) (*Watcher, error) {
	cfg = cfg.withDefaults()
	if clk == nil {
		clk = clock.System()
	}

	// Opened non-blocking so the descriptor is managed by the runtime's poller.
	// Close then wakes the reader rather than stranding a thread inside read.
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("start the watcher: %w", err)
	}

	w := &Watcher{
		cfg:     cfg,
		clk:     clk,
		sink:    sink,
		inotify: os.NewFile(uintptr(fd), "inotify"),
		hot:     newHotSet(cfg.HotSetMax),
		wdToKey: make(map[int]key),
		keyToWd: make(map[key]int),
		shares:  make(map[vfs.ShareID]share),
		pending: make(map[key]time.Time),
		stop:    make(chan struct{}),
	}

	w.wg.Add(3)
	task.Go(ctx, "watch: read events", func() { defer w.wg.Done(); w.readLoop() })
	task.Go(ctx, "watch: flush debounced", func() { defer w.wg.Done(); w.flushLoop() })
	task.Go(ctx, "watch: rescan", func() { defer w.wg.Done(); w.rescanLoop() })
	return w, nil
}

// AddShare records a share's location on the host. Nothing else in the watcher
// deals in host paths; this is the single entry point, because the kernel's
// watch API requires one.
//
// rescan flags filesystems whose changes inotify cannot observe, meaning every
// network and FUSE mount.
func (w *Watcher) AddShare(id vfs.ShareID, hostRoot string, rescan bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shares[id] = share{host: hostRoot, rescan: rescan}
}

// Subscribe pins a directory together with every ancestor, returning only once
// the watches exist. A caller may then read the directory with no risk of
// missing a change that arrives in the interval.
//
// This supplies the pinned portion of the hot set. Were the hot set purely an
// LRU, it would quietly fail on precisely the directory a user is viewing.
func (w *Watcher) Subscribe(id vfs.ShareID, p vfs.SafePath) {
	for _, k := range ancestorKeys(id, p) {
		w.mu.Lock()
		isNew := w.hot.addSticky(k)
		w.mu.Unlock()
		if isNew {
			w.register(k)
		}
	}
}

// Unsubscribe drops a single subscriber's pin covering a directory and its
// ancestors.
func (w *Watcher) Unsubscribe(id vfs.ShareID, p vfs.SafePath) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range ancestorKeys(id, p) {
		w.hot.removeSticky(k)
	}
}

// Touch notes a read of a directory, supplying the evictable portion of the hot
// set.
func (w *Watcher) Touch(id vfs.ShareID, p vfs.SafePath) {
	k := key{share: id, dir: p.String()}
	w.mu.Lock()
	isNew := w.hot.touch(k)
	w.mu.Unlock()
	if isNew {
		w.register(k)
	}
}

// Stats is what the health endpoint reports.
func (w *Watcher) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Stats{
		Registered: w.hot.registeredCount(),
		Pinned:     w.hot.stickyCount(),
		Degraded:   w.degraded.Load(),
		Shares:     len(w.shares),
	}
}

// Close halts all loops and gives up the watch descriptor.
func (w *Watcher) Close() error {
	var err error
	w.stopOnce.Do(func() {
		close(w.stop)
		err = w.inotify.Close()
		w.wg.Wait()
	})
	return err
}

// ancestorKeys yields the directory plus every level above it through the share
// root. Because a child's change alters the parent's listing, subscribing to a
// deep directory pins the entire chain used to render it.
func ancestorKeys(id vfs.ShareID, p vfs.SafePath) []key {
	comps := p.Components()
	out := make([]key, 0, len(comps)+1)
	out = append(out, key{share: id, dir: ""})
	cur := ""
	for _, c := range comps {
		if cur == "" {
			cur = c
		} else {
			cur += "/" + c
		}
		out = append(out, key{share: id, dir: cur})
	}
	return out
}

// register installs a kernel watch on k, first evicting the least recently used
// unpinned directories to hold the registered set within its cap.
//
// When the kernel declines a registration, that subtree falls back to lazy
// revalidation instead of the caller's read failing. The read still yields the
// correct answer; it simply receives no subsequent pushed update. This is a
// named, counted degradation rather than an error.
func (w *Watcher) register(k key) {
	host, ok := w.hostPath(k)
	if !ok {
		w.degraded.Add(1)
		return
	}

	w.mu.Lock()
	evicted := w.hot.evictFor(k)
	w.mu.Unlock()
	for _, e := range evicted {
		w.unregister(e)
	}

	wd, err := w.addWatch(host)
	if err != nil {
		w.degraded.Add(1)
		reason := "the kernel refused a watch registration"
		if errors.Is(err, unix.ENOSPC) {
			reason = "the per-user watch limit is reached, which a container cannot raise"
		}
		slog.Warn("a subtree fell back to lazy revalidation",
			slog.String("reason", reason),
			slog.String("dir", k.dir),
			slog.Uint64("share", uint64(k.share)),
			slog.Any("error", err))
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// Watching a path twice makes inotify return the descriptor it already
	// issued, so the reverse map keys on whatever the kernel returned.
	w.wdToKey[wd] = k
	w.keyToWd[k] = wd
	w.hot.markRegistered(k)
}

func (w *Watcher) unregister(k key) {
	w.mu.Lock()
	wd, ok := w.keyToWd[k]
	if ok {
		delete(w.keyToWd, k)
		delete(w.wdToKey, wd)
	}
	w.hot.markUnregistered(k)
	w.mu.Unlock()
	if !ok {
		return
	}
	if err := w.rmWatch(wd); err != nil {
		slog.Debug("removing a watch failed, which a deleted directory does on its own",
			slog.String("dir", k.dir), slog.Any("error", err))
	}
}

func (w *Watcher) hostPath(k key) (string, bool) {
	w.mu.Lock()
	s, ok := w.shares[k.share]
	w.mu.Unlock()
	if !ok {
		return "", false
	}
	if k.dir == "" {
		return s.host, true
	}
	return filepath.Join(s.host, filepath.FromSlash(k.dir)), true
}

// addWatch and rmWatch use SyscallConn instead of Fd. Fd would restore blocking
// mode and remove the descriptor from the poller, which is precisely what
// allows Close to wake the reader.
func (w *Watcher) addWatch(host string) (int, error) {
	rc, err := w.inotify.SyscallConn()
	if err != nil {
		return 0, err
	}
	var wd int
	var ierr error
	if cerr := rc.Control(func(fd uintptr) {
		wd, ierr = unix.InotifyAddWatch(int(fd), host, watchMask)
	}); cerr != nil {
		return 0, cerr
	}
	if ierr != nil {
		return 0, ierr
	}
	return wd, nil
}

func (w *Watcher) rmWatch(wd int) error {
	// Watch descriptors come back from the kernel signed and are passed in
	// unsigned, so this boundary is validated rather than blindly converted.
	id, err := num.Narrow[uint32](wd)
	if err != nil {
		return err
	}
	rc, err := w.inotify.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if cerr := rc.Control(func(fd uintptr) {
		_, ierr = unix.InotifyRmWatch(int(fd), id)
	}); cerr != nil {
		return cerr
	}
	return ierr
}

// readLoop decodes inotify records directly from the descriptor. Nothing
// abstracts this, since it is the one part of the tree that is not portable.
func (w *Watcher) readLoop() {
	buf := make([]byte, eventBufBytes)
	for {
		n, err := w.inotify.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
				return
			}
			select {
			case <-w.stop:
				return
			default:
			}
			slog.Warn("the watch descriptor stopped reading; change detection is now the rescan alone",
				slog.Any("error", err))
			return
		}
		w.consume(buf[:n])
	}
}

// consume parses one read's worth of records, fail-closed.
//
// A record that does not parse whole stops the batch and escalates to a
// whole-share invalidation. Resynchronizing by guessing at the next record
// boundary is how a parser silently skips events, and a skipped event is a
// stale answer nothing corrects.
func (w *Watcher) consume(buf []byte) {
	for off := 0; off < len(buf); {
		if off+inotifyEventHeader > len(buf) {
			// A trailing fragment shorter than a header. The kernel does not
			// split records across reads, so this is a buffer this code no
			// longer understands.
			w.escalate("an inotify record header was truncated")
			return
		}

		// Widened instead of reread as a signed descriptor. Queue overflow is
		// the only record bearing a negative wd, and the mask check below
		// identifies it before any lookup occurs.
		wd := int(binary.NativeEndian.Uint32(buf[off+offWd:]))
		mask := binary.NativeEndian.Uint32(buf[off+offMask:])
		nameLen := binary.NativeEndian.Uint32(buf[off+offNameLen:])

		size, err := num.Narrow[int](nameLen)
		if err != nil || off+inotifyEventHeader+size > len(buf) {
			// A name running past the buffer. Same answer: this batch cannot be
			// trusted and neither can what follows it.
			w.escalate("an inotify record name ran past the buffer")
			return
		}
		off += inotifyEventHeader + size

		switch {
		case mask&unix.IN_Q_OVERFLOW != 0:
			// Events were discarded by the kernel faster than they could be
			// consumed, leaving the dirty set incomplete and unusable. This
			// record's watch descriptor is unspecified, so no individual
			// directory can be marked and full invalidation is the response.
			w.overflow.Store(true)
		case mask&(unix.IN_IGNORED|unix.IN_UNMOUNT) != 0:
			w.forget(wd)
		default:
			w.markDirty(wd)
		}
	}
}

// escalate turns a parse failure into a whole-share invalidation, which the
// flush loop performs on its next tick.
func (w *Watcher) escalate(reason string) {
	slog.Warn("the inotify stream could not be parsed; invalidating whole shares",
		slog.String("reason", reason))
	w.overflow.Store(true)
}

// markDirty records that a watched directory changed, at the time it first did
// so within this debounce window.
func (w *Watcher) markDirty(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k, ok := w.wdToKey[wd]
	if !ok {
		return
	}
	if _, seen := w.pending[k]; !seen {
		w.pending[k] = w.clk.Now()
	}
}

// forget clears the bookkeeping for a watch the kernel has already released,
// the outcome of a directory being deleted or unmounted.
func (w *Watcher) forget(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k, ok := w.wdToKey[wd]
	if !ok {
		return
	}
	delete(w.wdToKey, wd)
	delete(w.keyToWd, k)
	w.hot.markUnregistered(k)
}

// flushLoop emits directories that have remained dirty across the debounce
// window, collapsing a burst that completes within it into a single event.
//
// Two conditions bypass this and invalidate entire shares: the kernel reporting
// its queue overflowed (or the parser rejecting a batch), and the dirty set
// exceeding the threshold. Either way the per-directory set is no longer worth
// walking, and the cost becomes one event per share instead of one per
// directory.
func (w *Watcher) flushLoop() {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}

		overflowed := w.overflow.Swap(false)
		w.mu.Lock()
		pendingLen := len(w.pending)
		w.mu.Unlock()

		if overflowed || pendingLen > w.cfg.FullThreshold {
			w.invalidateEverything(overflowed, pendingLen)
			continue
		}
		for _, k := range w.ready() {
			w.emit(InvalEvent{Share: k.share, Dir: k.dir})
		}
	}
}

func (w *Watcher) ready() []key {
	now := w.clk.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []key
	for k, first := range w.pending {
		if now.Sub(first) >= w.cfg.Debounce {
			out = append(out, k)
			delete(w.pending, k)
		}
	}
	return out
}

func (w *Watcher) invalidateEverything(kernelOverflow bool, pendingLen int) {
	w.mu.Lock()
	clear(w.pending)
	ids := make([]vfs.ShareID, 0, len(w.shares))
	for id := range w.shares {
		ids = append(ids, id)
	}
	w.mu.Unlock()

	slog.Warn("the watch queue overflowed; invalidating whole shares instead of directories",
		slog.Bool("kernel overflow", kernelOverflow),
		slog.Int("pending", pendingLen),
		slog.Int("threshold", w.cfg.FullThreshold))

	for _, id := range ids {
		w.emit(InvalEvent{Share: id, All: true})
	}
}

// emit delivers a single event to the consumer. A full sink means the consumer
// has fallen behind, and discarding a per-directory event would leave a stale
// answer that nothing later fixes, so it escalates to whole-share invalidation:
// the same bargain the kernel's own queue strikes on overflow.
func (w *Watcher) emit(ev InvalEvent) {
	select {
	case <-w.stop:
		return
	default:
	}
	select {
	case w.sink <- ev:
	case <-w.stop:
	default:
		w.overflow.Store(true)
	}
}

// rescanLoop re-flags the watched directories belonging to shares on
// event-losing filesystems. It never performs a new recursive walk, revisiting
// only what is already tracked.
func (w *Watcher) rescanLoop() {
	t := time.NewTicker(w.cfg.RescanInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}
		w.rescan()
	}
}

func (w *Watcher) rescan() {
	w.mu.Lock()
	defer w.mu.Unlock()

	unreliable := make(map[vfs.ShareID]struct{})
	for id, s := range w.shares {
		if s.rescan {
			unreliable[id] = struct{}{}
		}
	}
	now := w.clk.Now()
	for _, k := range w.hot.registeredKeys() {
		if _, ok := unreliable[k.share]; !ok {
			continue
		}
		if _, seen := w.pending[k]; !seen {
			w.pending[k] = now
		}
	}
}
