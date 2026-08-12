package dbfile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

// Migration is one ordered step. Its position in the list is the version it
// produces, so a step is never renumbered and never reordered, and a step that
// has shipped is never edited.
type Migration struct {
	Name string

	// SQL is one batch. It is applied with the version bump in a single
	// transaction, so a crash part way through leaves the old version beside
	// the old shape rather than a half-applied one.
	SQL string

	// Discard marks a step that throws away what is already in the database
	// instead of migrating it, and names in its own SQL what it drops. The
	// runner refuses it unless the file is rebuildable, which the cache is and
	// nothing else is.
	Discard bool
}

// migrate brings the database up to the last version in the list, one
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
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, sqlSetVersion, version)
			return err
		}); err != nil {
			return fmt.Errorf("%w: %s: %s: %w", ErrMigrationFailed, name, m.Name, err)
		}
	}
	return nil
}

// readVersion reports the stored version, and zero for a database that has
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

// Version reports the schema version on disk. It is here for the tests that
// assert a migration and its version bump are one transaction, and for the
// diagnostics that report what an operator is running.
func (d *DB) Version(ctx context.Context) (int, error) { return readVersion(ctx, d.sql) }
