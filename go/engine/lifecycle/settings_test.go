//go:build linux

package lifecycle_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// A saved host list reaches the running server.
//
// The chain reads the host lists per request. Nothing loaded them from the
// stored document, so every deployment ran in first-boot mode however it was
// configured: an operator who named their host found the setting ignored, and
// the only reason it was not a hole is that first boot admits private clients
// only.
func TestASavedHostListIsEnforced(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	// Saved before the engine that serves it is opened, which is the ordinary
	// order: an operator configures, the server restarts, the value applies.
	if merr := e.State.MergeSettings(ctx, "network", map[string]any{
		"app_hosts": []any{"files.example"},
	}); merr != nil {
		t.Fatalf("saving the host: %v", merr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// A second engine over the same directory, so the value is read from the
	// document rather than from anything left in memory.
	reopened, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, reopened)

	// The declared host is served.
	if status := hostRequest(t, base, "files.example"); status != http.StatusOK {
		t.Errorf("the declared host answered %d", status)
	}

	// One that was never declared is refused. Under first boot this would be
	// admitted, because first boot names no host at all.
	if status := hostRequest(t, base, "not.declared"); status == http.StatusOK {
		t.Error("a host the deployment does not serve was admitted, so the saved list is not being read")
	}
}

// hostRequest asks for health under a given Host header.
func hostRequest(t *testing.T, base, host string) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/system/health", nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = host

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	return resp.StatusCode
}

// A saved proxy range decides whether a forwarded address is believed.
//
// The chain resolves the client through this set. A range that was not saved
// must not be trusted, or any caller could name whatever address they liked
// and be rate-limited, logged and admitted as that address instead.
func TestASavedProxyRangeDecidesWhoIsBelieved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	// The loopback is the peer these tests connect from, so trusting it is
	// what makes a forwarded header believable at all. One entry is written
	// as a bare address and one as a range, because operators write both and
	// refusing either would silently drop a proxy they configured.
	if merr := e.State.MergeSettings(ctx, "network", map[string]any{
		"trusted_proxies": []any{"127.0.0.1", "10.0.0.0/8", "not-an-address"},
	}); merr != nil {
		t.Fatalf("saving the ranges: %v", merr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	reopened, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, reopened)

	// The unparseable entry must not have stopped the boot, and the two good
	// ones must have survived it: the server answers.
	if status := hostRequest(t, base, ""); status != http.StatusOK {
		t.Fatalf("the server answered %d after an unparseable proxy range", status)
	}

	// The loopback is trusted, so the forwarded header is now believed and
	// the address it names is what the guard sees. A public one is therefore
	// refused by the door, which admits private clients only.
	//
	// This is the direction the setting controls, and it is the one worth
	// asserting: trusting a range decides whose claim about an address is
	// taken, and taking a claim can only ever admit less than the peer would.
	public, _ := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "203.0.113.7")
	if public == http.StatusOK {
		t.Error("a public forwarded address was admitted through a trusted proxy, so the header is not being believed")
	}

	// A private one that is not itself a trusted proxy is the real client
	// behind that proxy, and it is admitted. 192.168.0.7 is private and
	// outside both saved ranges, so the walk stops there and returns it.
	behind, _ := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "192.168.0.7")
	if behind != http.StatusOK {
		t.Errorf("a private client behind a trusted proxy answered %d", behind)
	}
}

// An entry that is not an address is dropped, not turned into one.
//
// A parse failure that fell through to a zero address would add 0.0.0.0/0 to
// the trusted set, which trusts every proxy on the internet: any caller could
// then name whatever address they liked and have it believed.
func TestAnUnparseableProxyRangeIsDropped(t *testing.T) {
	trusted := lifecycle.ParsePrefixesForTest([]string{
		"127.0.0.1", "10.0.0.0/8", "not-an-address", "", "999.999.999.999", "10.0.0.0/99",
	})

	if len(trusted) != 2 {
		t.Fatalf("%d ranges survived, want the 2 that parse: %v", len(trusted), trusted)
	}
	for _, p := range trusted {
		if p.Bits() == 0 {
			t.Errorf("%v trusts everything, so a bad entry became a wildcard", p)
		}
		if !p.Addr().IsValid() {
			t.Errorf("%v holds no address", p)
		}
	}

	// The bare address became a single-address range rather than being
	// dropped, because that is how an operator writes one proxy.
	var sawSingle bool
	for _, p := range trusted {
		if p.Addr().String() == "127.0.0.1" && p.Bits() == p.Addr().BitLen() {
			sawSingle = true
		}
	}
	if !sawSingle {
		t.Errorf("the bare address was not kept as its own range: %v", trusted)
	}
}

// The stored rate limit is what the running server applies.
//
// The limiter is built with a placeholder before the settings are read, so a
// load that did not reach it would leave the deployment running at a rate no
// operator chose. The two numbers differ on purpose, which is what makes this
// observable.
func TestTheStoredRateLimitIsApplied(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	// A burst small enough that a short loop crosses it, and far below the
	// placeholder the limiter is constructed with.
	if merr := e.State.MergeSettings(ctx, "rate", map[string]any{
		"per_sec": 1, "burst": 3,
	}); merr != nil {
		t.Fatalf("saving the limits: %v", merr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	reopened, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, reopened)

	// Twenty requests against a burst of three. Under the placeholder burst
	// of 200 every one is served, so a throttle here is the stored value
	// taking effect.
	var served, throttled int
	for i := 0; i < 20; i++ {
		switch hostRequest(t, base, "") {
		case http.StatusOK:
			served++
		case http.StatusTooManyRequests:
			throttled++
		}
	}

	if throttled == 0 {
		t.Errorf("all %d requests were served against a stored burst of 3, so the limit was not applied", served)
	}
}
