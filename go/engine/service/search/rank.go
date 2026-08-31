package search

import (
	"bytes"
	"strings"
	"time"
)

// Ranking.
//
//	score = 3.0 x exact filename match
//	      + 2.0 x name prefix match
//	      + 1.0 x normalised bm25   (zero along the walk path, which has no content)
//	      + 0.5 x recency           (linear decay over 30 days)
//	      + 0.3 x beneath the current scope
//	      - 1.0 x hidden
//
// Nothing is learned and no clicks are logged.
const (
	WeightExact   float32 = 3.0
	WeightPrefix  float32 = 2.0
	WeightBM25    float32 = 1.0
	WeightRecency float32 = 0.5
	WeightInScope float32 = 0.3
	WeightHidden  float32 = 1.0
)

// RecencyWindow gives the span over which recency decays to zero.
const RecencyWindow = 30 * 24 * time.Hour

// RankInput describes a single candidate to score.
type RankInput struct {
	// NameFolded holds the folded filename rather than the path.
	NameFolded []byte
	// Needle is the folded query.
	Needle []byte
	// Path holds the display path, which the scope test uses.
	Path string
	// MTimeNs stays nil where no stat ran, leaving the recency term at zero.
	// That reports the truth instead of guessing, and it is what spares a
	// name-only query the cost of metadata nobody requested.
	MTimeNs *int64
	NowNs   int64
	// Scope names the directory the caller searches from, empty when there is
	// none.
	Scope string
	// BM25 arrives normalised to the range 0 to 1, and is always zero on the
	// walk.
	BM25 float32
}

// Score ranks one candidate.
func Score(i RankInput) float32 {
	var s float32

	if len(i.Needle) > 0 {
		if bytes.Equal(i.NameFolded, i.Needle) {
			s += WeightExact
		}
		// An exact match qualifies as a prefix match too, so both weights
		// apply. That is intentional, and it is what lifts an exact hit above
		// everything else.
		if bytes.HasPrefix(i.NameFolded, i.Needle) {
			s += WeightPrefix
		}
	}

	s += WeightBM25 * clamp01(i.BM25)

	if i.MTimeNs != nil {
		age := i.NowNs - *i.MTimeNs
		window := int64(RecencyWindow)
		if age < window {
			if age < 0 {
				age = 0
			}
			frac := 1.0 - float64(age)/float64(window)
			s += WeightRecency * float32(frac)
		}
	}

	// An absent scope means the caller searches from nowhere in particular, so
	// the term does not apply. Granting it universally would offset every score
	// by a constant, altering nothing but the numbers, which is worse than
	// pointless: it would make a scoreless candidate appear ranked.
	if i.Scope != "" && InScope(i.Path, i.Scope) {
		s += WeightInScope
	}

	if IsHidden(i.NameFolded) {
		s -= WeightHidden
	}

	return s
}

// InScope reports whether path sits at or beneath scope.
//
// Comparison is component-aware, so "photo" does not match "photography". An
// empty scope contains everything, which is what a filter wants; Score reads the
// same case as no scope having been supplied and omits the bonus.
func InScope(path, scope string) bool {
	if scope == "" {
		return true
	}
	scope = strings.TrimRight(scope, "/")
	if path == scope {
		return true
	}
	return len(path) > len(scope) && strings.HasPrefix(path, scope) && path[len(scope)] == '/'
}

// IsHidden applies the dotfile convention.
func IsHidden(name []byte) bool { return len(name) > 0 && name[0] == '.' }

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
