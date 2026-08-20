package search

import (
	"bytes"
	"strings"
	"time"
)

// Ranking.
//
//	score = 3.0 x exact name match
//	      + 2.0 x name prefix match
//	      + 1.0 x normalised bm25   (0 on the walk path, which has no content)
//	      + 0.5 x recency           (30-day linear decay)
//	      + 0.3 x below the current scope
//	      - 1.0 x hidden
//
// No learned ranking and no click logs.
const (
	WeightExact   float32 = 3.0
	WeightPrefix  float32 = 2.0
	WeightBM25    float32 = 1.0
	WeightRecency float32 = 0.5
	WeightInScope float32 = 0.3
	WeightHidden  float32 = 1.0
)

// RecencyWindow is how long recency takes to decay to zero.
const RecencyWindow = 30 * 24 * time.Hour

// RankInput is one candidate to score.
type RankInput struct {
	// NameFolded is the folded filename, not the path.
	NameFolded []byte
	// Needle is the folded query.
	Needle []byte
	// Path is the display path, used for the scope test.
	Path string
	// MTimeNs is nil when no stat was performed, and then the recency term is
	// zero. That is the honest answer rather than a guess, and it is what
	// keeps a name-only query from paying for metadata nobody asked for.
	MTimeNs *int64
	NowNs   int64
	// Scope is the directory the caller is looking from, empty for none.
	Scope string
	// BM25 is already normalised to 0..1, and is always zero on the walk.
	BM25 float32
}

// Score ranks one candidate.
func Score(i RankInput) float32 {
	var s float32

	if len(i.Needle) > 0 {
		if bytes.Equal(i.NameFolded, i.Needle) {
			s += WeightExact
		}
		// An exact match is also a prefix match, so the two weights add. That
		// is deliberate: it is what puts an exact hit above everything.
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

	// No scope means the caller is not looking from anywhere in particular, so
	// the term does not apply. Awarding it to everything would shift every
	// score by a constant and change nothing but the numbers, which is worse
	// than useless: it would make a scoreless candidate look ranked.
	if i.Scope != "" && InScope(i.Path, i.Scope) {
		s += WeightInScope
	}

	if IsHidden(i.NameFolded) {
		s -= WeightHidden
	}

	return s
}

// InScope reports whether path is at or below scope.
//
// Component-aware, so "photo" does not match "photography". An empty scope
// contains everything, which is the answer a filter wants; Score treats the
// same case as "no scope was given" and skips the bonus instead.
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

// IsHidden reports the dotfile convention.
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
