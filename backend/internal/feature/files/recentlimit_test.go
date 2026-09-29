//go:build linux

package core

import (
	"math"
	"testing"
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
		got := RecentLimitOf(n)
		if got <= 0 {
			t.Errorf("limit %d produced %d", n, got)
		}
		if got > RecentMaxLimit {
			t.Errorf("limit %d produced %d, past the ceiling of %d",
				n, got, RecentMaxLimit)
		}
	}
	if got := RecentLimitOf(7); got != 7 {
		t.Errorf("an explicit limit of 7 produced %d", got)
	}
	if got := RecentLimitOf(0); got != RecentLimit {
		t.Errorf("an absent limit produced %d, want the default %d", got, RecentLimit)
	}
}
