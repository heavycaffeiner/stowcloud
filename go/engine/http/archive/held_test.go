//go:build linux

package archive

import (
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// bounded builds a store with small bounds, so a test states the rule rather
// than allocating a gigabyte to reach the real one.
func bounded(t *testing.T, perArchive, perOwner, total int64) *Store {
	t.Helper()

	s := NewStore(nil)
	s.perArchive, s.perOwner, s.total = perArchive, perOwner, total
	return s
}

// Several people downloading at once cannot exceed the total.
//
// This is the bound that protects the machine. Reserving after the build
// instead of before would let every concurrent build allocate its bytes and
// only then discover there was room for one, which is exactly the allocation
// the bound exists to prevent.
func TestReservationsCannotExceedTheTotal(t *testing.T) {
	// Room for two reservations, asked for by eight callers.
	s := bounded(t, 100, 1_000, 200)

	granted := 0
	for owner := range 8 {
		if _, err := s.Reserve(int64(owner)); err == nil {
			granted++
		}
	}

	if granted != 2 {
		t.Errorf("%d reservations were granted against room for 2", granted)
	}
}

// One account cannot spend the whole budget.
//
// Without a per-owner bound, one person downloading several folders leaves
// everybody else on the unresumable path while the machine is nowhere near
// its ceiling.
func TestOneOwnerCannotTakeTheWholeBudget(t *testing.T) {
	s := bounded(t, 100, 200, 1_000)

	for i := range 2 {
		if _, err := s.Reserve(7); err != nil {
			t.Fatalf("reservation %d for the first owner was refused: %v", i, err)
		}
	}
	if _, err := s.Reserve(7); err == nil {
		t.Error("an owner reserved past their own bound")
	}

	// And the budget they could not take is still there for somebody else.
	if _, err := s.Reserve(8); err != nil {
		t.Errorf("another owner was refused room the first could not use: %v", err)
	}
}

// A reservation that does not publish gives its budget back.
//
// A build that fails partway must not leave the claim behind: the bytes were
// never allocated, and holding the claim would shrink the budget for every
// later download until the process restarted.
func TestAnAbandonedReservationIsReturned(t *testing.T) {
	s := bounded(t, 100, 1_000, 100)

	res, err := s.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(2); err == nil {
		t.Fatal("the store had room while its whole budget was reserved")
	}

	res.Release()
	if _, err := s.Reserve(2); err != nil {
		t.Errorf("the budget was not returned: %v", err)
	}
}

// Publishing charges the archive's real size, not the reservation.
//
// The reservation is pessimistic by design: it claims the per-archive bound
// because the size is unknown until the build finishes. Keeping that claim
// afterwards would hold budget nothing is using.
func TestPublishingChargesTheRealSize(t *testing.T) {
	s := bounded(t, 100, 1_000, 100)

	res, err := s.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	held := &Held{Name: "a.zip", Bytes: make([]byte, 10), Owner: 1}
	if perr := s.Put("token", held, res); perr != nil {
		t.Fatalf("publishing: %v", perr)
	}

	if got := s.HeldBytes(); got != 10 {
		t.Errorf("the store holds %d bytes, want the archive's own 10", got)
	}
	// 90 bytes came back, so another build fits.
	if _, err := s.Reserve(2); err == nil {
		t.Log("a second reservation fits, which is the point")
	}
}

// An expiring archive returns its owner's budget too.
//
// The TTL is the only reclaim: a fetch cannot tell a finished transfer from
// one the client abandoned, so releasing on the first delivery would break
// the resume the hold exists for.
func TestExpiryReturnsTheOwnersBudget(t *testing.T) {
	s := bounded(t, 100, 100, 1_000)

	now := time.Unix(1_700_000_000, 0)
	s.clk = clock.Fixed(now)

	res, err := s.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if perr := s.Put("t", &Held{Name: "a.zip", Bytes: make([]byte, 100), Owner: 1}, res); perr != nil {
		t.Fatal(perr)
	}
	if _, err := s.Reserve(1); err == nil {
		t.Fatal("the owner reserved past their bound while holding it all")
	}

	s.clk = clock.Fixed(now.Add(2 * time.Hour))
	if _, err := s.Reserve(1); err != nil {
		t.Errorf("the owner's budget was not returned on expiry: %v", err)
	}
}

// An expired archive releases its bytes without anybody fetching it.
//
// A download nobody collects must not hold memory until the process restarts.
func TestAnExpiredArchiveReleasesItsBytes(t *testing.T) {
	s := bounded(t, 100, 1_000, 100)

	// A fixed instant, so the test states the expiry rule rather than
	// depending on when it ran.
	now := time.Unix(1_700_000_000, 0)
	s.clk = clock.Fixed(now)

	res, err := s.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if perr := s.Put("t", &Held{Name: "a.zip", Bytes: make([]byte, 100), Owner: 1}, res); perr != nil {
		t.Fatal(perr)
	}

	s.clk = clock.Fixed(now.Add(2 * time.Hour))
	if _, ok := s.Get("t", 1); ok {
		t.Error("an expired archive is still fetchable")
	}
	if got := s.HeldBytes(); got != 0 {
		t.Errorf("the store still holds %d bytes after expiry", got)
	}
}

// A ticket does not cross accounts.
func TestATicketBelongsToOneAccount(t *testing.T) {
	s := bounded(t, 100, 1_000, 1_000)

	res, err := s.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if perr := s.Put("t", &Held{Name: "a.zip", Bytes: []byte("x"), Owner: 1}, res); perr != nil {
		t.Fatal(perr)
	}

	if _, ok := s.Get("t", 2); ok {
		t.Error("another account fetched an archive built for somebody else")
	}
	if _, ok := s.Get("t", 1); !ok {
		t.Error("the owner cannot fetch their own archive")
	}
}

// The buffer refuses past its bound rather than growing to fit.
func TestTheBufferStopsAtItsBound(t *testing.T) {
	b := NewBuffer(10)

	if _, err := b.Write(make([]byte, 6)); err != nil {
		t.Fatalf("a write inside the bound was refused: %v", err)
	}
	if _, err := b.Write(make([]byte, 6)); err == nil {
		t.Error("a write past the bound was accepted")
	}
	if got := len(b.Bytes()); got != 6 {
		t.Errorf("the buffer holds %d bytes; the refused write was kept", got)
	}
}
