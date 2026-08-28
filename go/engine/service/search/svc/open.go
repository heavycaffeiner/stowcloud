//go:build linux

package svc

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

// Opening an index directory, degrading rather than failing.
//
// The index is a cache. Every failure below ends at the same place, a nil
// index and search on the walk, because a query can never be failed by the
// index: a broken cache costs speed, never answers.
//
// What the three cases do differ in is what an operator is told, and that is
// the point of classifying them. The version this replaces logged one line for
// all of them, so a permission error read as "needs a rebuild" and an operator
// rebuilt an index that was never corrupt.

// OpenState is which of the three worlds an open landed in, returned so a
// caller can act on the distinction the log line describes.
type OpenState int

const (
	// OpenReady is an index that opened.
	OpenReady OpenState = iota
	// OpenAbsent is no index directory yet: it was never built. Not a
	// warning, because nothing is wrong.
	OpenAbsent
	// OpenCorrupt is a header or checksum that did not verify. A rebuild is
	// the fix, and the evidence is left on disk.
	OpenCorrupt
	// OpenUnavailable is anything else: permissions, I/O. A rebuild is not
	// the fix and is not suggested, because the index may be intact and
	// simply unreachable.
	OpenUnavailable
)

func (s OpenState) String() string {
	switch s {
	case OpenReady:
		return "ready"
	case OpenAbsent:
		return "absent"
	case OpenCorrupt:
		return "corrupt"
	}
	return "unavailable"
}

// OpenIndex loads an index directory and reports which state it encountered.
//
// A corrupt directory stays where it is rather than being deleted here. Removal
// belongs to the rebuild, and an operator investigating why search slowed down
// wants the evidence still present on disk.
func OpenIndex(dir string, cfg index.Config, log *slog.Logger) (*index.NameIndex, OpenState) {
	if log == nil {
		log = slog.Default()
	}

	// Asked before opening, because Open creates the directory: after it, the
	// never-built case is indistinguishable from a built one.
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		ix, oerr := index.Open(dir, cfg)
		if oerr == nil {
			// A fresh empty index. Nothing was ever built into it, so this is
			// the quiet case rather than a warning.
			return ix, OpenAbsent
		}
		log.Warn("the search index directory could not be created; search continues on the walk",
			"dir", dir, "error", oerr)
		return nil, OpenUnavailable
	}

	ix, err := index.Open(dir, cfg)
	if err == nil {
		return ix, OpenReady
	}
	if errors.Is(err, index.ErrIndexCorrupt) || errors.Is(err, index.ErrCorrupt) {
		log.Warn("the search index is corrupt and has been disabled; "+
			"search continues on the walk until it is rebuilt",
			"dir", dir, "error", err)
		return nil, OpenCorrupt
	}
	// Deliberately not suggesting a rebuild: the index may be perfectly good
	// and simply unreadable, and telling an operator to rebuild it would have
	// them destroy a working cache to fix a permission.
	log.Warn("the search index could not be opened, and may recover; search continues on the walk",
		"dir", dir, "error", err)
	return nil, OpenUnavailable
}
