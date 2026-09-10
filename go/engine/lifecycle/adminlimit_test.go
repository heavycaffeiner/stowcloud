//go:build linux

package lifecycle

import (
	"math"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The audit page bound holds for every input a caller can send.
//
// Checked here rather than through a response: proving a ceiling of 1000 by
// counting rows needs more than 1000 audit events, and a fixture that writes
// them spends seconds doing it while testing the same arithmetic. The handler
// test covers that the parameter reaches this function at all.
func TestTheAuditLimitIsAlwaysBounded(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"", "0", "-1", "-999999", "abc", "1", "99", "100", "1000", "1001",
		"999999999", "9223372036854775807", "99999999999999999999",
	} {
		got := auditLimit(raw)
		if got <= 0 {
			t.Errorf("limit %q produced %d, which asks for nothing or for a negative page", raw, got)
		}
		if got > auditPageCeiling {
			t.Errorf("limit %q produced %d, past the ceiling of %d", raw, got, auditPageCeiling)
		}
	}

	// A usable number is honoured rather than replaced by the default, which
	// is what makes the parameter worth having.
	if got := auditLimit("7"); got != 7 {
		t.Errorf("an explicit limit of 7 produced %d", got)
	}
	if got := auditLimit(""); got != auditPageDefault {
		t.Errorf("an absent limit produced %d, want the default %d", got, auditPageDefault)
	}
}

// The same for the recent listing, whose ceiling has the same job.
//
// The bound lives in the core now, because three surfaces answer this listing
// and a ceiling enforced by only one of them is not a ceiling.
func TestTheRecentLimitIsAlwaysBounded(t *testing.T) {
	t.Parallel()
	for _, n := range []int{
		0, -1, 1, 500, 501, 999999999, math.MaxInt32, math.MaxInt,
	} {
		got := core.RecentLimitOf(n)
		if got <= 0 {
			t.Errorf("limit %d produced %d", n, got)
		}
		if got > core.RecentMaxLimit {
			t.Errorf("limit %d produced %d, past the ceiling of %d",
				n, got, core.RecentMaxLimit)
		}
	}
	if got := core.RecentLimitOf(7); got != 7 {
		t.Errorf("an explicit limit of 7 produced %d", got)
	}
	if got := core.RecentLimitOf(0); got != core.RecentLimit {
		t.Errorf("an absent limit produced %d, want the default %d", got, core.RecentLimit)
	}
}
