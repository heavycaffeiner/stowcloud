//go:build linux

package archive

import (
	"strconv"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// An expired ticket is not fetchable, and does not sit in the map forever.
//
// The deadline is what stops a token from being a durable capability: one
// left in a browser's history should stop working, and the store should not
// grow with selections nobody collected.
func TestAnExpiredTicketIsGone(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewTickets(clock.Fixed(now))

	if ok := s.Put("t", &Ticket{Kind: KindArchive, Name: "a.zip", Paths: []string{"/x"}, Owner: 13}); !ok {
		t.Fatal("the ticket was refused")
	}
	if _, ok := s.Get("t", 13, KindArchive); !ok {
		t.Fatal("a live ticket is not fetchable")
	}

	s.clk = clock.Fixed(now.Add(limits.ArchiveTicketTTL + time.Second))
	if _, ok := s.Get("t", 13, KindArchive); ok {
		t.Error("an expired ticket is still fetchable")
	}
	if held := s.Held(); held != 0 {
		t.Errorf("the store still holds %d tickets after expiry", held)
	}
}

// A ticket does not cross accounts.
//
// It is a capability, and the token is all a fetch carries. One that leaked
// must not become a way to read another account's files, and the refusal is
// the same as for a token that does not exist so that a guess learns nothing.
func TestATicketBelongsToItsOwner(t *testing.T) {
	s := NewTickets(clock.Fixed(time.Unix(1_700_000_000, 0)))
	if ok := s.Put("t", &Ticket{Kind: KindArchive, Name: "a.zip", Paths: []string{"/x"}, Owner: 13}); !ok {
		t.Fatal("the ticket was refused")
	}

	if _, ok := s.Get("t", 14, KindArchive); ok {
		t.Error("another account fetched the ticket")
	}
	if _, ok := s.Get("nonexistent", 13, KindArchive); ok {
		t.Error("a token that was never minted resolved")
	}
	if _, ok := s.Get("t", 13, KindArchive); !ok {
		t.Error("the owner cannot fetch their own ticket")
	}
}

// A full store refuses rather than evicting.
//
// Dropping somebody else's ticket to make room turns one person's download
// into another's failure, which is worse than refusing the mint that found
// the store full.
func TestAFullStoreRefusesTheMint(t *testing.T) {
	s := NewTickets(clock.Fixed(time.Unix(1_700_000_000, 0)))
	for i := range limits.ArchiveTicketsHeld {
		if ok := s.Put(strconv.Itoa(i), &Ticket{Kind: KindArchive, Name: "a.zip", Owner: 1}); !ok {
			t.Fatalf("ticket %d was refused below the bound", i)
		}
	}

	if ok := s.Put("one-too-many", &Ticket{Kind: KindArchive, Name: "a.zip", Owner: 1}); ok {
		t.Error("the store minted past its bound")
	}
	if _, ok := s.Get("0", 1, KindArchive); !ok {
		t.Error("a refused mint evicted an existing ticket")
	}
}

// A ticket does not cross kinds.
//
// The route that mints one is not the only route that could resolve it: an
// archive fetch handed a file ticket would zip a single file, and a file fetch
// handed an archive ticket would stream a folder. Each route serves its own,
// and the refusal is the same one a wrong owner gets so a guess learns
// nothing.
func TestATicketBelongsToItsKind(t *testing.T) {
	s := NewTickets(clock.Fixed(time.Unix(1_700_000_000, 0)))
	if ok := s.Put("zip", &Ticket{Kind: KindArchive, Name: "a.zip", Owner: 7}); !ok {
		t.Fatal("the archive ticket was refused")
	}
	if ok := s.Put("file", &Ticket{Kind: KindFile, Name: "a.txt", Owner: 7}); !ok {
		t.Fatal("the file ticket was refused")
	}

	if _, ok := s.Get("zip", 7, KindFile); ok {
		t.Error("the file route fetched an archive ticket")
	}
	if _, ok := s.Get("file", 7, KindArchive); ok {
		t.Error("the archive route fetched a file ticket")
	}
	if _, ok := s.Get("zip", 7, KindArchive); !ok {
		t.Error("the archive route cannot fetch its own ticket")
	}
	if _, ok := s.Get("file", 7, KindFile); !ok {
		t.Error("the file route cannot fetch its own ticket")
	}
}
