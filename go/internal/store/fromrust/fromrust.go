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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
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
	locksFile    = "dav-locks.db"

	// stagedSuffix and stagingBytes name the database being built, beside the
	// name it takes. The random half is what makes the name this invocation's
	// own: with one fixed name, a second run cannot tell a crashed run's
	// leftover from a live run's database, and removing it either way is how a
	// concurrent import loses its work.
	stagedSuffix = ".importing-"
	stagingBytes = 8

	// legacyTokenKeyVersion marks a Rust share-link ciphertext, whose AAD
	// carried no version at all. Phase 3 re-seals it under a positive one.
	legacyTokenKeyVersion = 0
)

// ErrStateExists is a data directory that has already been migrated, or one
// the Go build has already run against. Either way this is not a merge.
var ErrStateExists = errors.New("state.db already exists")

// Reason is why a row did not come across. Every drop site names one: this
// report is the operator's only account of what a one-way migration lost, and a
// single count with one sentence attached to it says "unknown account" over a
// row that was discarded for something else entirely.
type Reason string

const (
	ReasonUnknownUser  Reason = "the account they belonged to no longer exists"
	ReasonUnknownGroup Reason = "the group they belonged to no longer exists"
	ReasonMissingNode  Reason = "the metadata cache holds no row for the file they named"
	ReasonExpired      Reason = "they had already expired"
	ReasonCorruptRange Reason = "their received-range set would not decode"
)

// Drop is one table and one reason, which is the granularity the report has to
// have to be worth reading.
type Drop struct {
	Table  string
	Reason Reason
}

// Report is what was carried across, per destination table, what was dropped on
// the way with why, and what was found in the directory and left alone.
type Report struct {
	Copied  map[string]int
	Dropped map[Drop]int

	// Ignored is the unpublished staging databases the directory held. They are
	// named rather than removed: a name alone cannot say whether it belongs to
	// a run that died or one still going.
	Ignored []string
}

func newReport() *Report {
	return &Report{Copied: map[string]int{}, Dropped: map[Drop]int{}}
}

// Write prints the report in the order an operator reads it: what arrived,
// then what did not and why, then what was never going to.
func (r *Report) Write(w io.Writer) error {
	for _, name := range sortedKeys(r.Copied) {
		if _, err := fmt.Fprintf(w, "  %-16s %d rows\n", name, r.Copied[name]); err != nil {
			return err
		}
	}
	for _, d := range sortedDrops(r.Dropped) {
		if _, err := fmt.Fprintf(w, "  %-16s %d rows dropped: %s\n",
			d.Table, r.Dropped[d], d.Reason); err != nil {
			return err
		}
	}
	for _, name := range r.Ignored {
		if _, err := fmt.Fprintf(w,
			"  %-16s left alone: an unpublished import this run did not create\n",
			name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w,
		"\nNot imported, by design:\n"+
			"  the metadata cache, which rebuilds from the tree on demand\n"+
			"\nRetained in place, not copied:\n"+
			"  journal.db, which this binary adopts as it stands\n"+
			"\nEvery file id changes once at this cutover, so every attached sync\n"+
			"client performs one full reconciliation.\n")
	return err
}

// Import reads the Rust-era databases under dir and writes state.db beside
// them. The old files are left exactly as they were.
//
// The server that wrote them has to be stopped, and this takes the same
// advisory lock both servers hold to make sure of it. The sources are several
// independent WAL databases with no snapshot across them, so reading them while
// something writes produces a state.db holding a user from one instant and their
// grants from another. A publication that is atomic does not fix a source that
// was inconsistent when it was read.
//
// It builds the whole thing under a staging name of its own and publishes it
// with one no-clobber rename at the end. A run that fails part way leaves no
// destination and can simply be run again, which matters because the
// alternative is an operator staring at a state.db that exists, is incomplete,
// and blocks the retry that would fix it.
func Import(ctx context.Context, dir string, clk clock.Clock) (*Report, error) {
	lock, err := store.LockInstance(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			slog.Warn("releasing the data directory lock failed",
				slog.String("dir", dir), slog.Any("error", rerr))
		}
	}()

	target := filepath.Join(dir, store.StateFile)
	if _, serr := os.Stat(target); serr == nil {
		return nil, fmt.Errorf("%w in %s: this import does not merge", ErrStateExists, dir)
	} else if !errors.Is(serr, os.ErrNotExist) {
		return nil, fmt.Errorf("looking for %s: %w", target, serr)
	}

	// The manifest and the inventory both run before a byte is written, so a
	// directory this binary cannot account for costs nothing to find out about.
	found, err := manifest(dir)
	if err != nil {
		return nil, err
	}

	src, err := openSources(dir)
	if err != nil {
		return nil, err
	}
	defer src.close()

	rep := newReport()
	rep.Ignored = found
	if err := src.checkInventory(ctx, rep); err != nil {
		return nil, err
	}

	// A staging name of this invocation's own, so that two runs cannot mistake
	// each other's live database for a leftover. Nothing removes a staging name
	// it did not create: an unpublished file from a dead process is inert,
	// while one a concurrent process owns is not, and the name alone does not
	// say which it is.
	staged, err := reserveStaging(dir)
	if err != nil {
		return nil, err
	}

	if err := build(ctx, staged, src, rep, clk); err != nil {
		return nil, errors.Join(err, discardStaged(staged))
	}
	// No-clobber, so a destination that appeared while this ran is the
	// kernel's refusal rather than a check something could race.
	if err := vfs.PublishNew(target, staged); err != nil {
		if errors.Is(err, vfs.ErrExists) {
			err = fmt.Errorf("%w in %s: %w", ErrStateExists, dir, err)
		}
		return nil, errors.Join(err, discardStaged(staged))
	}
	return rep, nil
}

