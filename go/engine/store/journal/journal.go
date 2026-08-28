// Package journal stores a single row for each (account, file) pair, recording
// that account's most recent action on the file. Recent-files listings read it.
//
// Three properties, each the result of a particular failure:
//
// This is not an audit log. By the time a row is written the file write has
// already completed, so errors here are logged and discarded, and a missing row
// must never be read as proof that a write did not occur.
//
// The bound is a per-account row count, never an age. Pruning by comparing a
// stored timestamp to the present wipes the table as soon as the clock jumps
// ahead, a routine occurrence on a machine with a dead RTC awaiting NTP.
//
// This is not an activity stream. It keeps no per-event history and is readable
// only by the account whose rows it holds.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
)

// Op describes the action an account performed on a file.
type Op uint8

const (
	OpUpload Op = iota
	OpEdit
	OpCopy
	OpMove
	OpRestore
)

// String is the stored label. The label is what goes into the table, not the
// numeric value, so inserting an op in the middle of the list later does not
// re-label every row already written.
func (o Op) String() string {
	switch o {
	case OpEdit:
		return "edit"
	case OpCopy:
		return "copy"
	case OpMove:
		return "move"
	case OpRestore:
		return "restore"
	case OpUpload:
		return "upload"
	}
	return "upload"
}

// ParseOp reads a stored label back. An unrecognized one reads as an upload
// rather than failing the row: a row written by a later binary still says
// that something happened to the file, and dropping it over an unfamiliar
// word loses more than defaulting the op does.
func ParseOp(s string) Op {
	switch s {
	case "edit":
		return OpEdit
	case "copy":
		return OpCopy
	case "move":
		return OpMove
	case "restore":
		return OpRestore
	}
	return OpUpload
}

// Event records a single account's most recent interaction with one file.
type Event struct {
	// Account is the user id issued by the auth layer.
	Account uint32
	Share   vfs.ShareID
	Path    vfs.SharePath
	Op      Op

	// AtNs is filled in on the way out of the database. Record stamps its
	// own, from the clock this journal was opened with.
	AtNs int64
}

// DB is the journal. A nil *DB means the feature is off, the state left behind
// when journal.db cannot be opened: recording becomes a no-op and listings come
// back empty. Every method tolerates a nil receiver by design, so losing this
// file costs one listing instead of requiring a check at every call site up
// through the core.
type DB struct {
	f   *dbfile.DB
	clk clock.Clock
}

// Spec describes this database's file. Rebuilding is impossible, since no other
// source records which account wrote which file before these rows existed.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: false}
}

// New wraps an open file.
func New(f *dbfile.DB, clk clock.Clock) *DB { return &DB{f: f, clk: clk} }

// Enabled reports whether there is a journal at all. Safe on a nil receiver.
func (d *DB) Enabled() bool { return d != nil }

// Record stores the last thing account did to a file and deletes that
// account's oldest rows past the cap in the same transaction, so the cap
// holds even against a crash immediately after. Safe on a nil receiver,
// where it is a no-op.
func (d *DB) Record(ctx context.Context, e Event) error {
	if d == nil {
		return nil
	}
	at := d.clk.Nanos()
	const keep = int64(limits.JournalRowsPerAccount)
	return d.f.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlUpsertEvent,
			int64(e.Account), int64(e.Share), e.Path.String(), e.Op.String(), at); err != nil {
			return fmt.Errorf("recording a write: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlTrimAccount, int64(e.Account), keep); err != nil {
			return fmt.Errorf("trimming the journal: %w", err)
		}
		return nil
	})
}

// Recent is the account's own last touches, newest first, with no window.
// Safe on a nil receiver, where it answers an empty list.
func (d *DB) Recent(ctx context.Context, account uint32, limit int) ([]Event, error) {
	return d.RecentSince(ctx, account, 0, limit)
}

// RecentSince behaves identically but restricts results to writes at or after a
// given instant.
//
// The caller supplies an instant rather than a duration. Resolving a relative
// window inside this package would resolve it against this package's clock,
// making "the last seven days" mean different spans depending on which side of
// the call performed the arithmetic.
//
// Passing zero for sinceNs disables the window. limit is clamped to the same
// per-account cap the table enforces, and a non-positive limit adopts that cap
// instead of producing an error. Rows are re-parsed as they are read out: a
// stored path this server would now reject fails the read rather than being
// quietly skipped, since this package cannot distinguish a corrupt row from a
// stale one and that call belongs to the caller.
func (d *DB) RecentSince(ctx context.Context, account uint32, sinceNs int64, limit int) (out []Event, err error) {
	if d == nil {
		return nil, nil
	}
	if limit <= 0 || limit > limits.JournalRowsPerAccount {
		limit = limits.JournalRowsPerAccount
	}

	stmt, args := sqlRecentForAccount, []any{int64(account), limit}
	if sinceNs > 0 {
		stmt, args = sqlRecentSince, []any{int64(account), sinceNs, limit}
	}
	rows, err := d.f.SQL().QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the journal: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			share, at int64
			path, op  string
		)
		if err := rows.Scan(&share, &path, &op, &at); err != nil {
			return nil, fmt.Errorf("reading a journal row: %w", err)
		}
		s, err := num.Narrow[uint32](share)
		if err != nil {
			return nil, fmt.Errorf("journal row carries share %d: %w", share, err)
		}
		p, err := vfs.ParseSharePath(path)
		if err != nil {
			return nil, fmt.Errorf("journal row carries a path this server would refuse: %w", err)
		}
		out = append(out, Event{
			Account: account,
			Share:   vfs.ShareID(s),
			Path:    p,
			Op:      ParseOp(op),
			AtNs:    at,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the journal: %w", err)
	}
	return out, nil
}
