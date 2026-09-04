// Linux only, because it serves a Linux-only engine.
//go:build linux

// Naming a selection so a browser can fetch it.
//
// A folder download is a POST: the selection is a list of paths, too long and
// too structured for a query string, and it is a request that needs a CSRF
// header. A browser cannot navigate to one, so the response body is the only
// place the archive could go, and reading a POST body means holding the whole
// archive in the tab before any of it reaches the disk.
//
// The ticket breaks that. The POST validates the selection and records it;
// the GET that follows is a plain navigation the browser owns, so the bytes
// land progressively at no cost to the tab and the download appears in the
// browser's own list. Nothing is built here and no bytes are kept: a ticket is
// a few hundred bytes naming what to walk when somebody asks.
package archive

import (
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// Kind says what a ticket is for. A ticket is a capability, and the route
// that mints one is not the only route that could resolve it: naming the kind
// is what keeps each route serving only its own.
type Kind uint8

const (
	// KindArchive walks the selection into a zip.
	KindArchive Kind = iota + 1
	// KindFile streams one file as it is on disk.
	KindFile
)

// Ticket is one validated selection waiting to be fetched.
type Ticket struct {
	// Kind decides which route may fetch it and what that route does.
	Kind Kind

	// Name is the download's filename, decided when the ticket was minted.
	Name string

	// Paths are the selection, exactly as the request named them. Re-resolved
	// on fetch rather than stored resolved: a grant can be revoked between the
	// two requests, and the fetch must answer to the permissions the account
	// holds then rather than the ones it held when it clicked.
	//
	// A file ticket holds exactly one.
	Paths []string

	// Owner is the account that minted it. A ticket is a capability, and one
	// leaking to another account must not become a way to read their files.
	Owner int64

	expires time.Time
}

// Tickets holds minted selections for a short while.
//
// One bound, on the count. A ticket holds no bytes, so the cost of one is its
// path list and the ceiling exists to stop a caller minting without end rather
// than to protect any memory the archives occupy.
type Tickets struct {
	mu  sync.Mutex
	clk clock.Clock
	// byToken is every live ticket. Swept on write rather than by a timer, so
	// an idle server runs nothing.
	byToken map[string]*Ticket
}

// NewTickets builds an empty store.
func NewTickets(clk clock.Clock) *Tickets {
	return &Tickets{clk: clk, byToken: make(map[string]*Ticket)}
}

// Put records a selection under a token.
//
// Refuses when the store is full rather than evicting: dropping somebody
// else's ticket to make room turns one person's download into another's
// failure, and the ceiling is high enough that reaching it is a caller minting
// tickets it never fetches.
func (s *Tickets) Put(token string, t *Ticket) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	if len(s.byToken) >= limits.ArchiveTicketsHeld {
		return false
	}
	t.expires = s.clk.Now().Add(limits.ArchiveTicketTTL)
	s.byToken[token] = t
	return true
}

// Get returns a live ticket for one owner and one kind.
//
// The owner is compared here rather than by the caller: a ticket is a
// capability, and the one place that resolves them is the one place that can
// be sure the check is not skipped.
//
// The kind is compared for the same reason. A ticket minted for one file and
// fetched through the archive route would zip a single file, which is not what
// either side asked for, and a route serving a ticket it did not mint is a
// seam nobody is checking.
//
// A token that does not exist, one belonging to somebody else, and one of the
// wrong kind all answer the same, because distinguishing them would confirm
// that a guessed token names a real download.
func (s *Tickets) Get(token string, owner int64, want Kind) (*Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	t, ok := s.byToken[token]
	if !ok || t.Owner != owner || t.Kind != want {
		return nil, false
	}
	return t, true
}

// Held is how many tickets are live, for a test or a metric.
func (s *Tickets) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byToken)
}

// sweepLocked drops what has expired. Called under the lock by every operation
// that takes it, so an abandoned ticket cannot outlive its deadline even on a
// server nobody fetches from.
func (s *Tickets) sweepLocked() {
	now := s.clk.Now()
	for token, t := range s.byToken {
		if now.After(t.expires) {
			delete(s.byToken, token)
		}
	}
}
