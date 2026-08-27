// Package journal holds one row per (account, file): the last thing that
// account did to it. It is what a recent-files listing reads.
//
// Three properties, each arrived at from a specific failure:
//
// It is not an audit log. The file write has already succeeded by the time a
// row is written, so a failure here is logged and dropped, and nothing may
// treat the absence of a row as evidence that a write did not happen.
//
// It is capped by row count per account, never by age. A prune comparing a
// stored timestamp against now empties the table the moment the clock jumps
// forward, which is ordinary on a box with a dead RTC before NTP corrects it.
//
// It is not an activity stream. No per-event history, and no reader other
// than the account whose rows they are.
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

// Op is what an account did to a file.
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

// Event is one account's last touch of one file.
type Event struct {
	// Account is the user id the auth layer hands out.
	Account uint32
	Share   vfs.ShareID
	Path    vfs.SharePath
	Op      Op

	// AtNs is filled in on the way out of the database. Record stamps its
	// own, from the clock this journal was opened with.
	AtNs int64
}

// DB is the journal. A nil *DB is the feature disabled, which is what an
// unopenable journal.db leaves behind: recording does nothing and a listing
// is empty. Every method below is safe on a nil receiver, deliberately, so
// that losing this file costs one listing rather than a branch at every call
// site up to the core.
type DB struct {
	f   *dbfile.DB
	clk clock.Clock
}

// Spec is this database's file. It is not rebuildable: nothing can
// reconstruct who wrote what before the record existed.
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

// RecentSince is the same, bounded to writes at or after an instant.
//
// An instant rather than a duration, resolved by the caller: a relative
// window resolved inside this package would be resolved against this
// package's clock, and "the last seven days" then means two different
// windows depending on which side of the call did the arithmetic.
//
// A sinceNs of zero is no window. limit clamps to the same cap the table
// holds per account, and a non-positive limit takes that cap rather than
// erroring. Every row is re-parsed on the way out: a stored path this
// server would now refuse fails the read rather than being silently
// dropped, because this package cannot tell a corrupt row from a stale one
// and that judgment belongs to the caller.
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
