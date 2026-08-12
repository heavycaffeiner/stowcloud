package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// A Rust journal.db predates this migration runner and has no version row, so
// opening one the ordinary way would run migration 1's CREATE TABLE against a
// table that is already there and fail. It is also the one database a cutover
// keeps rather than copies: its rows say who wrote what, and nothing can
// reconstruct that.
//
// So the shape is inspected and, when it is exactly the one that shipped, the
// version row is written and nothing else is. "Exactly" is the whole of it: a
// file that nearly matches is refused, because adopting it would record a
// version the file does not have and every later migration would run against a
// shape nobody checked.

// ErrNotAdoptable is a journal.db this binary will not claim. Store treats it
// the way it treats any unopenable journal: a warning, a disabled listing, and
// the file left exactly as it was.
var ErrNotAdoptable = errors.New("journal.db carries a shape this binary does not recognise")

// column is one row of the table's own description.
type column struct {
	name    string
	kind    string
	notNull int
}

func wantColumns() []column {
	return []column{
		{"user", "INTEGER", 1},
		{"share", "INTEGER", 1},
		{"path", "TEXT", 1},
		{"op", "TEXT", 1},
		{"at_ns", "INTEGER", 1},
	}
}

func wantUnique() []string { return []string{"user", "share", "path"} }

// adopt reports version 1 for a file already holding the shipped shape, zero
// for an empty file this runner should migrate itself, and an error for
// anything else.
func adopt(ctx context.Context, db *sql.DB) (int, error) {
	tables, err := names(ctx, db, sqlUserTables)
	if err != nil {
		return 0, fmt.Errorf("reading the schema of journal.db: %w", err)
	}
	if len(tables) == 0 {
		return 0, nil
	}
	if !slices.Equal(tables, []string{"write_event"}) {
		return 0, fmt.Errorf("%w: it holds %v", ErrNotAdoptable, tables)
	}
	if err := checkColumns(ctx, db); err != nil {
		return 0, err
	}
	if err := checkIndexes(ctx, db); err != nil {
		return 0, err
	}
	return 1, nil
}

func checkColumns(ctx context.Context, db *sql.DB) (err error) {
	rows, err := db.QueryContext(ctx, sqlWriteEventCol)
	if err != nil {
		return fmt.Errorf("reading the columns of write_event: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var have []column
	for rows.Next() {
		var c column
		if serr := rows.Scan(&c.name, &c.kind, &c.notNull); serr != nil {
			return fmt.Errorf("reading a column of write_event: %w", serr)
		}
		have = append(have, c)
	}
	if rerr := rows.Err(); rerr != nil {
		return fmt.Errorf("reading the columns of write_event: %w", rerr)
	}
	if !slices.Equal(have, wantColumns()) {
		return fmt.Errorf("%w: write_event has the columns %v", ErrNotAdoptable, have)
	}
	return nil
}

// checkIndexes wants both: the unique constraint, which is what stops a second
// row for the same file, and the index the recent listing reads through.
func checkIndexes(ctx context.Context, db *sql.DB) (err error) {
	rows, err := db.QueryContext(ctx, sqlWriteEventIdx)
	if err != nil {
		return fmt.Errorf("reading the indexes of write_event: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var unique, byUser []string
	for rows.Next() {
		var name, origin string
		if serr := rows.Scan(&name, &origin); serr != nil {
			return fmt.Errorf("reading an index of write_event: %w", serr)
		}
		switch {
		case origin == "u":
			unique = append(unique, name)
		case name == indexByUser:
			byUser = append(byUser, name)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return fmt.Errorf("reading the indexes of write_event: %w", rerr)
	}

	if len(byUser) != 1 {
		return fmt.Errorf("%w: it has no %s index", ErrNotAdoptable, indexByUser)
	}
	for _, name := range unique {
		cols, cerr := names(ctx, db, sqlIndexColumns, name)
		if cerr != nil {
			return fmt.Errorf("reading the columns of index %s: %w", name, cerr)
		}
		if slices.Equal(cols, wantUnique()) {
			return nil
		}
	}
	return fmt.Errorf("%w: nothing in it makes %v unique", ErrNotAdoptable, wantUnique())
}

const indexByUser = "write_event_by_user"

func names(ctx context.Context, db *sql.DB, query string, args ...any) (out []string, err error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var name string
		if serr := rows.Scan(&name); serr != nil {
			return nil, serr
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
