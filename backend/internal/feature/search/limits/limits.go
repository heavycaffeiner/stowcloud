// Package limits defines bounds for search work and query input.
package limits

import "time"

const (
	SearchResults      = 1_000
	ConcurrentSearches = 4
	SearchWalkDeadline = 3 * time.Second
	SearchWalkDepth    = 64
	SearchQueryBytes   = 1 << 10
	CorpusScanEntries  = 5_000_000
)