// build writes the staged database and closes it, which checkpoints the
// write-ahead log back into the file so that the one rename that follows
// carries the whole of it.
func build(ctx context.Context, staged string, src *sources, rep *Report, clk clock.Clock) error {
	out, err := dbfile.Open(ctx, state.Spec(staged))
	if err != nil {
		return err
	}
	if err := out.Write(ctx, func(tx *sql.Tx) error { return src.into(ctx, tx, rep, clk) }); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

// reserveStaging picks a name no other invocation can be using and creates the
// file, so that the name is taken rather than merely chosen. O_EXCL turns the
// vanishingly unlikely collision into a refusal rather than two importers
// writing one file.
func reserveStaging(dir string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("naming the staging database: %w", err)
	}
	staged := filepath.Join(dir, store.StateFile+stagedSuffix+hex.EncodeToString(b[:]))

	f, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("reserving %s: %w", filepath.Base(staged), err)
	}
	if cerr := f.Close(); cerr != nil {
		return "", errors.Join(fmt.Errorf("reserving %s: %w", filepath.Base(staged), cerr),
			discardStaged(staged))
	}
	return staged, nil
}

// isStagingName reports a name this package produces for a database it has not
// published. It is what keeps such a file out of the unknown-database refusal
// without making it something anything deletes.
func isStagingName(name string) bool {
	rest, ok := strings.CutPrefix(name, store.StateFile+stagedSuffix)
	if !ok || len(rest) != 2*stagingBytes {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// discardStaged removes a staged database and the two files SQLite keeps
// beside one. Closing the database normally deletes those two; a run that died
// before closing did not.
//
// It is only ever called with a name this invocation reserved. A staging file
// under another name belongs to a run this one cannot identify, and the
// instance lock does not prove who created a file that was already there.
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
	// byName is every source this import opens, keyed by the filename the old
	// build wrote it under, and nil for one that is not there. Keyed rather
	// than named fields because the inventory walks the same set and the two
	// would otherwise be two lists to keep in step.
	open map[string]*sql.DB

	auth     *sql.DB
	acl      *sql.DB
	links    *sql.DB
	upload   *sql.DB
	settings *sql.DB
	meta     *sql.DB
	compat   *sql.DB
	locks    *sql.DB
}

func openSources(dir string) (*sources, error) {
	s := &sources{open: map[string]*sql.DB{}}
	for _, name := range knownFiles() {
		db, err := openReadOnly(filepath.Join(dir, name))
		if err != nil {
			s.close()
			return nil, err
		}
		if db != nil {
			s.open[name] = db
		}
	}
	s.auth = s.byName(authFile)
	s.acl = s.byName(aclFile)
	s.links = s.byName(linksFile)
	s.upload = s.byName(uploadFile)
	s.settings = s.byName(settingsFile)
	s.meta = s.byName(metaFile)
	s.compat = s.byName(compatFile)
	s.locks = s.byName(locksFile)

	if s.auth == nil {
		s.close()
		return nil, fmt.Errorf("%s holds no %s, so it is not a data directory this can import", dir, authFile)
	}
	return s, nil
}

// byName is the database that file holds, or nil for a deployment that never
// had one: no OIDC, no compatibility layer, no search index.
func (s *sources) byName(file string) *sql.DB { return s.open[file] }

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
	for _, db := range s.open {
		_ = db.Close() //nolint:errcheck // every one of these is query-only, so a failed close loses nothing.
	}
}
