//go:build linux

package app

import (
	"math"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
)

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
