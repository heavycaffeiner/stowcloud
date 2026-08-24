package state_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

func open(t *testing.T) *state.DB {
	t.Helper()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	return state.New(f)
}

// exec runs one statement in the write path and reports what it said.
func exec(t *testing.T, d *state.DB, stmt string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, stmt, args...)
		return err
	})
}

func addUser(t *testing.T, d *state.DB, id int, name string) {
	t.Helper()
	if err := exec(t, d,
		`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, 'x', 0)`, id, name); err != nil {
		t.Fatalf("adding user %d: %v", id, err)
	}
}

// Every table the durable half is supposed to hold, by name. A table that
// quietly failed to be created is a phase that quietly did not land.
func TestEveryDurableTableExists(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	want := []string{
		"app_password", "audit", "compat_kv", "compat_login_flow",
		"compat_upload_alias", "dav_lock", "dav_prop", "favorite",
		"fileid_override", "grant", "group", "key_version", "membership",
		"oidc_flow", "oidc_link", "operation", "operation_result", "recovery_code",
		"session", "settings", "share_definition", "share_identity_override",
		"share_link", "share_trash_override", "totp_secret", "totp_used",
		"upload_alias", "upload_chunk_settings", "upload_interval", "upload_session",
		"upload_touched_dir", "user", "user_smb_secret",
	}
	rows, err := d.SQL().QueryContext(ctx,
		`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var have []string
	for rows.Next() {
		var name string
		if serr := rows.Scan(&name); serr != nil {
			t.Fatalf("scanning: %v", serr)
		}
		if name != "schema_version" {
			have = append(have, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	slices.Sort(have)

	for _, w := range want {
		if !slices.Contains(have, w) {
			t.Errorf("%s is missing", w)
		}
	}
	for _, h := range have {
		if !slices.Contains(want, h) {
			t.Errorf("%s is in the database and not in the list this phase agreed to", h)
		}
	}
}

// foreign_keys is per-connection and SQLite defaults it off, so "we set it" is
// only worth something if a row that violates one is actually refused.
func TestForeignKeysAreEnforced(t *testing.T) {
	d := open(t)
	err := exec(t, d,
		`INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, amr)
		 VALUES (X'00', 999, 0, 0, 0, 0)`)
	if err == nil {
		t.Fatal("a session for a user that does not exist was accepted")
	}
}

// Deleting an account takes its credentials with it, and leaves the audit
// trail standing: a record of what an account did is not the account.
func TestDeletingAUserCascadesButKeepsTheAudit(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	addUser(t, d, 1, "alice")

	if err := exec(t, d,
		`INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, amr)
		 VALUES (X'01', 1, 0, 0, 0, 0)`); err != nil {
		t.Fatalf("adding a session: %v", err)
	}
	if err := exec(t, d,
		`INSERT INTO audit(ts_ns, actor, event, result) VALUES (0, 1, 'login', 0)`); err != nil {
		t.Fatalf("adding an audit row: %v", err)
	}
	if err := exec(t, d, `DELETE FROM user WHERE id = 1`); err != nil {
		t.Fatalf("deleting the user: %v", err)
	}

	var sessions, audits int
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM session`).Scan(&sessions); err != nil {
		t.Fatalf("counting sessions: %v", err)
	}
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM audit`).Scan(&audits); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if sessions != 0 {
		t.Errorf("%d sessions survived the account", sessions)
	}
	if audits != 1 {
		t.Errorf("%d audit rows survived the account, want 1", audits)
	}
}

// A grant belongs to exactly one principal. Both columns set, or neither, is a
// grant nothing can evaluate.
func TestAGrantHasExactlyOnePrincipal(t *testing.T) {
	d := open(t)
	addUser(t, d, 1, "alice")
	if err := exec(t, d, `INSERT INTO "group"(id, name) VALUES (1, 'staff')`); err != nil {
		t.Fatalf("adding a group: %v", err)
	}

	const stmt = `
INSERT INTO "grant"(id, user, "group", share, subpath, allow, deny, inherit, created_ns)
VALUES (?, ?, ?, 1, '', 1, 0, 1, 0)`

	if err := exec(t, d, stmt, 1, 1, nil); err != nil {
		t.Errorf("a grant to a user was refused: %v", err)
	}
	if err := exec(t, d, stmt, 2, nil, 1); err != nil {
		t.Errorf("a grant to a group was refused: %v", err)
	}
	if err := exec(t, d, stmt, 3, 1, 1); err == nil {
		t.Error("a grant naming both a user and a group was accepted")
	}
	if err := exec(t, d, stmt, 4, nil, nil); err == nil {
		t.Error("a grant naming no principal was accepted")
	}
}

// The rows that used to key by a node id key by the file's identity, and a
// filesystem with no birth time is one of the cases they have to hold.
func TestIdentityKeyedRowsHoldAFileWithNoBirthTime(t *testing.T) {
	d := open(t)
	addUser(t, d, 1, "alice")

	if err := exec(t, d,
		`INSERT INTO dav_prop(share, dev, ino, btime_present, btime_ns, ns, name, value)
		 VALUES (1, 2, 3, 0, 0, 'DAV:', 'x', 'v')`); err != nil {
		t.Fatalf("a dead property on a file with no birth time: %v", err)
	}
	if err := exec(t, d,
		`INSERT INTO favorite(user, share, dev, ino, btime_present, btime_ns)
		 VALUES (1, 1, 2, 3, 0, 0)`); err != nil {
		t.Fatalf("a favorite on a file with no birth time: %v", err)
	}
	// And the same file with a zero birth time is a different row, not a
	// conflict.
	if err := exec(t, d,
		`INSERT INTO favorite(user, share, dev, ino, btime_present, btime_ns)
		 VALUES (1, 1, 2, 3, 1, 0)`); err != nil {
		t.Fatalf("a zero birth time collided with an absent one: %v", err)
	}
}

// The settings table is one row by construction rather than by discipline.
func TestSettingsHoldsOneRow(t *testing.T) {
	d := open(t)
	if err := exec(t, d, `INSERT INTO settings(id, json) VALUES (1, '{}')`); err != nil {
		t.Fatalf("the first settings row: %v", err)
	}
	if err := exec(t, d, `INSERT INTO settings(id, json) VALUES (2, '{}')`); err == nil {
		t.Error("a second settings row was accepted")
	}
}

// An interval belongs to a session, and goes when the session does.
func TestUploadIntervalsBelongToASession(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	addUser(t, d, 1, "alice")

	if err := exec(t, d,
		`INSERT INTO upload_session(id, user, share, dest, part_name, mode, chunk_size,
		   random_access, filename, created_ns, expires_ns, state)
		 VALUES (X'aa', 1, 1, 'd', 'p', 0, 1024, 0, 'f', 0, 0, 0)`); err != nil {
		t.Fatalf("adding an upload session: %v", err)
	}
	if err := exec(t, d, `INSERT INTO upload_interval(session, lo, hi) VALUES (X'aa', 0, 10)`); err != nil {
		t.Fatalf("adding an interval: %v", err)
	}
	if err := exec(t, d, `INSERT INTO upload_interval(session, lo, hi) VALUES (X'bb', 0, 10)`); err == nil {
		t.Error("an interval for a session that does not exist was accepted")
	}

	if err := exec(t, d, `DELETE FROM upload_session WHERE id = X'aa'`); err != nil {
		t.Fatalf("deleting the session: %v", err)
	}
	var n int
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM upload_interval`).Scan(&n); err != nil {
		t.Fatalf("counting intervals: %v", err)
	}
	if n != 0 {
		t.Errorf("%d intervals outlived their session", n)
	}
}
