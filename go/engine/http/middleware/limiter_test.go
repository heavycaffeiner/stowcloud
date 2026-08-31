// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// stepClock is a clock the test moves by hand, so refill is measured rather
// than waited for.
type stepClock struct {
	mu sync.Mutex
	at time.Time
}

func newStepClock() *stepClock {
	return &stepClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *stepClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *stepClock) Nanos() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at.UnixNano()
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// The burst is spent, then refills at the configured rate.
func TestTheBucketSpendsItsBurstAndRefills(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 2, 3)

	for i := range 3 {
		if !l.Allow("client") {
			t.Fatalf("the burst was refused at request %d", i)
		}
	}
	if l.Allow("client") {
		t.Fatal("a fourth request was allowed against a burst of three")
	}

	// Half a second at two per second is one token.
	clk.advance(500 * time.Millisecond)
	if !l.Allow("client") {
		t.Fatal("half a second bought no token")
	}
	if l.Allow("client") {
		t.Fatal("half a second bought two tokens")
	}
}

// The bucket does not refill past its burst, so an idle client cannot bank an
// unbounded allowance and spend it at once.
func TestTheBucketDoesNotBankPastItsBurst(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 10, 2)

	// The bucket has to exist before the idle, since a bucket created now
	// starts full and has nothing to refill. Idling first would measure the
	// initial burst instead of the cap on refill.
	if !l.Allow("client") {
		t.Fatal("the first of the initial burst was refused")
	}
	if !l.Allow("client") {
		t.Fatal("the second of the initial burst was refused")
	}
	if l.Allow("client") {
		t.Fatal("the burst was more than two")
	}

	clk.advance(time.Hour)
	if !l.Allow("client") {
		t.Fatal("the first token was not available after idling")
	}
	if !l.Allow("client") {
		t.Fatal("the second token was not available after idling")
	}
	if l.Allow("client") {
		t.Fatal("an hour of idling banked more than the burst")
	}
}

// One client's spending does not affect another's.
func TestBucketsAreIndependentPerClient(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 1, 1)

	if !l.Allow("a") {
		t.Fatal("the first client was refused")
	}
	if l.Allow("a") {
		t.Fatal("the first client spent twice")
	}
	if !l.Allow("b") {
		t.Fatal("the second client was refused because the first had spent")
	}
}

// The map is capped. Past the cap a bucket is evicted rather than the new
// client being refused, since refusing would turn a full map into a denial of
// service against everyone who arrives after it fills.
func TestTheMapIsCappedAndTheNewClientIsStillServed(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 1, 1)

	for i := range LimiterCap {
		l.Allow(keyOf(i))
	}
	if got := l.Size(); got != LimiterCap {
		t.Fatalf("the map holds %d buckets, want %d", got, LimiterCap)
	}

	if !l.Allow("one-past-the-cap") {
		t.Fatal("the client past the cap was refused rather than served")
	}
	if got := l.Size(); got > LimiterCap {
		t.Fatalf("the map grew past the cap to %d", got)
	}
}

func keyOf(i int) string { return "client-" + strconv.Itoa(i) }

// Changing the limits at runtime does not reset what a client has spent.
// Resetting would let anyone who can cause a settings write clear their own
// throttle.
func TestChangingTheLimitsDoesNotClearABucket(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 1, 1)

	if !l.Allow("client") {
		t.Fatal("the first request was refused")
	}
	l.SetLimits(100, 100)
	if l.Allow("client") {
		t.Fatal("raising the limits refilled a spent bucket")
	}

	// The new rate does apply from here: at a hundred per second, ten
	// milliseconds is a token.
	clk.advance(10 * time.Millisecond)
	if !l.Allow("client") {
		t.Fatal("the new rate did not apply to the refill")
	}
}

// A nonsense rate is clamped rather than refusing every request forever.
func TestANonsenseRateIsClamped(t *testing.T) {
	clk := newStepClock()
	l := NewLimiter(clk, 0, 0)
	if !l.Allow("client") {
		t.Fatal("a zero rate refused the first request")
	}
	l.SetLimits(-5, -5)
	clk.advance(time.Second)
	if !l.Allow("client") {
		t.Fatal("a negative rate refused every request")
	}
}

// Concurrent callers share one bucket correctly: the total allowed equals the
// burst, not the number of goroutines.
func TestConcurrentCallersShareOneBucket(t *testing.T) {
	clk := newStepClock()
	const burst = 8
	l := NewLimiter(clk, 1, burst)

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 64 {
		wg.Add(1)
		task.Go(context.Background(), "middleware: concurrent limiter caller", func() {
			defer wg.Done()
			if l.Allow("shared") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if allowed != burst {
		t.Fatalf("%d of 64 concurrent requests were allowed against a burst of %d", allowed, burst)
	}
}

// The token is derived from the session and the deployment key. Neither half
// alone mints a token for another session.
func TestTheCSRFTokenBindsTheSessionAndTheKey(t *testing.T) {
	key := []byte("deployment key material")
	other := []byte("a different deployment")

	a := CSRFToken(key, "session-a")
	b := CSRFToken(key, "session-b")
	if a == b {
		t.Fatal("two sessions derived the same token")
	}
	if CSRFToken(other, "session-a") == a {
		t.Fatal("a different key derived the same token")
	}
	if CSRFToken(key, "session-a") != a {
		t.Fatal("the derivation is not deterministic")
	}
}

// The comparison accepts the right token and refuses everything else.
func TestCSRFValidation(t *testing.T) {
	key := []byte("deployment key material")
	good := CSRFToken(key, "session-a")

	if !CSRFValid(key, "session-a", good) {
		t.Fatal("the derived token was refused")
	}
	if !CSRFValid(key, "session-a", "  "+good+" ") {
		t.Fatal("surrounding whitespace refused a valid token")
	}
	for _, c := range []struct{ what, presented string }{
		{"an empty token", ""},
		{"a truncated token", good[:len(good)-1]},
		{"a token with one character changed", flipLast(good)},
		{"another session's token", CSRFToken(key, "session-b")},
	} {
		if CSRFValid(key, "session-a", c.presented) {
			t.Errorf("%s was accepted", c.what)
		}
	}
}

func flipLast(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	if last == '0' {
		return s[:len(s)-1] + "1"
	}
	return s[:len(s)-1] + "0"
}

// Only ambient authority is asked for a token.
func TestOnlyAmbientAuthorityNeedsAToken(t *testing.T) {
	for _, c := range []struct {
		what   string
		method string
		kind   CredentialKind
		want   bool
	}{
		{"a cookie mutation", "POST", CredentialSessionCookie, true},
		{"a cookie delete", "DELETE", CredentialSessionCookie, true},
		{"a cookie read", "GET", CredentialSessionCookie, false},
		{"a cookie options", "OPTIONS", CredentialSessionCookie, false},
		{"a basic app password mutation", "POST", CredentialBasicApp, false},
		{"a bearer app password mutation", "POST", CredentialBearerApp, false},
		{"an unauthenticated mutation", "POST", CredentialNone, false},
	} {
		if got := CSRFRequired(c.method, c.kind); got != c.want {
			t.Errorf("%s: CSRFRequired = %v, want %v", c.what, got, c.want)
		}
	}
}
