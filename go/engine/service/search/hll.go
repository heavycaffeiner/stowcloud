package search

import (
	"errors"
	"math"
	"math/bits"
)

// A compact HyperLogLog.
//
// The estimator requires the distinct-trigram count to size the posting
// dictionary, and that figure is what separates a CJK corpus from a Latin one.
// Counting exactly would mean retaining millions of trigrams in a set during the
// estimate scan, whereas this answers within about a percent using 16 KB.
//
// Written here rather than pulled from a module: it spans eighty lines, and an
// administrator is being asked to trust its arithmetic, so it must be auditable
// line by line.

// HLLDefaultPrecision yields 2^14 registers, occupying 16 KB with roughly 0.8
// percent standard error.
const HLLDefaultPrecision = 14

// HLL precision bounds. Beyond them the register array becomes either too small
// to estimate from or larger than the exact set it stands in for.
const (
	hllMinPrecision = 4
	hllMaxPrecision = 18
)

// ErrHLLPrecision reports an attempt to merge two incompatible sketches.
var ErrHLLPrecision = errors.New("search: cannot merge sketches of different precision")

// HLL is the sketch.
type HLL struct {
	p    uint8
	regs []uint8
}

// NewHLL builds a sketch.
//
// p is clamped rather than refused, and that is sound only because every
// caller passes a compiled-in constant: it is an internal tuning knob, not a
// value from a request. Should precision ever become configurable, the clamp
// has to become a refusal, or an operator's out-of-range setting would take
// effect as a different number with nothing saying so.
func NewHLL(p uint8) *HLL {
	if p < hllMinPrecision {
		p = hllMinPrecision
	}
	if p > hllMaxPrecision {
		p = hllMaxPrecision
	}
	return &HLL{p: p, regs: make([]uint8, 1<<p)}
}

// Precision gives the register exponent.
func (h *HLL) Precision() uint8 { return h.p }

// MemoryBytes gives the sketch's cost, which the estimator reports so an
// operator can see what was spent.
func (h *HLL) MemoryBytes() int { return len(h.regs) }

// Add inserts a byte string.
func (h *HLL) Add(b []byte) { h.AddHash(Hash64(b)) }

// AddHash records a value that has already been hashed.
func (h *HLL) AddHash(x uint64) {
	idx := x >> (64 - h.p)
	// The rank is the one-based position of the first set bit in the
	// remainder.
	w := x << h.p
	var rank uint8
	if w == 0 {
		rank = 64 - h.p + 1
	} else {
		// LeadingZeros64 yields 0 through 63 for a non-zero word, capping the
		// sum at 64 so the narrowing cannot discard a bit.
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

// Merge folds another sketch into this one.
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

	// When many registers remain empty, linear counting proves far more accurate
	// than the raw estimator. No large-range correction applies, since the hash
	// is 64 bits and the collision regime that correction addresses cannot be
	// reached.
	if raw <= 2.5*m && zeros > 0 {
		return m * math.Log(m/float64(zeros))
	}
	return raw
}

// EstimateUint gives the rounded cardinality.
func (h *HLL) EstimateUint() uint64 {
	e := math.Round(h.Estimate())
	if e < 0 {
		return 0
	}
	return uint64(e)
}

// Hash64 applies FNV-1a followed by a splitmix64 finaliser.
//
// FNV on its own avalanches poorly in the high bits, precisely where the
// register index is taken from. The finaliser corrects the distribution without
// introducing a hashing dependency.
//
// It is separate from the index's FNV1a32, which serves as a corruption
// checksum. The two tolerate collisions differently and remain distinct
// primitives.
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
