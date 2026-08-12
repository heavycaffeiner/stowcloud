// Package store is the three SQLite databases under the data directory, split
// by what losing each one costs.
//
// cache.db costs a rebuild: everything in it can be walked back out of the
// filesystem, and deleting it is a supported operation. state.db costs an
// account, a grant or a share link, and it is the entire backup instruction.
// journal.db costs a listing, and it is a third file rather than a table in
// either of the other two so that the difference is visible to whoever decides
// what to back up.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// The three files, and the directory mode a data directory is created with.
const (
	CacheFile   = "cache.db"
	StateFile   = "state.db"
	JournalFile = "journal.db"

	dirMode = 0o750
)

// Options is what Open cannot work out from the directory.
type Options struct {
	// Clock stamps journal rows. Nil takes the system clock.
	Clock clock.Clock
}

// Store is the three databases.
type Store struct {
	cacheFile   *dbfile.DB
	stateFile   *dbfile.DB
	journalFile *dbfile.DB

	cache   *cache.DB
	state   *state.DB
	journal *journal.DB
}

// Open opens the databases under dir, applies the pragmas and runs the pending
// migrations. It returns ErrSchemaAhead if either of the two that matter was
// written by a newer binary, because a downgrade writing an old shape into a
// new file loses data silently.
//
// A journal.db that will not open is a warning and a disabled feature. The
// server still serves files; it stops recording what it did with them.
func Open(dir string, opt Options) (*Store, error) {
	ctx := context.Background()
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("creating the data directory: %w", err)
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}

	// The durable half opens first: the cache consults its override table on
	// every id it allocates.
	stateFile, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, StateFile)))
	if err != nil {
		return nil, err
	}
	st := state.New(stateFile)

	cacheFile, err := dbfile.Open(ctx, cache.Spec(filepath.Join(dir, CacheFile)))
	if err != nil {
		return nil, errors.Join(err, stateFile.Close())
	}

	s := &Store{
		cacheFile: cacheFile,
		stateFile: stateFile,
		cache:     cache.New(cacheFile, st),
		state:     st,
	}

	journalFile, err := dbfile.Open(ctx, journal.Spec(filepath.Join(dir, JournalFile)))
	if err != nil {
		slog.Warn("the write journal could not be opened, so recent-files listings are empty",
			slog.String("file", JournalFile), slog.Any("error", err))
		return s, nil
	}
	s.journalFile = journalFile
	s.journal = journal.New(journalFile, clk)
	return s, nil
}

// Cache is the rebuildable half. Every method on it is allowed to answer "I do
// not know" and have the caller fall back to the filesystem; nothing may treat
// a missing row as a missing file.
func (s *Store) Cache() *cache.DB { return s.cache }

// State is the durable half. It is the entire backup instruction.
func (s *Store) State() *state.DB { return s.state }

// Journal is the third file, and nil when it could not be opened. Its methods
// are safe on a nil receiver, so a caller does not branch on this.
func (s *Store) Journal() *journal.DB { return s.journal }

// Close checkpoints and closes all three.
func (s *Store) Close() error {
	var err error
	if s.journalFile != nil {
		err = errors.Join(err, s.journalFile.Close())
	}
	return errors.Join(err, s.cacheFile.Close(), s.stateFile.Close())
}
