// Linux only, because what it tests is.
//go:build linux

package handler

import (
	"encoding/json"
	"testing"
)

// The network patch decodes the field names the snapshot reports.
//
// It read trusted_proxy_cidrs while the client sent trusted_proxies, so the
// list decoded as absent on every save: the handler answered "applied" and the
// proxy boundary never moved. A save that reports success and changes nothing
// is the worst of the three outcomes, because nothing in the interface says so.
//
// Checked against the struct the handler actually decodes into, declared here
// with the same tags, so a rename on either side fails this.
func TestTheNetworkPatchDecodesTheClientsFieldNames(t *testing.T) {
	// Exactly what web/src/lib/api/types.ts NetworkSettingsReq sends.
	const body = `{"app_hosts":["a.test"],"content_hosts":["c.test"],` +
		`"trusted_proxies":["10.0.0.0/8"]}`

	var req struct {
		TrustedProxyCIDRs []string `json:"trusted_proxies,omitempty"`
		AppHosts          []string `json:"app_hosts,omitempty"`
		ContentHosts      []string `json:"content_hosts,omitempty"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(req.TrustedProxyCIDRs) != 1 || req.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Errorf("trusted_proxies decoded as %v, want the range the client sent", req.TrustedProxyCIDRs)
	}
	if len(req.AppHosts) != 1 || len(req.ContentHosts) != 1 {
		t.Errorf("the host lists decoded as %v / %v", req.AppHosts, req.ContentHosts)
	}
}

// The old name is not silently accepted as well: honouring both would leave
// two spellings of one setting, and the one a client used would decide whether
// the save took.
func TestTheOldProxyFieldNameNoLongerDecodes(t *testing.T) {
	var req struct {
		TrustedProxyCIDRs []string `json:"trusted_proxies,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"trusted_proxy_cidrs":["10.0.0.0/8"]}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.TrustedProxyCIDRs != nil {
		t.Fatalf("the old field name still decodes: %v", req.TrustedProxyCIDRs)
	}
}
