// Package fromrust is the one-shot import of a Rust-era data directory into
// state.db. It is removed from the tree one release after the cutover.
//
// It opens the old databases query-only, never writes to them, and refuses if
// state.db already exists. The metadata cache is not imported: it regenerates,
// and that is the whole argument for splitting the store in the first place.
// It is read, though, because four kinds of durable row used to key by a node
// id and the only place that id can be turned back into a file's identity is
// the cache's own node table.
package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The files the Rust build wrote, by the names it wrote them under.
const (
	authFile     = "auth.db"
	aclFile      = "acl.db"
	linksFile    = "links.db"
	uploadFile   = "upload.db"
	settingsFile = "settings.db"
	metaFile     = "meta.db"
	compatFile   = "compat-nc.db"

	// stagedSuffix names the database being built, beside the name it takes.
	stagedSuffix = ".importing"
)

// ErrStateExists is a data directory that has already been migrated, or one
// the Go build has already run against. Either way this is not a merge.
var ErrStateExists = errors.New("state.db already exists")

// Report is what was carried across, per destination table, and what was
// dropped on the way.
type Report struct {
	Copied  map[string]int
	Dropped map[string]int
}

func newReport() *Report {
	return &Report{Copied: map[string]int{}, Dropped: map[string]int{}}
}

// Write prints the report in the order an operator reads it: what arrived,
// then what did not, then what was never going to.
func (r *Report) Write(w io.Writer) error {
	for _, name := range sortedKeys(r.Copied) {
		if _, err := fmt.Fprintf(w, "  %-16s %d rows\n", name, r.Copied[name]); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(r.Dropped) {
		if _, err := fmt.Fprintf(w,
			"  %-16s %d rows dropped: they referred to an account that no longer exists\n",
			name, r.Dropped[name]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w,
		"\nNot imported, by design:\n"+
			"  the metadata cache, which rebuilds from the tree on demand\n"+
			"  WebDAV locks, which expire in minutes and are retaken by the client\n"+
			"\nEvery file id changes once at this cutover, so every attached sync\n"+
			"client performs one full reconciliation.\n")
	return err
}

// Import reads the Rust-era databases under dir and writes state.db beside
// them. The old files are left exactly as they were.
//
// It builds the whole thing under a staged name and publishes it with one
// rename at the end. A run that fails part way therefore leaves nothing behind
// and can simply be run again, which matters because the alternative is an
// operator staring at a state.db that exists, is incomplete, and blocks the
// retry that would fix it.
func Import(ctx context.Context, dir string) (*Report, error) {
	target := filepath.Join(dir, store.StateFile)
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("%w in %s: this import does not merge", ErrStateExists, dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("looking for %s: %w", target, err)
	}

	src, err := openSources(dir)
	if err != nil {
		return nil, err
	}
	defer src.close()

	staged := filepath.Join(dir, store.StateFile+stagedSuffix)
	// Whatever a previous run left is ours and is worth nothing: it is a
	// partial import that was never published.
	if derr := discardStaged(staged); derr != nil {
		return nil, derr
	}

	rep, err := build(ctx, staged, src)
	if err != nil {
		return nil, errors.Join(err, discardStaged(staged))
	}
	if err := vfs.PublishNew(target, staged); err != nil {
		return nil, errors.Join(err, discardStaged(staged))
	}
	return rep, nil
}

// build writes the staged database and closes it, which checkpoints the
// write-ahead log back into the file so that the one rename that follows
// carries the whole of it.
func build(ctx context.Context, staged string, src *sources) (*Report, error) {
	out, err := dbfile.Open(ctx, state.Spec(staged))
	if err != nil {
		return nil, err
	}
	rep := newReport()
	if err := out.Write(ctx, func(tx *sql.Tx) error { return src.into(ctx, tx, rep) }); err != nil {
		return nil, errors.Join(err, out.Close())
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return rep, nil
}

// discardStaged removes a staged database and the two files SQLite keeps
// beside one. Closing the database normally deletes those two; a run that died
// before closing did not.
func discardStaged(staged string) error {
	var err error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if rerr := os.Remove(staged + suffix); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing %s: %w", filepath.Base(staged+suffix), rerr))
		}
	}
	return err
}

// sources is every old database that exists, and nil for each one that does
// not: a deployment that never enabled OIDC or the compatibility layer has no
// file for it, and that is not an error.
type sources struct {
	auth     *sql.DB
	acl      *sql.DB
	links    *sql.DB
	upload   *sql.DB
	settings *sql.DB
	meta     *sql.DB
	compat   *sql.DB
}

func openSources(dir string) (*sources, error) {
	s := &sources{}
	for _, spec := range []struct {
		name string
		into **sql.DB
	}{
		{authFile, &s.auth},
		{aclFile, &s.acl},
		{linksFile, &s.links},
		{uploadFile, &s.upload},
		{settingsFile, &s.settings},
		{metaFile, &s.meta},
		{compatFile, &s.compat},
	} {
		db, err := openReadOnly(filepath.Join(dir, spec.name))
		if err != nil {
			s.close()
			return nil, err
		}
		*spec.into = db
	}
	if s.auth == nil {
		s.close()
		return nil, fmt.Errorf("%s holds no %s, so it is not a data directory this can import", dir, authFile)
	}
	return s, nil
}

// openReadOnly opens an old database with writes refused by the connection
// itself, so that a mistake in this package cannot reach the file an operator
// still has to be able to roll back to. A file that is not there is not an
// error: it is a feature that deployment never used.
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("looking for %s: %w", path, err)
	}
	db, err := sql.Open(driverName, path+queryOnlyDSN)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("opening %s: %w", filepath.Base(path), err), db.Close())
	}
	return db, nil
}

func (s *sources) close() {
	for _, db := range []*sql.DB{s.auth, s.acl, s.links, s.upload, s.settings, s.meta, s.compat} {
		if db != nil {
			_ = db.Close() //nolint:errcheck // every one of these is query-only, so a failed close loses nothing.
		}
	}
}
