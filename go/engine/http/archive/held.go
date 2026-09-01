// Linux only, because it serves a Linux-only engine.
//go:build linux

// Holding a built archive in memory so its download can be resumed.
//
// A streamed archive cannot be resumed. Its length is unknown until the last
// entry is written, so the response carries no Content-Length and no
// Accept-Ranges, and a browser that loses the connection at 90% starts again
// from zero. For a folder of any size that is the difference between a
// download that finishes and one that never does.
//
// Held in memory rather than spooled to disk. A temporary zip per download
// fills the data volume, and the volume filling takes the database with it.
// RAM is the bounded resource here, so the bound is stated: an archive over
// it is streamed instead, which cannot be resumed but costs nothing to keep.
package archive

import (
	"errors"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// ErrTooLarge is a build that crossed the per-archive memory bound. The
// caller streams instead.
var ErrTooLarge = errors.New("the archive is too large to hold in memory")

// ErrNoRoom is a build refused because the server is already holding as much
// as it will. The caller streams instead.
var ErrNoRoom = errors.New("no room to hold another archive")

// Held is one built archive waiting to be fetched.
type Held struct {
	// Name is the download's filename, decided when the archive was built.
	Name string

	// Bytes is the whole archive. Never mutated after the hold is published,
	// so concurrent readers need no copy and no lock.
	Bytes []byte

	// Owner is the account that built it. A ticket is a capability, and one
	// leaking to another account must not become a way to read their files.
	Owner int64

	expires time.Time
}

// Store holds built archives for a short while, under bounds that hold with
// any number of people downloading at once.
//
// Three bounds, because one is not enough. Per archive stops a single folder
// from being the whole budget. Per owner stops one account from spending it
// all and leaving everybody else on the unresumable path. The total is the
// ceiling that actually protects the machine: without it every concurrent
// download is another copy of its own archive in the heap, and the server
// dies of a feature working as designed.
//
// Budget is reserved before an archive is built, not after. Reserving
// afterwards means N concurrent builds each allocate their bytes and only
// then discover there was room for one, which is the moment the bound was
// supposed to prevent.
type Store struct {
	mu    sync.Mutex
	held  map[string]*Held
	bytes int64

	// reserved is budget claimed by builds in flight, counted separately
	// because those bytes are being allocated right now and are not yet in
	// held.
	reserved int64

	// byOwner is each account's share of held and reserved together, so one
	// account cannot take the whole budget.
	byOwner map[int64]int64

	// clk supplies the expiry deadline. Injected rather than read from the
	// wall clock, which is the tree's rule and what lets a test state the
	// expiry without sleeping.
	clk clock.Clock

	// perArchive, perOwner and total are the bounds, held as fields so a test
	// can lower them rather than building a gigabyte.
	perArchive int64
	perOwner   int64
	total      int64
}

// NewStore builds a store at the configured bounds.
func NewStore(clk clock.Clock) *Store {
	if clk == nil {
		clk = clock.System()
	}
	return &Store{
		held:       map[string]*Held{},
		byOwner:    map[int64]int64{},
		clk:        clk,
		perArchive: limits.ArchiveHeldBytes,
		perOwner:   limits.ArchiveHeldPerOwnerBytes,
		total:      limits.ArchiveHeldTotalBytes,
	}
}

// Reservation is budget claimed for one build in flight.
//
// Held until the build either publishes or gives up, so two people starting
// a download at the same moment cannot both be told there is room for them.
type Reservation struct {
	store *Store
	owner int64
	size  int64
	done  bool
}

// Bound is how many bytes this reservation may build into.
func (r *Reservation) Bound() int64 { return r.size }

// Release returns unpublished budget. Safe to call twice, so a deferred
// release after a successful publish costs nothing.
func (r *Reservation) Release() {
	if r == nil || r.done {
		return
	}
	r.done = true
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.reserved -= r.size
	r.store.chargeOwnerLocked(r.owner, -r.size)
}

// Reserve claims budget for one build.
//
// The reservation is for the per-archive bound rather than the archive's
// eventual size, because the size is not known until it is built. That is
// deliberately pessimistic: it is what stops the total from being exceeded by
// builds that were all under the bound individually.
func (s *Store) Reserve(owner int64) (*Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	size := s.perArchive
	if s.bytes+s.reserved+size > s.total {
		return nil, ErrNoRoom
	}
	if s.byOwner[owner]+size > s.perOwner {
		return nil, ErrNoRoom
	}
	s.reserved += size
	s.chargeOwnerLocked(owner, size)
	return &Reservation{store: s, owner: owner, size: size}, nil
}

// chargeOwnerLocked adjusts one account's usage, dropping the entry at zero
// so the map does not grow one key per account that ever downloaded.
func (s *Store) chargeOwnerLocked(owner, delta int64) {
	next := s.byOwner[owner] + delta
	if next <= 0 {
		delete(s.byOwner, owner)
		return
	}
	s.byOwner[owner] = next
}

// PerArchiveBound is the largest archive this store will accept.
//
// Read by the builder to stop copying the moment the bound is crossed, rather
// than building the whole thing and discovering it does not fit.
func (s *Store) PerArchiveBound() int64 { return s.perArchive }

// Put converts a reservation into a hold.
//
// The reservation is what guaranteed the room, so this cannot fail on budget:
// the claim was made before the bytes were allocated. What it does is swap
// the pessimistic reservation for the archive's real size, which hands the
// difference back to whoever is waiting.
//
// The token is minted by the caller because it also has to reach the client,
// and a store that invented one would hand back a second thing to plumb.
func (s *Store) Put(token string, h *Held, res *Reservation) error {
	size := int64(len(h.Bytes))
	if size > s.perArchive {
		return ErrTooLarge
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	// The reservation is released and the real size charged in one step, so
	// no window exists where the budget shows neither.
	if res != nil && !res.done {
		res.done = true
		s.reserved -= res.size
		s.chargeOwnerLocked(res.owner, -res.size)
	}
	if s.bytes+s.reserved+size > s.total || s.byOwner[h.Owner]+size > s.perOwner {
		// Only reachable without a reservation, which no caller does today.
		// It stays because the alternative is holding bytes nothing bounded.
		return ErrNoRoom
	}

	h.expires = s.clk.Now().Add(limits.ArchiveTicketTTL)
	s.held[token] = h
	s.bytes += size
	s.chargeOwnerLocked(h.Owner, size)
	return nil
}

// Get returns a held archive for one owner.
//
// The owner is compared here rather than by the caller: a ticket is a
// capability, and the one place that resolves tickets is the one place that
// can be sure the check is not skipped.
func (s *Store) Get(token string, owner int64) (*Held, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	h, ok := s.held[token]
	if !ok || h.Owner != owner {
		// The same answer for a token that does not exist and one belonging
		// to somebody else: distinguishing them would confirm that a guessed
		// token names a real archive.
		return nil, false
	}
	return h, true
}

// HeldBytes is what the store is currently holding, for a test or a metric.
func (s *Store) HeldBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// sweepLocked drops what has expired. Called on every operation rather than
// from a timer: the store is only interesting while it is being used, and a
// goroutine per store is a lifetime to manage for no gain.
func (s *Store) sweepLocked() {
	now := s.clk.Now()
	for token, h := range s.held {
		if now.After(h.expires) {
			size := int64(len(h.Bytes))
			s.bytes -= size
			s.chargeOwnerLocked(h.Owner, -size)
			delete(s.held, token)
		}
	}
}

// Buffer collects an archive in memory and refuses past a bound.
//
// The bound is checked as bytes arrive rather than after the archive is
// built: an archive that will not fit must not be fully allocated first,
// which is exactly the allocation the bound exists to prevent.
type Buffer struct {
	buf   []byte
	bound int64
}

// NewBuffer starts a buffer that refuses past bound bytes.
func NewBuffer(bound int64) *Buffer {
	return &Buffer{bound: bound}
}

// Write appends, refusing with ErrTooLarge once the bound is crossed.
func (b *Buffer) Write(p []byte) (int, error) {
	if int64(len(b.buf))+int64(len(p)) > b.bound {
		return 0, ErrTooLarge
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// Bytes is what was collected.
func (b *Buffer) Bytes() []byte { return b.buf }
