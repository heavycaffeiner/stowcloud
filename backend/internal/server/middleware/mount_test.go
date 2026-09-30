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

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

func sessionCookie() *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: "abcd", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

type harness struct {
	app *gin.Engine
	lim *Limiter
}

func newHarness(t *testing.T, hosts Hosts) *harness {
	t.Helper()
	h := &harness{app: gin.New(), lim: NewLimiter(newStepClock(), 1000, 1000)}
	d := Deps{
		Hosts:   func() Hosts { return hosts },
		Trusted: func() []netip.Prefix { return []netip.Prefix{mustPrefix(t, "10.0.0.0/8")} },
		Limiter: h.lim,
		Errors:  apierr.NewClassifier(nil),
	}
	global, err := Global(d)
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	h.app.Use(global...)
	h.app.Group("", Device(d)...).Any("/*path", func(c *gin.Context) { c.String(http.StatusOK, "handled") })
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

func TestARefusedHostStopsBeforeTheLimiter(t *testing.T) {
	h := newHarness(t, namedHosts())
	h.lim.SetLimits(1, 1)
	got := h.do(t, httptest.NewRequest("GET", "http://evil.example.test/anything", nil))
	if got.status != http.StatusMisdirectedRequest || !got.close {
		t.Fatalf("refusal was %+v, want 421 and connection close", got)
	}
	if got := h.do(t, httptest.NewRequest("GET", "http://app.example.test/anything", nil)).status; got != http.StatusOK {
		t.Fatalf("a refused host spent the budget: the next request answered %d", got)
	}
}

func TestARefusalCarriesTheTraceHeader(t *testing.T) {
	h := newHarness(t, namedHosts())
	rec := httptest.NewRecorder()
	h.app.ServeHTTP(rec, httptest.NewRequest("GET", "http://evil.example.test/anything", nil))
	if rec.Header().Get(TraceHeader) == "" {
		t.Fatal("a boundary refusal went out without a trace id")
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

func TestGlobalRefusesMissingDependencies(t *testing.T) {
	if _, err := Global(Deps{}); err == nil {
		t.Error("a chain without dependencies was built")
	}
}

func TestHostsAreReadPerRequest(t *testing.T) {
	var mu sync.Mutex
	hosts := Hosts{App: []string{"first.example.test"}}
	app := gin.New()
	global, err := Global(Deps{
		Hosts:   func() Hosts { mu.Lock(); defer mu.Unlock(); return hosts },
		Trusted: func() []netip.Prefix { return nil }, Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Errors: apierr.NewClassifier(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Use(global...)
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
