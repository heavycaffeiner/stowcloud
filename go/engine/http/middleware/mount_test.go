// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// replay records the steps a request actually entered, in order.
type replay struct {
	mu      sync.Mutex
	entered []Step
	passed  []Step
}

func (r *replay) Record(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Entered {
		r.entered = append(r.entered, rec.Step)
		return
	}
	if rec.Passed {
		r.passed = append(r.passed, rec.Step)
	}
}

func (r *replay) snapshot() ([]Step, []Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.entered...), append([]Step(nil), r.passed...)
}

// sessionCookie is the cookie as the browser receives it. The __Host- prefix
// requires Secure and no Domain, so a test cookie without those attributes
// would not be the one that ships.
func sessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "abcd",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

type harness struct {
	app *fiber.App
	rec *replay
	lim *Limiter
	clk *stepClock
}

func newHarness(t *testing.T, hosts Hosts) *harness {
	t.Helper()
	clk := newStepClock()
	h := &harness{
		app: fiber.New(fiber.Config{DisableStartupMessage: true}),
		rec: &replay{},
		lim: NewLimiter(clk, 1000, 1000),
		clk: clk,
	}
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}

	if err := Mount(h.app, Chain(), Deps{
		Hosts:   func() Hosts { return hosts },
		Trusted: func() []netip.Prefix { return trusted },
		Limiter: h.lim,
	}, h.rec); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	h.app.All("/*", func(c *fiber.Ctx) error { return c.SendString("handled") })
	return h
}

// answer is what a caller here reads: no test in this file reads a body, and
// returning the response instead would leave an unclosed one at every call
// site for the linter to object to, correctly.
type answer struct {
	status int
	close  bool
}

func (h *harness) do(t *testing.T, req *http.Request) answer {
	t.Helper()
	return send(t, h.app, req)
}

func send(t *testing.T, app *fiber.App, req *http.Request) answer {
	t.Helper()
	req.RequestURI = ""
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() {
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing the response body: %v", cerr)
		}
	}()
	return answer{status: res.StatusCode, close: res.Close}
}

// The request walks the chain in the table's order. This is the replay the
// design asks for: a reordering shows up here as a failure rather than as a
// subtly different response somewhere else.
func TestARequestWalksTheChainInOrder(t *testing.T) {
	h := newHarness(t, namedHosts())

	req := httptest.NewRequest("GET", "http://app.example.test/anything", nil)
	if got := h.do(t, req); got.status != fiber.StatusOK {
		t.Fatalf("the request answered %d", got.status)
	}

	entered, _ := h.rec.snapshot()
	want := Chain()
	if len(entered) != len(want) {
		t.Fatalf("entered %d steps, want %d: %v", len(entered), len(want), entered)
	}
	for i := range want {
		if entered[i] != want[i] {
			t.Fatalf("at position %d the request entered %v, want %v\n  full: %v",
				i, entered[i], want[i], entered)
		}
	}
}

// A refused host stops the walk at the boundary, and nothing after it runs.
// This is what makes the order load-bearing rather than decorative.
func TestARefusedHostStopsTheWalkAtTheBoundary(t *testing.T) {
	h := newHarness(t, namedHosts())

	req := httptest.NewRequest("GET", "http://evil.example.test/anything", nil)
	got := h.do(t, req)
	if got.status != fiber.StatusMisdirectedRequest {
		t.Fatalf("a foreign host answered %d, want 421", got.status)
	}
	// The connection-close flag rather than a Connection header: the error
	// handler rewrites the response, and what survives into the wire format is
	// the flag. The flag is what a client acts on.
	if !got.close {
		t.Error("the refusal did not ask the connection to close")
	}

	entered, _ := h.rec.snapshot()
	for _, s := range entered {
		if s == StepAuth || s == StepRateLimit {
			t.Fatalf("%v ran after the boundary refused: %v", s, entered)
		}
	}
	// It did reach the boundary, so the refusal came from there rather than
	// from the framework rejecting the request first.
	last := entered[len(entered)-1]
	if last != StepHostAndOriginBoundary {
		t.Fatalf("the walk stopped at %v, want the boundary: %v", last, entered)
	}
}

