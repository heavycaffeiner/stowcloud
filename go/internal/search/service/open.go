//go:build linux

package service

import (
	"errors"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
)

// OpenIndex opens an index directory, degrading rather than failing.
//
// The index is a cache. A segment that failed its header or checksum disables
// it and search continues on the walk, which is the whole reason a query can
// never be failed by the index: a broken cache costs speed, never answers.
//
// The corrupt directory is left in place rather than deleted here. Removing it
// is the rebuild's job, and an operator looking at why search got slow wants
// the evidence still on disk.
func OpenIndex(dir string, cfg index.Config, log *slog.Logger) *index.NameIndex {
	if log == nil {
		log = slog.Default()
	}
	ix, err := index.Open(dir, cfg)
	if err == nil {
		return ix
	}
	if errors.Is(err, index.ErrIndexCorrupt) || errors.Is(err, index.ErrCorrupt) {
		log.Warn("the search index is corrupt and has been disabled; "+
			"search continues on the walk until it is rebuilt",
			"dir", dir, "error", err)
		return nil
	}
	log.Warn("the search index could not be opened; search continues on the walk",
		"dir", dir, "error", err)
	return nil
}
