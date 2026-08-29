//go:build linux

// Reading the operator's settings into the running server.
//
// The chain reads the host lists, the trusted proxy ranges and the rate
// limits per request, so they have to be loaded from the stored document at
// boot rather than left at their zero values. They were: nothing assigned
// them, so a deployment with configured hosts ran permanently in first-boot
// mode, admitting only private clients and ignoring what the operator had
// saved.
package lifecycle

import (
	"context"
	"io"
	"log/slog"
	"net/netip"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// loadSettings reads the stored document over the compiled-in defaults and
// applies what the chain consults live.
//
// A document that cannot be read is a warning rather than a failure: the
// defaults are a working deployment, and refusing to boot over an unreadable
// settings row would take a server down for a value it could run without.
func (e *Engine) loadSettings(ctx context.Context) {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)

	e.settingsMu.Lock()
	e.appHosts = middleware.Hosts{
		App:     values.AppHosts,
		Content: values.ContentHosts,
	}
	e.trusted = parsePrefixes(values.TrustedProxy, e)
	e.settingsMu.Unlock()

	// The search bounds an administrator adjusts. Applied through the
	// service's setter, which leaves the concurrency gate alone: it is a
	// buffered channel established at construction, and swapping it while
	// queries are in flight would lose the slots they hold.
	if e.Search != nil {
		concurrency, deadline := values.SearchConcurrentSSD, values.SearchDeadlineSSD
		e.Search.SetBounds(concurrency, deadline)
	}

	// The limiter's own setter rather than a fresh limiter: it holds a bucket
	// per client, and replacing it would discard every one of them, handing a
	// full burst to whoever was being throttled at that moment.
	e.limiter.SetLimits(values.RatePerSec, float64(values.RateBurst))
}

// parsePrefixes reads the operator's proxy list into the form the client
// resolver compares against.
//
// An entry that does not parse is dropped with a warning rather than failing
// the boot. Dropping is the safe direction: a range that is not trusted means
// a forwarded header from it is ignored, which refuses more than it admits.
func parsePrefixes(raw []string, e *Engine) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			// A bare address is the ordinary way an operator writes a single
			// proxy, so it is accepted as its own single-address range rather
			// than refused for missing a mask.
			addr, aerr := netip.ParseAddr(entry)
			if aerr != nil {
				e.logger.Warn("a trusted proxy range was not understood and is ignored",
					"entry", entry, "error", err)
				continue
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p)
	}
	return out
}

// ParsePrefixesForTest exposes the proxy-range parse.
//
// Exported for a test because the failure worth pinning is a wildcard: an
// entry that does not parse must be dropped rather than become 0.0.0.0/0,
// which would trust every proxy on the internet. Driving that through the
// server would need a request from an address the test cannot bind.
func ParsePrefixesForTest(raw []string) []netip.Prefix {
	return parsePrefixes(raw, &Engine{logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}