// The limiter keys on what TrustedProxy resolved, and under this harness that
// is the untrusted-peer case: fiber's in-process test transport reports the
// peer as 0.0.0.0, which no trusted prefix contains, so the forwarding header
// is correctly ignored and every request shares one bucket.
//
// That is the rule working rather than failing, and it is worth pinning: a
// change that started believing headers from an untrusted peer would show up
// here as the second client suddenly getting its own budget.
func TestTheLimiterKeysOnTheResolvedClientNotTheHeader(t *testing.T) {
	h := newHarness(t, namedHosts())
	h.lim.SetLimits(1, 1)

	send := func(forwarded string) int {
		req := httptest.NewRequest("GET", "http://app.example.test/anything", nil)
		req.Header.Set("X-Forwarded-For", forwarded)
		return h.do(t, req).status
	}

	if got := send("198.51.100.4"); got != fiber.StatusOK {
		t.Fatalf("the first request answered %d", got)
	}
	if got := send("198.51.100.4"); got != fiber.StatusTooManyRequests {
		t.Fatalf("the second request answered %d, want 429", got)
	}
	// A different claimed address does not buy a fresh budget, because the
	// claim came from a peer with no standing to make it.
	if got := send("198.51.100.9"); got != fiber.StatusTooManyRequests {
		t.Fatalf("a different claimed address answered %d, so the header was believed", got)
	}
}

// With a trusted peer the header is honoured and two clients behind one proxy
// are throttled apart. The peer is set directly, since the test transport
// cannot dial from a chosen address.
func TestATrustedPeersClientsAreThrottledApart(t *testing.T) {
	lim := NewLimiter(newStepClock(), 1, 1)
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	allow := func(forwarded string) bool {
		client := ClientAddr(peer, trusted, "", forwarded)
		return lim.Allow(client.String())
	}

	if !allow("198.51.100.4") {
		t.Fatal("the first client was refused")
	}
	if allow("198.51.100.4") {
		t.Fatal("the first client spent twice")
	}
	if !allow("198.51.100.9") {
		t.Fatal("a second client behind the same proxy shared the first one's bucket")
	}
}

// A mutating cookie request without an Origin is refused by the boundary, and
// with a matching one it is admitted. The chain wires the rule the boundary
// test already proved, so this checks the wiring rather than the rule.
func TestTheBoundaryRuleAppliesThroughTheChain(t *testing.T) {
	h := newHarness(t, namedHosts())

	req := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	req.AddCookie(sessionCookie())
	if got := h.do(t, req).status; got != fiber.StatusMisdirectedRequest {
		t.Fatalf("a cookie mutation with no Origin answered %d, want 421", got)
	}

	req = httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	req.AddCookie(sessionCookie())
	req.Header.Set("Origin", "https://app.example.test")
	if got := h.do(t, req).status; got != fiber.StatusOK {
		t.Fatalf("a cookie mutation with a matching Origin answered %d", got)
	}
}

// Mount refuses a chain that is not one, so a misassembly is a startup error
// rather than a server that silently omits a step.
func TestMountRefusesAMisassembledChain(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	deps := Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1, 1),
	}

	if err := Mount(app, []Step{StepAuth, StepCSRF}, deps, nil); err == nil {
		t.Error("a chain with no ErrorMapper was mounted")
	}
	if err := Mount(app, nil, deps, nil); err == nil {
		t.Error("an empty chain was mounted")
	}
	if err := Mount(app, Chain(), Deps{}, nil); err == nil {
		t.Error("a chain with no dependencies was mounted")
	}
}

// Hosts are read per request, so a settings change takes effect on the next
// one without a listener swap.
func TestHostsAreReadPerRequest(t *testing.T) {
	var mu sync.Mutex
	hosts := Hosts{App: []string{"first.example.test"}}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts: func() Hosts {
			mu.Lock()
			defer mu.Unlock()
			return hosts
		},
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("handled") })

	request := func(host string) int {
		return send(t, app, httptest.NewRequest("GET", "http://"+host+"/x", nil)).status
	}

	if got := request("second.example.test"); got != fiber.StatusMisdirectedRequest {
		t.Fatalf("the unnamed host answered %d", got)
	}
	mu.Lock()
	hosts = Hosts{App: []string{"second.example.test"}}
	mu.Unlock()
	if got := request("second.example.test"); got != fiber.StatusOK {
		t.Fatalf("after the change the host answered %d", got)
	}
}
