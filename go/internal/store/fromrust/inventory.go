package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Every table in every database the old build wrote, and what this import does
// with it. A table that is not in here stops the migration.
//
// The list is the point. Without it, a table added to the old schema after this
// was written is silently not imported, and the first evidence is a user
// noticing their SMB password stopped working. Phase 13 compares this against
// what the source files actually hold.

// how is what happens to a table.
type how uint8

const (
	// copied lands in state.db as it stands.
	copied how = iota
	// transformed is read and its content reaches state.db as something else.
	transformed
	// retained stays in its own file, which this binary goes on using.
	retained
	// rebuildable regenerates from the filesystem.
	rebuildable
	// discarded is deliberately not carried, with a reason.
	discarded
	// deferred belongs to a later phase, which is where its destination
	// arrives. Rows in one now stop the migration.
	deferred
)

// disposition is one table's fate, and why.
type disposition struct {
	how how
	// phase is the phase that owns the destination, for a deferred table.
	phase int
	// why is the sentence a discarded table gets.
	why string
}

// ErrUnknownTable is a table the old build wrote that nothing here classified.
var ErrUnknownTable = errors.New("a source table has no recorded disposition")

// ErrDeferredTableHasRows is durable state a later phase owns. Reporting
// success over it would be the data loss this whole inventory exists to catch.
var ErrDeferredTableHasRows = errors.New("a source table holds rows a later phase owns")

func inventory() map[string]map[string]disposition {
	return map[string]map[string]disposition{
		authFile: {
			"user":            {how: copied},
			"group_":          {how: copied},
			"membership":      {how: copied},
			"session":         {how: copied},
			"app_password":    {how: copied},
			"recovery_code":   {how: copied},
			"oidc_identity":   {how: copied},
			"audit":           {how: copied},
			"key_version":     {how: transformed},
			"totp_used":       {how: transformed},
			"user_smb_secret": {how: transformed},
			"login_challenge": {how: discarded,
				why: "a login part way through is retried rather than resumed across a cutover"},
			"oidc_flow": {how: discarded,
				why: "an authorization exchange in flight is retried rather than resumed"},
		},
		aclFile: {
			"grant_": {how: copied},
			"acl_migration": {how: discarded,
				why: "the old build's own migration bookkeeping, not data"},
		},
		linksFile: {"share_link": {how: copied}},
		uploadFile: {
			"upload_sessions":       {how: copied},
			"upload_alias":          {how: copied},
			"upload_chunk_settings": {how: copied},
			"upload_touched_dirs":   {how: copied},
		},
		settingsFile: {"settings_overrides": {how: copied}},
		metaFile: {
			"node":      {how: transformed},
			"dav_prop":  {how: copied},
			"diretag":   {how: rebuildable},
			"share_gen": {how: rebuildable},
		},
		locksFile: {"dav_lock": {how: copied}},
		sharesFile: {
			"share_":                  {how: transformed},
			"share_identity_override": {how: copied},
			"share_trash_override":    {how: copied},
		},
		jobsFile: {
			"jobs":        {how: transformed},
			"job_results": {how: transformed},
		},
		indexFile: {"index_settings": {how: transformed}},
		compatFile: {
			"nc_favorite":     {how: copied},
			"nc_instance":     {how: deferred, phase: 10},
			"nc_login_flow":   {how: deferred, phase: 10},
			"nc_upload_alias": {how: deferred, phase: 10},
		},
		journalFile: {"write_event": {how: retained}},
	}
}

// checkInventory reads every source database's own schema, refuses anything
// this binary has not accounted for, and puts the deliberate discards in the
// report with the reason each one was given.
func (s *sources) checkInventory(ctx context.Context, rep *Report) error {
	all := inventory()
	for _, f := range knownFiles() {
		db := s.byName(f)
		if db == nil {
			continue
		}
		tables, err := listTables(ctx, db)
		if err != nil {
			return fmt.Errorf("reading the schema of %s: %w", f, err)
		}
		for _, table := range tables {
			d, known := all[f][table]
			if !known {
				return fmt.Errorf("%w: %s in %s. This binary would not carry it, and a "+
					"migration that drops a table nobody classified is silent data loss",
					ErrUnknownTable, table, f)
			}
			if d.how != deferred && d.how != discarded {
				continue
			}
			n, cerr := countRows(ctx, db, table)
			if cerr != nil {
				return fmt.Errorf("counting %s in %s: %w", table, f, cerr)
			}
			if n == 0 {
				continue
			}
			if d.how == deferred {
				return fmt.Errorf("%w: %s in %s holds %d. Phase %d is what adds its "+
					"destination; migrating now would report success while dropping them",
					ErrDeferredTableHasRows, table, f, n, d.phase)
			}
			rep.Dropped[Drop{Table: table, Reason: Reason(d.why)}] += n
		}
	}
	return nil
}

// listTables is the file's own account of itself, which is the only account
// that cannot be out of date.
func listTables(ctx context.Context, db *sql.DB) (out []string, err error) {
	rows, err := db.QueryContext(ctx, selUserTables)
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
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	slices.Sort(out)
	return out, nil
}

// countRows takes the table name from the inventory above and nowhere else, so
// the one statement in this package that is not a constant still cannot be
// reached by anything a caller supplies.
func countRows(ctx context.Context, db *sql.DB, table string) (int, error) {
	if !validTableName(table) {
		return 0, fmt.Errorf("refusing to count a table named %q", table)
	}
	// An int rather than an int64, so a count that does not fit is the driver's
	// refusal rather than a number that wrapped.
	var n int
	//nolint:gosec // the name is one of this file's own literals; see validTableName.
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+table+`"`).Scan(&n)
	return n, err
}

// validTableName is the guard that makes the one built statement above safe to
// read: every name it accepts is a plain identifier, and every name it is ever
// handed came out of the inventory literal.
func validTableName(table string) bool {
	if table == "" {
		return false
	}
	for _, r := range table {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return !strings.HasPrefix(table, "sqlite_")
}
