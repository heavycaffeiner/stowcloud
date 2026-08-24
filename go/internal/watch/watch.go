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

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"golang.org/x/sys/unix"
)

// watchMask is what a directory watch asks the kernel to report. Entries
// appearing, disappearing, changing content or changing metadata, which is
// every way a listing this server already answered can become wrong.
const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_MOVE_SELF |
	unix.IN_DELETE_SELF | unix.IN_ATTRIB

// eventBufBytes holds a batch of inotify records. One name is at most 255
// bytes, so this reads many events per syscall on a busy tree.
const eventBufBytes = 16 << 10

// flushInterval is how often the debounce loop looks for entries that have
// waited out their window.
const flushInterval = 50 * time.Millisecond

// inotifyEventHeader is the fixed part of struct inotify_event: wd, mask,
// cookie and len, four 32-bit fields, followed by len bytes of NUL-padded name.
const inotifyEventHeader = 16

// share is one registered share root, in the only terms the transport can use.
type share struct {
	host string
	// rescan is set for a filesystem whose changes inotify cannot see, which is
	// every network and FUSE mount. The periodic sweep is then the only thing
	// that notices a change another host made.
	rescan bool
}

// Watcher is the change detector. One kernel watch descriptor, one reader, one
// debounce loop and one rescan loop.
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

// Start brings up the watcher. The sink receives one event per debounced
// directory, and the caller owns the channel.
func Start(ctx context.Context, cfg Config, clk clock.Clock, sink chan<- InvalEvent) (*Watcher, error) {
	cfg = cfg.withDefaults()

	// Non-blocking, so the descriptor joins the runtime's poller and a Close
	// unblocks the reader instead of leaving a thread parked in read.
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

// AddShare registers where a share lives on the host. This is the one place the
// watcher learns a host path, because the kernel watch API takes one and
// nothing else here does.
func (w *Watcher) AddShare(id vfs.ShareID, hostRoot string, rescan bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shares[id] = share{host: hostRoot, rescan: rescan}
}

// Subscribe pins a directory and its whole ancestor chain, and returns only
// after the watches are registered, so the caller can read the directory
// afterwards without missing a change that lands in between.
//
// It is exported now even though the WebSocket layer that calls it does not
// exist yet, because the sticky half of the hot set is fed from outside and a
// hot set with only its LRU half fails silently on exactly the directory a user
// is looking at.
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

// Unsubscribe releases one subscriber's pin on a directory and its ancestors.
func (w *Watcher) Unsubscribe(id vfs.ShareID, p vfs.SafePath) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range ancestorKeys(id, p) {
		w.hot.removeSticky(k)
	}
}

// Touch records that a directory was read, which is what feeds the evictable
// half of the hot set.
func (w *Watcher) Touch(id vfs.ShareID, p vfs.SafePath) {
	k := key{share: id, dir: p.String()}
	w.mu.Lock()
	isNew := w.hot.touch(k)
	w.mu.Unlock()
	if isNew {
		w.register(k)
	}
}

func (w *Watcher) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Stats{
		Registered: w.hot.registeredCount(),
		Degraded:   w.degraded.Load(),
		Shares:     len(w.shares),
	}
}

// Close stops every loop and releases the watch descriptor.
func (w *Watcher) Close() error {
	var err error
	w.stopOnce.Do(func() {
		close(w.stop)
		err = w.inotify.Close()
		w.wg.Wait()
	})
	return err
}

// ancestorKeys is the directory itself and everything above it up to the share
// root. A change to a child changes the parent's listing, so a subscriber to a
// deep directory pins the chain that renders it.
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

// register puts a kernel watch on k, evicting the least recently used
// unpinned directories first so the registered set stays under the cap.
//
// A registration the kernel refuses degrades that subtree to lazy revalidation
// rather than failing the caller's read: the read still returns the right
// answer, it just does not get a pushed update afterwards.
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
	// inotify returns the existing descriptor when a path is watched twice, so
	// the reverse map is keyed on what the kernel actually handed back.
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

// addWatch and rmWatch go through SyscallConn rather than Fd, because Fd would
// put the descriptor back into blocking mode and take it out of the poller,
// which is what lets Close unblock the reader.
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
	// The kernel hands watch descriptors back as a signed int and takes them
	// back as unsigned, so this crossing is checked rather than cast.
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

// readLoop parses inotify records straight off the descriptor. There is no
// portability layer over the one thing this tree does not make portable.
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

func (w *Watcher) consume(buf []byte) {
	for off := 0; off+inotifyEventHeader <= len(buf); {
		// Widened rather than reinterpreted as a signed descriptor. The one
		// record that carries a negative wd is the queue overflow, and that is
		// recognised by its mask below before anything looks the wd up.
		wd := int(binary.NativeEndian.Uint32(buf[off:]))
		mask := binary.NativeEndian.Uint32(buf[off+4:])
		nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
		size := inotifyEventHeader + nameLen
		if size <= 0 || off+size > len(buf) {
			return
		}
		off += size

		if mask&unix.IN_Q_OVERFLOW != 0 {
			// The kernel dropped events faster than they were read, so the
			// dirty set is now incomplete and cannot be trusted. The watch
			// descriptor for this record is unspecified, so there is no single
			// directory to mark: the full invalidation is the answer.
			w.overflow.Store(true)
			continue
		}
		if mask&(unix.IN_IGNORED|unix.IN_UNMOUNT) != 0 {
			w.forget(wd)
			continue
		}
		w.mu.Lock()
		k, ok := w.wdToKey[wd]
		if ok {
			if _, seen := w.pending[k]; !seen {
				w.pending[k] = w.clk.Now()
			}
		}
		w.mu.Unlock()
	}
}

// forget drops the bookkeeping for a watch the kernel has already discarded,
// which is what a deleted or unmounted directory produces.
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

// flushLoop reports directories that have sat dirty for the debounce window, so
// a burst that finishes inside it coalesces into one event.
//
// Two signals short-circuit that and invalidate whole shares instead: the
// kernel reported its own queue overflowed, or the dirty set outgrew the
// threshold. Both mean the per-directory set is no longer worth enumerating,
// and both cost one event per share rather than one per directory.
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

// emit hands one event to the consumer. A sink that is full means the consumer
// is behind, and a dropped per-directory event is a stale answer nothing
// corrects, so it escalates to the whole-share invalidation instead: the same
// trade the kernel's own queue makes when it overflows.
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

// rescanLoop re-marks the watched directories of every share whose filesystem
// loses events. Never a fresh recursive walk: it only revisits what is already
// tracked.
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
	unreliable := make(map[vfs.ShareID]struct{})
	for id, s := range w.shares {
		if s.rescan {
			unreliable[id] = struct{}{}
		}
	}
	keys := w.hot.registeredKeys()
	now := w.clk.Now()
	for _, k := range keys {
		if _, ok := unreliable[k.share]; !ok {
			continue
		}
		if _, seen := w.pending[k]; !seen {
			w.pending[k] = now
		}
	}
	w.mu.Unlock()
}
