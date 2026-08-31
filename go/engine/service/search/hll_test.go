package search

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// The sketch is what makes the estimate affordable, and an administrator is
// being asked to trust its arithmetic, so the accuracy claim is measured
// rather than asserted.
func TestHLLEstimatesKnownCardinalitiesWithinTolerance(t *testing.T) {
	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		h := NewHLL(HLLDefaultPrecision)
		for i := range n {
			h.Add([]byte(fmt.Sprintf("item-%d", i)))
		}
		got := float64(h.EstimateUint())
		want := float64(n)
		// Roughly 0.8 percent standard error at this precision. Five percent
		// is a wide band around that, chosen so the test measures the
		// estimator rather than one seed's luck.
		if rel := math.Abs(got-want) / want; rel > 0.05 {
			t.Errorf("%d distinct items estimated as %.0f, off by %.1f%%", n, got, rel*100)
		}
	}
}

// Few distinct values is where the raw estimator is worst and linear counting
// takes over, so an exact small count is the case worth pinning.
func TestHLLIsAccurateOnASmallSet(t *testing.T) {
	h := NewHLL(HLLDefaultPrecision)
	for i := range 10 {
		h.Add([]byte{byte(i)})
	}
	if got := h.EstimateUint(); got < 9 || got > 11 {
		t.Errorf("10 distinct items estimated as %d", got)
	}
	if empty := NewHLL(HLLDefaultPrecision).EstimateUint(); empty != 0 {
		t.Errorf("an empty sketch estimated %d, want 0", empty)
	}
}

// Adding the same value repeatedly must not move the estimate: that is what
// distinguishes a distinct count from a total.
func TestHLLCountsDistinctValuesOnly(t *testing.T) {
	h := NewHLL(HLLDefaultPrecision)
	for range 1000 {
		h.Add([]byte("the same trigram"))
	}
	if got := h.EstimateUint(); got != 1 {
		t.Errorf("one distinct value added a thousand times estimated as %d", got)
	}
}

// The clamp is sound only because every caller passes a compiled-in constant.
// These are its bounds; if precision ever becomes configurable, this test is
// where the refusal replaces the clamp.
func TestHLLPrecisionClampBounds(t *testing.T) {
	cases := []struct{ in, want uint8 }{
		{0, hllMinPrecision},
		{1, hllMinPrecision},
		{hllMinPrecision, hllMinPrecision},
		{14, 14},
		{hllMaxPrecision, hllMaxPrecision},
		{255, hllMaxPrecision},
	}
	for _, c := range cases {
		h := NewHLL(c.in)
		if h.Precision() != c.want {
			t.Errorf("NewHLL(%d) has precision %d, want %d", c.in, h.Precision(), c.want)
		}
		if want := 1 << c.want; h.MemoryBytes() != want {
			t.Errorf("NewHLL(%d) took %d bytes, want %d", c.in, h.MemoryBytes(), want)
		}
	}
}

func TestHLLMergeUnionsAndRefusesAMismatch(t *testing.T) {
	a, b := NewHLL(12), NewHLL(12)
	for i := range 500 {
		a.Add([]byte(fmt.Sprintf("a-%d", i)))
		b.Add([]byte(fmt.Sprintf("b-%d", i)))
	}
	if err := a.Merge(b); err != nil {
		t.Fatalf("merging equal precisions: %v", err)
	}
	got := float64(a.EstimateUint())
	if rel := math.Abs(got-1000) / 1000; rel > 0.08 {
		t.Errorf("the union of two 500-item sketches estimated %.0f", got)
	}

	if err := NewHLL(12).Merge(NewHLL(13)); !errors.Is(err, ErrHLLPrecision) {
		t.Errorf("merging different precisions = %v, want ErrHLLPrecision", err)
	}
}

// Merging a sketch into itself is idempotent: the registers are maxima, so a
// union with the same maxima changes nothing.
func TestHLLMergeIsIdempotent(t *testing.T) {
	h := NewHLL(12)
	for i := range 200 {
		h.Add([]byte(fmt.Sprintf("x-%d", i)))
	}
	before := h.EstimateUint()
	if err := h.Merge(h); err != nil {
		t.Fatalf("self merge: %v", err)
	}
	if after := h.EstimateUint(); after != before {
		t.Errorf("self merge moved the estimate from %d to %d", before, after)
	}
}

// Hash64 is FNV-1a plus a splitmix64 finaliser because FNV alone avalanches
// poorly in the high bits, which is exactly where the register index is read
// from. Two inputs differing in one bit should land in different registers far
// more often than not.
func TestHash64AvalanchesInTheHighBits(t *testing.T) {
	const p = 14
	collisions := 0
	const trials = 512
	for i := range trials {
		a := Hash64([]byte{byte(i), 0x00})
		b := Hash64([]byte{byte(i), 0x01})
		if a>>(64-p) == b>>(64-p) {
			collisions++
		}
	}
	// With 2^14 registers, chance alone gives about one collision in 16384.
	if collisions > trials/20 {
		t.Errorf("%d of %d one-bit-apart inputs shared a register index", collisions, trials)
	}
}

func TestHash64IsDeterministic(t *testing.T) {
	first, second := Hash64([]byte("abc")), Hash64([]byte("abc"))
	if first != second {
		t.Error("the same input hashed differently")
	}
	if Hash64([]byte("abc")) == Hash64([]byte("abd")) {
		t.Error("two different inputs collided on a 64-bit hash")
	}
}
