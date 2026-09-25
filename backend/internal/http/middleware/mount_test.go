// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

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
	} else if rec.Passed {
		r.passed = append(r.passed, rec.Step)
	}
}

func (r *replay) snapshot() ([]Step, []Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.entered...), append([]Step(nil), r.passed...)
}

func sessionCookie() *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: "abcd", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

type harness struct {
	app *gin.Engine
	rec *replay
	lim *Limiter
}

func newHarness(t *testing.T, hosts Hosts) *harness {
	t.Helper()
	h := &harness{app: gin.New(), rec: &replay{}}
	h.lim = NewLimiter(newStepClock(), 1000, 1000)
	if err := Mount(h.app, Chain(), Deps{
		Hosts:   func() Hosts { return hosts },
		Trusted: func() []netip.Prefix { return []netip.Prefix{mustPrefix(t, "10.0.0.0/8")} },
		Limiter: h.lim,
	}, h.rec); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	h.app.Any("/*path", func(c *gin.Context) { c.String(http.StatusOK, "handled") })
	return h
}

type answer struct {
	status int
	close  bool
}

func send(t *testing.T, app *gin.Engine, req *http.Request) answer {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return answer{status: rec.Code, close: rec.Header().Get("Connection") == "close"}
}

func (h *harness) do(t *testing.T, req *http.Request) answer { return send(t, h.app, req) }

func TestARequestWalksTheChainInOrder(t *testing.T) {
	h := newHarness(t, namedHosts())
	if got := h.do(t, httptest.NewRequest("GET", "http://app.example.test/anything", nil)).status; got != http.StatusOK {
		t.Fatalf("request answered %d", got)
	}
	entered, _ := h.rec.snapshot()
	want := Chain()
	if len(entered) != len(want) {
		t.Fatalf("entered %d steps, want %d: %v", len(entered), len(want), entered)
	}
	for i := range want {
		if entered[i] != want[i] {
			t.Fatalf("at %d entered %v, want %v", i, entered[i], want[i])
		}
	}
}

func TestARefusedHostStopsTheWalkAtTheBoundary(t *testing.T) {
	h := newHarness(t, namedHosts())
	got := h.do(t, httptest.NewRequest("GET", "http://evil.example.test/anything", nil))
	if got.status != http.StatusMisdirectedRequest || !got.close {
		t.Fatalf("refusal was %+v, want 421 and connection close", got)
	}
	entered, _ := h.rec.snapshot()
	for _, s := range entered {
		if s == StepAuth || s == StepRateLimit {
			t.Fatalf("%v ran after boundary refusal: %v", s, entered)
		}
	}
	if entered[len(entered)-1] != StepHostAndOriginBoundary {
		t.Fatalf("walk stopped at %v", entered[len(entered)-1])
	}
}

func TestTheLimiterKeysOnTheResolvedClientNotTheHeader(t *testing.T) {
	h := newHarness(t, namedHosts())
	h.lim.SetLimits(1, 1)
	req := func(forwarded string) int {
		r := httptest.NewRequest("GET", "http://app.example.test/anything", nil)
		r.Header.Set("X-Forwarded-For", forwarded)
		return h.do(t, r).status
	}
	if req("198.51.100.4") != http.StatusOK || req("198.51.100.4") != http.StatusTooManyRequests || req("198.51.100.9") != http.StatusTooManyRequests {
		t.Fatal("untrusted forwarding header changed the limiter bucket")
	}
}

func TestATrustedPeersClientsAreThrottledApart(t *testing.T) {
	lim := NewLimiter(newStepClock(), 1, 1)
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	allow := func(forwarded string) bool { return lim.Allow(ClientAddr(peer, trusted, "", forwarded).String()) }
	if !allow("198.51.100.4") || allow("198.51.100.4") || !allow("198.51.100.9") {
		t.Fatal("trusted peer clients did not receive separate buckets")
	}
}

func TestTheBoundaryRuleAppliesThroughTheChain(t *testing.T) {
	h := newHarness(t, namedHosts())
	r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	r.AddCookie(sessionCookie())
	if h.do(t, r).status != http.StatusMisdirectedRequest {
		t.Fatal("cookie mutation without Origin was admitted")
	}
	r = httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	r.AddCookie(sessionCookie())
	r.Header.Set("Origin", "http://app.example.test")
	if h.do(t, r).status != http.StatusOK {
		t.Fatal("matching Origin was refused")
	}
}

func TestBrowserAuthenticationIsBoundThroughTheChain(t *testing.T) {
	h := newHarness(t, namedHosts())
	for _, origin := range []string{"", "https://evil.example.test"} {
		r := httptest.NewRequest("POST", "http://app.example.test/api/v1/auth/login", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if h.do(t, r).status != http.StatusMisdirectedRequest {
			t.Fatalf("origin %q was admitted", origin)
		}
	}
	r := httptest.NewRequest("POST", "http://app.example.test/api/v1/auth/login", nil)
	r.Header.Set("Origin", "http://app.example.test")
	if h.do(t, r).status != http.StatusOK {
		t.Fatal("app Origin was refused")
	}
}

func TestMountRefusesAMisassembledChain(t *testing.T) {
	app := gin.New()
	deps := Deps{Hosts: func() Hosts { return namedHosts() }, Trusted: func() []netip.Prefix { return nil }, Limiter: NewLimiter(newStepClock(), 1, 1)}
	if err := Mount(app, []Step{StepAuth, StepCSRF}, deps, nil); err == nil {
		t.Error("invalid chain mounted")
	}
	if err := Mount(app, nil, deps, nil); err == nil {
		t.Error("empty chain mounted")
	}
	if err := Mount(app, Chain(), Deps{}, nil); err == nil {
		t.Error("chain without dependencies mounted")
	}
}

func TestHostsAreReadPerRequest(t *testing.T) {
	var mu sync.Mutex
	hosts := Hosts{App: []string{"first.example.test"}}
	app := gin.New()
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { mu.Lock(); defer mu.Unlock(); return hosts },
		Trusted: func() []netip.Prefix { return nil }, Limiter: NewLimiter(newStepClock(), 1000, 1000),
	}, nil); err != nil {
		t.Fatal(err)
	}
	app.Any("/*path", func(c *gin.Context) { c.String(http.StatusOK, "handled") })
	request := func(host string) int {
		return send(t, app, httptest.NewRequest("GET", "http://"+host+"/x", nil)).status
	}
	if request("second.example.test") != http.StatusMisdirectedRequest {
		t.Fatal("old host accepted")
	}
	mu.Lock()
	hosts = Hosts{App: []string{"second.example.test"}}
	mu.Unlock()
	if request("second.example.test") != http.StatusOK {
		t.Fatal("updated host refused")
	}
}
