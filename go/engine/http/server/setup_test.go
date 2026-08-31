// Linux only, matching the file under test.
//go:build linux

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// countingAccounts answers the durable question the gate asks.
type countingAccounts struct {
	mu    sync.Mutex
	count int64
	err   error
}

func (a *countingAccounts) CountUsers(context.Context) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count, a.err
}

func (a *countingAccounts) set(n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count = n
}

// setupClock is a clock the test moves by hand.
type setupClock struct {
	mu sync.Mutex
	at time.Time
}

func newSetupClock() *setupClock {
	return &setupClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *setupClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *setupClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *setupClock) Nanos() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at.UnixNano()
}

func (c *setupClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// An issued token is 32 bytes of hex, and each one differs.
func TestTheSetupTokenIsFreshAndLongEnough(t *testing.T) {
	ctx := context.Background()
	accounts := &countingAccounts{}
	seen := map[string]bool{}

	for range 32 {
		g := NewSetupGate(newSetupClock(), accounts)
		tok, err := g.Issue(ctx)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if len(tok) != 64 {
			t.Fatalf("the token is %d characters", len(tok))
		}
		if strings.Trim(tok, "0123456789abcdef") != "" {
			t.Fatalf("the token is not hex: %q", tok)
		}
		if seen[tok] {
			t.Fatal("a token was minted twice")
		}
		seen[tok] = true
	}
}

// An account existing closes the gate, whatever the token says. A stale
// plaintext file on disk is not an authorisation fact.
func TestAnExistingAccountClosesTheGate(t *testing.T) {
	ctx := context.Background()
	accounts := &countingAccounts{}
	g := NewSetupGate(newSetupClock(), accounts)

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if verr := g.Verify(ctx, tok); verr != nil {
		t.Fatalf("a fresh token was refused: %v", verr)
	}

	// Somebody finished setting up.
	accounts.set(1)

	if verr := g.Verify(ctx, tok); !errors.Is(verr, ErrSetupClosed) {
		t.Errorf("a valid token after setup returned %v", verr)
	}
	if _, ierr := g.Issue(ctx); !errors.Is(ierr, ErrSetupClosed) {
		t.Errorf("reissuing after setup returned %v", ierr)
	}
	uerr := g.Use(ctx, tok, func(context.Context) error { return nil })
	if !errors.Is(uerr, ErrSetupClosed) {
		t.Errorf("using a token after setup returned %v", uerr)
	}
}

// A database that cannot answer closes the gate rather than opening it. "How
// many accounts exist" going unanswered is not an answer of none.
func TestAnUnreadableAccountCountClosesTheGate(t *testing.T) {
	ctx := context.Background()
	accounts := &countingAccounts{err: errors.New("the database is unreadable")}
	g := NewSetupGate(newSetupClock(), accounts)

	open, err := g.Open(ctx)
	if open {
		t.Error("an unreadable count left the gate open")
	}
	if !errors.Is(err, ErrSetupClosed) {
		t.Errorf("an unreadable count returned %v", err)
	}
	if _, ierr := g.Issue(ctx); !errors.Is(ierr, ErrSetupClosed) {
		t.Errorf("issuing against an unreadable count returned %v", ierr)
	}
}

// A token expires, and expiry is checked before the comparison so an expired
// token is refused without the digest being compared at all.
func TestATokenExpires(t *testing.T) {
	ctx := context.Background()
	clk := newSetupClock()
	g := NewSetupGate(clk, &countingAccounts{})

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clk.advance(SetupTokenLifetime - time.Second)
	if verr := g.Verify(ctx, tok); verr != nil {
		t.Fatalf("a token just inside its lifetime was refused: %v", verr)
	}

	clk.advance(2 * time.Second)
	verr := g.Verify(ctx, tok)
	if !errors.Is(verr, ErrSetupToken) {
		t.Fatalf("an expired token returned %v", verr)
	}
	if !strings.Contains(verr.Error(), "expired") {
		t.Errorf("the refusal says %q", verr)
	}
}

// A token is usable once. The second attempt is refused even though the token
// itself is still correct.
func TestATokenIsUsableOnce(t *testing.T) {
	ctx := context.Background()
	g := NewSetupGate(newSetupClock(), &countingAccounts{})

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ran := 0
	if uerr := g.Use(ctx, tok, func(context.Context) error { ran++; return nil }); uerr != nil {
		t.Fatalf("the first use returned %v", uerr)
	}
	if uerr := g.Use(ctx, tok, func(context.Context) error { ran++; return nil }); !errors.Is(uerr, ErrSetupToken) {
		t.Errorf("the second use returned %v", uerr)
	}
	if ran != 1 {
		t.Errorf("the work ran %d times", ran)
	}
}

// A failed attempt leaves the token usable, because the alternative is an
// operator whose one token is spent on a password the server rejected.
func TestAFailedAttemptDoesNotSpendTheToken(t *testing.T) {
	ctx := context.Background()
	g := NewSetupGate(newSetupClock(), &countingAccounts{})

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	refused := errors.New("that password is too short")
	if uerr := g.Use(ctx, tok, func(context.Context) error { return refused }); !errors.Is(uerr, refused) {
		t.Fatalf("the failed attempt returned %v", uerr)
	}
	// The same token still works.
	if uerr := g.Use(ctx, tok, func(context.Context) error { return nil }); uerr != nil {
		t.Errorf("the retry returned %v", uerr)
	}
}

// A request arriving before anything was issued is refused, and says so
// distinctly: a stale file on disk must not pass a gate that minted nothing.
func TestNothingIssuedIsItsOwnAnswer(t *testing.T) {
	ctx := context.Background()
	g := NewSetupGate(newSetupClock(), &countingAccounts{})

	err := g.Verify(ctx, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if !errors.Is(err, ErrSetupNotIssued) {
		t.Errorf("a token against an unissued gate returned %v", err)
	}
	if errors.Is(err, ErrSetupToken) {
		t.Error("an unissued gate reported the token as merely wrong")
	}
}

// A wrong token is refused. Constant-time comparison is not observable from a
// test, so what is checked is that no near-miss passes.
func TestAWrongTokenIsRefused(t *testing.T) {
	ctx := context.Background()
	g := NewSetupGate(newSetupClock(), &countingAccounts{})

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for _, c := range []struct{ what, presented string }{
		{"empty", ""},
		{"truncated", tok[:len(tok)-1]},
		{"one character changed", tok[:len(tok)-1] + flipHex(tok[len(tok)-1])},
		{"with a prefix", "x" + tok},
		{"upper case", strings.ToUpper(tok)},
	} {
		if verr := g.Verify(ctx, c.presented); !errors.Is(verr, ErrSetupToken) {
			t.Errorf("%s returned %v", c.what, verr)
		}
	}
}

func flipHex(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}

// Two requests arriving together create one administrator, not two. The mutex
// covering the check and the work is what makes that true.
func TestConcurrentSetupCreatesOneAdministrator(t *testing.T) {
	ctx := context.Background()
	accounts := &countingAccounts{}
	g := NewSetupGate(newSetupClock(), accounts)

	tok, err := g.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The work overlaps deliberately: it counts itself in, waits, then closes
	// the gate. Without the mutex covering both the check and the work, a
	// second request passes the check during that wait, and inside is what
	// counts two administrators.
	//
	// Closing the gate at the end rather than the start is the point. Doing it
	// first would make the account count serialise the requests and the mutex
	// would not be under test at all.
	var mu sync.Mutex
	created := 0
	refused := 0
	inside := 0
	maxInside := 0

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		task.Go(ctx, "server: concurrent setup", func() {
			defer wg.Done()
			// Fifteen of the sixteen are refused, which is the whole point:
			// counted rather than discarded so a run where none was refused
			// would be visible here rather than only in the created count.
			uerr := g.Use(ctx, tok, func(context.Context) error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				created++
				mu.Unlock()

				time.Sleep(5 * time.Millisecond)

				mu.Lock()
				inside--
				mu.Unlock()
				accounts.set(1)
				return nil
			})
			if uerr != nil {
				mu.Lock()
				refused++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if created != 1 {
		t.Errorf("%d administrators were created", created)
	}
	if maxInside > 1 {
		t.Errorf("%d requests were inside the gate at once", maxInside)
	}
	if refused != 15 {
		t.Errorf("%d of 16 requests were refused, want 15", refused)
	}
}
