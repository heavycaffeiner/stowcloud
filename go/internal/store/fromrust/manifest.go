package fromrust

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// The set of SQLite files this server owns, which is a different question from
// the set of tables it knows: a database file added to the old build and not to
// this list would be skipped whole, and every table in it with it.
const (
	sharesFile  = "shares.db"
	jobsFile    = "jobs.db"
	indexFile   = "index.db"
	journalFile = store.JournalFile
)

// ErrUnknownDatabase is a SQLite file in the data directory that nothing here
// accounts for.
var ErrUnknownDatabase = errors.New("an unrecognised database is in the data directory")

// knownFiles is every database this import reads, in the order the report
// mentions them.
func knownFiles() []string {
	return []string{
		authFile, aclFile, linksFile, uploadFile, settingsFile,
		metaFile, locksFile, compatFile, sharesFile, jobsFile, indexFile, journalFile,
	}
}

// ownedFiles adds the two this build writes, which are not sources but are also
// not a surprise to find here.
func ownedFiles() []string {
	return append(knownFiles(), store.StateFile, store.CacheFile)
}

// manifest walks the data directory and reports the staging databases it found,
// refusing any SQLite file this binary does not recognise.
//
// A file it does not know is a refusal rather than a warning for the same
// reason an unknown table is: whatever wrote it did so because something needed
// to be durable, and a migration that steps over it reports success while
// leaving it behind.
func manifest(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var staging []string
	owned := ownedFiles()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case isSidecar(name):
			// A write-ahead log or its shared-memory index belongs to the
			// database beside it and is not an artifact of its own.
		case isStagingName(name):
			// An unpublished staging database from a run that died, or one a
			// concurrent run owns right now. Its name alone cannot say which,
			// so it is reported and left exactly where it is.
			staging = append(staging, name)
		case !strings.HasSuffix(name, ".db"):
		case !slices.Contains(owned, name):
			return nil, fmt.Errorf("%w: %s. If it holds durable state, this binary "+
				"would leave it behind", ErrUnknownDatabase, filepath.Join(dir, name))
		}
	}
	slices.Sort(staging)
	return staging, nil
}

// isSidecar reports the two files SQLite keeps beside a database. They are
// never backed up, copied or classified on their own.
func isSidecar(name string) bool {
	return strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")
}
