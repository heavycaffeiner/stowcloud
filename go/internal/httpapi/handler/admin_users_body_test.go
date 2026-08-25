//go:build linux

package handler

import (
	"encoding/json"
	"testing"
)

// A quota cleared back to unlimited arrives as an explicit null, and leaving
// the cap alone omits the key. One pointer cannot say both, so which keys the
// body carried is read separately: both decoded to nil, and the interface
// could set a cap and never remove one.
func TestClearingAQuotaIsToldApartFromNotMentioningIt(t *testing.T) {
	var patch struct {
		QuotaBytes *int64 `json:"quota_bytes"`
	}

	for _, tc := range []struct {
		name          string
		body          string
		wantPresent   bool
		wantUnlimited bool
	}{
		{name: "clearing", body: `{"quota_bytes":null}`, wantPresent: true, wantUnlimited: true},
		{name: "leaving alone", body: `{"disabled":true}`, wantPresent: false},
		{name: "setting", body: `{"quota_bytes":1024}`, wantPresent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch.QuotaBytes = nil
			if err := json.Unmarshal([]byte(tc.body), &patch); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			var present map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &present); err != nil {
				t.Fatalf("decoding the key set: %v", err)
			}
			_, ok := present["quota_bytes"]
			if ok != tc.wantPresent {
				t.Fatalf("the key was seen as present=%v, want %v", ok, tc.wantPresent)
			}
			if tc.wantUnlimited && patch.QuotaBytes != nil {
				t.Fatalf("an explicit null decoded as %d, want unlimited", *patch.QuotaBytes)
			}
		})
	}
}
