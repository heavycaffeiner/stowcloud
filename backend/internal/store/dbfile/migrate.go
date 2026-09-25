package dbfile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

// Migration represents a single ordered step. The version it yields is its
// index in the list, so a released step is never rewritten, renumbered or
// moved; new steps are only ever appended.
type Migration struct {
	Name string

	// SQL is one batch, applied with the version bump inside a single
	// transaction. A crash part way through leaves the old version standing
	// beside the old shape, so there is no half-applied step to repair.
	SQL string

	// Discard marks a step that throws away what the database holds instead
	// of migrating it forward. The runner refuses it unless the spec is
	// rebuildable.
	Discard bool

	// Precondition runs inside the step's own transaction, before its SQL.
	// It is for a refusal a CHECK can enforce but not explain: a constraint
	// failure names the constraint, not the row, and an operator holding a
	// durable database needs the row.
	Precondition func(context.Context, *sql.Tx) error
}

// migrate brings the file up to the last version in the list, one
// transaction per step.
func migrate(ctx context.Context, d *DB, spec Spec) error {
	name := filepath.Base(spec.Path)
	if _, err := d.sql.ExecContext(ctx, sqlCreateSchemaVersion); err != nil {
		return fmt.Errorf("%w: %s: creating the version table: %w", ErrMigrationFailed, name, err)
	}

	have, err := readVersion(ctx, d.sql)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrMigrationFailed, name, err)
	}
	if have > len(spec.Migrations) {
		return fmt.Errorf("%w: %s is at version %d and this binary knows %d",
			ErrSchemaAhead, name, have, len(spec.Migrations))
	}

	for i := have; i < len(spec.Migrations); i++ {
		m := spec.Migrations[i]
		if m.Discard && !spec.Rebuildable {
			return fmt.Errorf("%w: %s discards what is in %s, which nothing can rebuild",
				ErrMigrationFailed, m.Name, name)
		}
		version := i + 1
		if err := d.Write(ctx, func(tx *sql.Tx) error {
			if m.Precondition != nil {
				if err := m.Precondition(ctx, tx); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, sqlWriteVersion, version)
			return err
		}); err != nil {
			return fmt.Errorf("%w: %s: %s: %w", ErrMigrationFailed, name, m.Name, err)
		}
	}
	return nil
}

// readVersion reports the stored version, and zero for a file that has
// never been migrated.
func readVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, sqlReadVersion).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the schema version: %w", err)
	}
	return v, nil
}

// Version reports the schema version on disk, for the tests that assert a
// step and its version bump commit together and for the diagnostics that
// report what an operator is running.
func (d *DB) Version(ctx context.Context) (int, error) { return readVersion(ctx, d.sql) }
