package search

import (
	"errors"
	"math"
	"math/bits"
)

// A small HyperLogLog.
//
// The estimator needs the distinct-trigram count to size the posting
// dictionary, and that is the number separating a CJK corpus from a Latin one.
// Counting exactly would mean holding millions of trigrams in a set during the
// estimate scan; this answers to about a percent in 16 KB.
//
// Hand-rolled rather than taken from a module: it is eighty lines, and an
// administrator is being asked to trust its arithmetic, so it has to be
// auditable line by line.

// HLLDefaultPrecision gives 2^14 registers, so 16 KB and roughly 0.8 percent
// standard error.
const HLLDefaultPrecision = 14

// HLL precision bounds. Outside them the register array is either too small to
// estimate with or larger than the exact set it replaces.
const (
	hllMinPrecision = 4
	hllMaxPrecision = 18
)

// ErrHLLPrecision is a merge of two sketches that cannot be merged.
var ErrHLLPrecision = errors.New("search: cannot merge sketches of different precision")

// HLL is the sketch.
type HLL struct {
	p    uint8
	regs []uint8
}

// NewHLL builds a sketch. p is clamped rather than refused: it is an internal
// tuning knob, not a value from a request.
func NewHLL(p uint8) *HLL {
	if p < hllMinPrecision {
		p = hllMinPrecision
	}
	if p > hllMaxPrecision {
		p = hllMaxPrecision
	}
	return &HLL{p: p, regs: make([]uint8, 1<<p)}
}

// Precision is the register exponent.
func (h *HLL) Precision() uint8 { return h.p }

// MemoryBytes is what the sketch costs, which the estimator reports so an
// operator can see what it spent.
func (h *HLL) MemoryBytes() int { return len(h.regs) }

// Add inserts a byte string.
func (h *HLL) Add(b []byte) { h.AddHash(Hash64(b)) }

// AddHash inserts a pre-hashed value.
func (h *HLL) AddHash(x uint64) {
	idx := x >> (64 - h.p)
	// The rank is the position of the first one bit in what is left, counting
	// from one.
	w := x << h.p
	var rank uint8
	if w == 0 {
		rank = 64 - h.p + 1
	} else {
		// LeadingZeros64 returns 0..63 for a non-zero word, so the sum is at
		// most 64 and the narrowing cannot lose a bit.
		lz := bits.LeadingZeros64(w)
		if lz > 63 {
			lz = 63
		}
		rank = uint8(lz) + 1
	}
	if rank > h.regs[idx] {
		h.regs[idx] = rank
	}
}

// Merge unions another sketch into this one.
func (h *HLL) Merge(o *HLL) error {
	if h.p != o.p {
		return ErrHLLPrecision
	}
	for i, r := range o.regs {
		if r > h.regs[i] {
			h.regs[i] = r
		}
	}
	return nil
}

// Estimate is the cardinality.
func (h *HLL) Estimate() float64 {
	m := float64(len(h.regs))
	var alpha float64
	switch len(h.regs) {
	case 16:
		alpha = 0.673
	case 32:
		alpha = 0.697
	case 64:
		alpha = 0.709
	default:
		alpha = 0.7213 / (1.0 + 1.079/m)
	}

	sum := 0.0
	zeros := 0
	for _, r := range h.regs {
		sum += math.Pow(2, -float64(r))
		if r == 0 {
			zeros++
		}
	}
	raw := alpha * m * m / sum

	// With many empty registers linear counting is far more accurate than the
	// raw estimator. No large-range correction is needed: the hash is 64 bits,
	// so the collision regime the correction exists for is unreachable.
	if raw <= 2.5*m && zeros > 0 {
		return m * math.Log(m/float64(zeros))
	}
	return raw
}

// EstimateUint is the cardinality rounded.
func (h *HLL) EstimateUint() uint64 {
	e := math.Round(h.Estimate())
	if e < 0 {
		return 0
	}
	return uint64(e)
}

// Hash64 is FNV-1a followed by a splitmix64 finaliser.
//
// FNV alone avalanches poorly in the high bits, which is exactly where the
// register index is read from. The finaliser fixes the distribution without
// taking a hashing dependency.
func Hash64(b []byte) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, c := range b {
		h ^= uint64(c)
		h *= 0x00000100000001b3
	}
	return splitmix64(h)
}

func splitmix64(z uint64) uint64 {
	z += 0x9e3779b97f4a7c15
	x := z
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
