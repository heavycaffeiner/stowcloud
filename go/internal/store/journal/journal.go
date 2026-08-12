// Package journal is one row per (account, file) holding the last thing that
// account did to it. It is what a Recent Files listing reads.
//
// Three properties, each arrived at by thinking about a failure:
//
// It is not an audit log. The file write has already succeeded by the time a
// row is written, so a failure here is logged and dropped, and nothing may
// treat the absence of a row as evidence that a write did not happen.
//
// It is capped by row count per account, never by age. A prune comparing a
// stored timestamp against now deletes the whole table when the clock jumps
// forward, which is an ordinary event on a small box with a dead RTC before
// NTP corrects it.
//
// It is not an activity stream. No per-event history, and no reader other than
// the account itself.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
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

// ParseOp reads a stored label back. An unrecognised one reads as an upload
// rather than being dropped: a row written by a later version still says that
// something happened, and losing the row over a word loses that.
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

	// AtNs is filled in on the way out of the database. Record stamps its own,
	// from the clock it was opened with.
	AtNs int64
}

// DB is the journal. A nil *DB is the feature disabled, which is what an
// unopenable journal.db leaves behind: recording does nothing and a listing is
// empty. That is deliberate rather than a convenience, because losing this
// file costs a listing and must not cost a running server.
type DB struct {
	f   *dbfile.DB
	clk clock.Clock
}

// Spec is this database's file. It is not rebuildable: there is no way to
// reconstruct who wrote what before the record existed. It carries an adoption
// check because a Rust install keeps this file rather than migrating it, and
// that file has no version row.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{
		Path:        path,
		Migrations:  migrations(),
		Rebuildable: false,
		Adopt:       adopt,
	}
}

// New wraps an open file.
func New(f *dbfile.DB, clk clock.Clock) *DB { return &DB{f: f, clk: clk} }

// Enabled reports whether there is a journal at all.
func (d *DB) Enabled() bool { return d != nil }

// Record stores the last thing account did to a file, and deletes that
// account's oldest rows beyond the cap in the same transaction. The cap holds
// even if the process dies immediately afterwards, which an out-of-band prune
// could not promise.
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

// Recent is the account's own last touches, newest first. It is bounded by the
// same cap the table is, so a caller cannot ask for more than the table holds.
func (d *DB) Recent(ctx context.Context, account uint32, limit int) (out []Event, err error) {
	if d == nil {
		return nil, nil
	}
	if limit <= 0 || limit > limits.JournalRowsPerAccount {
		limit = limits.JournalRowsPerAccount
	}
	rows, err := d.f.SQL().QueryContext(ctx, sqlRecentForAccount, int64(account), limit)
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
