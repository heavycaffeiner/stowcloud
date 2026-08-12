package fromrust_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/fromrust"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"

	_ "modernc.org/sqlite" // the driver the fixtures are written with
)

// mkdb writes one of the databases the Rust build kept, with only the columns
// this import reads.
func mkdb(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("creating %s: %v", filepath.Base(path), err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("closing %s: %v", filepath.Base(path), cerr)
		}
	}()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(path), err, s)
		}
	}
}

// rustDir is a data directory as the old build left it, with one of every
// awkward row in it: an account that was deleted without its sessions being
// cleaned up, an audit entry pointing at it, a grant to a group, a share link
// and a dead property keyed by a node id, and an upload half way through.
func rustDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	mkdb(t, filepath.Join(dir, "auth.db"),
		`CREATE TABLE user (id INTEGER PRIMARY KEY, name TEXT, display TEXT, pw_hash TEXT,
		   totp_secret BLOB, disabled INTEGER, quota_bytes INTEGER, usage_bytes INTEGER,
		   created_ns INTEGER, smb_opt_out INTEGER, smb_enabled INTEGER, role INTEGER)`,
		`INSERT INTO user VALUES (1, 'alice', 'Alice', '$argon2id$a', X'5555', 0, NULL, 10, 100, 0, 1, 1)`,
		`INSERT INTO user VALUES (2, 'bob', NULL, '$argon2id$b', NULL, 0, 500, 0, 200, 0, 1, 0)`,
		`CREATE TABLE key_version (id INTEGER PRIMARY KEY, ver INTEGER)`,
		`INSERT INTO key_version VALUES (1, 3)`,
		`CREATE TABLE group_ (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO group_ VALUES (1, 'staff')`,
		`CREATE TABLE membership (user INTEGER, group_ INTEGER)`,
		`INSERT INTO membership VALUES (1, 1), (99, 1)`,
		`CREATE TABLE session (id_hash BLOB, user INTEGER, created_ns INTEGER, last_seen_ns INTEGER,
		   absolute_expiry_ns INTEGER, ip_first TEXT, ua_first TEXT, amr INTEGER)`,
		`INSERT INTO session VALUES (X'01', 1, 0, 0, 0, '10.0.0.1', 'ua', 1)`,
		`INSERT INTO session VALUES (X'02', 99, 0, 0, 0, NULL, NULL, 1)`,
		`CREATE TABLE app_password (id INTEGER PRIMARY KEY, token_hash BLOB, user INTEGER, name TEXT,
		   scope_perms INTEGER, scope_shares BLOB, created_ns INTEGER, last_used_ns INTEGER,
		   last_ip TEXT, last_ua TEXT, expires_ns INTEGER, wipe_requested INTEGER)`,
		`INSERT INTO app_password VALUES (1, X'aa', 1, 'phone', 65535, NULL, 0, NULL, NULL, NULL, NULL, 0)`,
		`CREATE TABLE recovery_code (user INTEGER, code_hash BLOB, used_ns INTEGER)`,
		`INSERT INTO recovery_code VALUES (1, X'bb', NULL)`,
		`CREATE TABLE oidc_identity (issuer TEXT, subject TEXT, user INTEGER, linked_ns INTEGER,
		   last_login_ns INTEGER)`,
		`INSERT INTO oidc_identity VALUES ('https://idp', 'sub-1', 1, 0, NULL)`,
		`CREATE TABLE audit (ts_ns INTEGER, actor INTEGER, event TEXT, target TEXT, ip TEXT,
		   ua TEXT, result INTEGER, detail TEXT)`,
		`INSERT INTO audit VALUES (1, 1, 'login', NULL, NULL, NULL, 0, NULL)`,
		`INSERT INTO audit VALUES (2, 99, 'login', NULL, NULL, NULL, 1, NULL)`,
	)

	mkdb(t, filepath.Join(dir, "acl.db"),
		`CREATE TABLE grant_ (id INTEGER PRIMARY KEY, principal_kind INTEGER, principal_id INTEGER,
		   share INTEGER, subpath TEXT, allow INTEGER, deny INTEGER, inherit INTEGER,
		   label TEXT, created_ns INTEGER)`,
		`INSERT INTO grant_ VALUES (1, 0, 1, 7, '', 3, 0, 1, NULL, 0)`,
		`INSERT INTO grant_ VALUES (2, 1, 1, 7, 'sub', 1, 0, 1, 'staff', 0)`,
		`INSERT INTO grant_ VALUES (3, 0, 99, 7, '', 1, 0, 1, NULL, 0)`,
	)

	mkdb(t, filepath.Join(dir, "links.db"),
		`CREATE TABLE share_link (id INTEGER PRIMARY KEY, token_hash BLOB, token_enc BLOB,
		   share INTEGER, path TEXT, fileid INTEGER, owner INTEGER, perms INTEGER,
		   password_hash TEXT, expires_ns INTEGER, max_downloads INTEGER, downloads INTEGER,
		   label TEXT, note TEXT, created_ns INTEGER)`,
		`INSERT INTO share_link VALUES (1, X'cc', NULL, 7, 'docs/a.txt', 5, 1, 1, NULL, NULL,
		   NULL, 0, NULL, NULL, 0)`,
	)

	mkdb(t, filepath.Join(dir, "meta.db"),
		`CREATE TABLE node (id INTEGER PRIMARY KEY, share INTEGER, parent INTEGER, name TEXT,
		   dev INTEGER, ino INTEGER, btime_ns INTEGER, flags INTEGER, size INTEGER, mtime_ns INTEGER)`,
		`INSERT INTO node VALUES (5, 7, 0, 'a.txt', 2, 3, 4, 0, 0, 0)`,
		`INSERT INTO node VALUES (6, 7, 0, 'b.txt', 2, 9, NULL, 0, 0, 0)`,
		`CREATE TABLE dav_prop (fileid INTEGER, ns TEXT, name TEXT, value TEXT)`,
		`INSERT INTO dav_prop VALUES (5, 'DAV:', 'colour', 'red')`,
		`INSERT INTO dav_prop VALUES (6, 'DAV:', 'colour', 'blue')`,
		`INSERT INTO dav_prop VALUES (404, 'DAV:', 'colour', 'gone')`,
	)

	mkdb(t, filepath.Join(dir, "compat-nc.db"),
		`CREATE TABLE nc_favorite (user INTEGER, fileid INTEGER)`,
		`INSERT INTO nc_favorite VALUES (1, 5), (1, 404), (99, 5)`,
	)

	// received is the old run-length form: two runs, [0,10) and [20,30).
	mkdb(t, filepath.Join(dir, "upload.db"),
		`CREATE TABLE upload_sessions (id BLOB PRIMARY KEY, user INTEGER, share INTEGER, dest TEXT,
		   part_name TEXT, spool_dir TEXT, mode INTEGER, total_len INTEGER, chunk_size INTEGER,
		   random_access INTEGER, received BLOB, next_name INTEGER, write_head INTEGER,
		   spooled_names BLOB, if_match TEXT, filename TEXT, mtime_ns INTEGER, mime TEXT,
		   relative_path TEXT, verify INTEGER, verify_digest BLOB, created_ns INTEGER,
		   expires_ns INTEGER, state INTEGER)`,
		`INSERT INTO upload_sessions VALUES (X'aa', 1, 7, 'd', 'p', NULL, 0, 100, 1024, 0,
		   X'02000A0A0A', 1, 30, X'', NULL, 'f.bin', NULL, NULL, NULL, NULL, NULL, 0, 0, 0)`,
		`INSERT INTO upload_sessions VALUES (X'bb', 99, 7, 'd', 'p', NULL, 0, 100, 1024, 0,
		   X'', 1, 0, X'', NULL, 'g.bin', NULL, NULL, NULL, NULL, NULL, 0, 0, 0)`,
	)

	mkdb(t, filepath.Join(dir, "settings.db"),
		`CREATE TABLE settings_overrides (id INTEGER PRIMARY KEY, json TEXT)`,
		`INSERT INTO settings_overrides VALUES (1, '{"a":1}')`,
	)
	return dir
}

// openState re-opens what the import wrote.
func openState(t *testing.T, dir string) *state.DB {
	t.Helper()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dir, store.StateFile)))
	if err != nil {
		t.Fatalf("opening the imported state: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	return state.New(f)
}

func count(t *testing.T, d *state.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.SQL().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestImportCarriesTheDurableHalf(t *testing.T) {
	dir := rustDir(t)
	rep, err := fromrust.Import(context.Background(), dir)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	for _, tc := range []struct {
		what  string
		query string
		want  int
	}{
		{"accounts", `SELECT count(*) FROM user`, 2},
		{"groups", `SELECT count(*) FROM "group"`, 1},
		{"memberships", `SELECT count(*) FROM membership`, 1},
		{"sessions", `SELECT count(*) FROM session`, 1},
		{"app passwords", `SELECT count(*) FROM app_password`, 1},
		{"recovery codes", `SELECT count(*) FROM recovery_code`, 1},
		{"oidc links", `SELECT count(*) FROM oidc_link`, 1},
		{"audit rows", `SELECT count(*) FROM audit`, 2},
		{"grants", `SELECT count(*) FROM "grant"`, 2},
		{"share links", `SELECT count(*) FROM share_link`, 1},
		{"settings", `SELECT count(*) FROM settings`, 1},
		{"upload sessions", `SELECT count(*) FROM upload_session`, 1},
		{"upload intervals", `SELECT count(*) FROM upload_interval`, 2},
		{"dead properties", `SELECT count(*) FROM dav_prop`, 2},
		{"favorites", `SELECT count(*) FROM favorite`, 1},
	} {
		if got := count(t, d, tc.query); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.what, got, tc.want)
		}
	}

	if rep.Copied["user"] != 2 || rep.Dropped["session"] != 1 {
		t.Errorf("the report says %+v and %+v", rep.Copied, rep.Dropped)
	}
}

// The TOTP secret moves out of the user row into the table that holds it now,
// and it keeps the key version it was sealed under: re-sealing is the master
// key's own rotation and not an import's business.
func TestImportLiftsTheTotpSecretOutOfTheUserRow(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	var (
		user, keyVer int64
		secret       []byte
	)
	if err := d.SQL().QueryRowContext(context.Background(),
		`SELECT user, secret_ct, key_ver FROM totp_secret`).Scan(&user, &secret, &keyVer); err != nil {
		t.Fatalf("reading the secret: %v", err)
	}
	if user != 1 || keyVer != 3 || len(secret) != 2 {
		t.Errorf("secret for user %d under key version %d, %d bytes", user, keyVer, len(secret))
	}
}

// A grant's principal becomes the column a foreign key can be written against,
// and one whose account is gone does not come across at all.
func TestImportSplitsTheGrantPrincipal(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	if n := count(t, d, `SELECT count(*) FROM "grant" WHERE user = 1 AND "group" IS NULL`); n != 1 {
		t.Errorf("%d grants to the user, want 1", n)
	}
	if n := count(t, d, `SELECT count(*) FROM "grant" WHERE "group" = 1 AND user IS NULL`); n != 1 {
		t.Errorf("%d grants to the group, want 1", n)
	}
}

// The rows that pointed at a node id now carry the file's identity, read out
// of the cache on the way past. The cache itself is not imported.
func TestImportTranslatesNodeIDsIntoIdentities(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)
	if _, err := fromrust.Import(ctx, dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	var share, dev, ino, present, btime int64
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT share, dev, ino, btime_present, btime_ns FROM dav_prop WHERE value = 'red'`).
		Scan(&share, &dev, &ino, &present, &btime); err != nil {
		t.Fatalf("reading the property: %v", err)
	}
	if share != 7 || dev != 2 || ino != 3 || present != 1 || btime != 4 {
		t.Errorf("identity (%d, %d, %d, %d, %d)", share, dev, ino, present, btime)
	}

	// The file with no birth time keeps the difference.
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT btime_present, btime_ns FROM dav_prop WHERE value = 'blue'`).
		Scan(&present, &btime); err != nil {
		t.Fatalf("reading the second property: %v", err)
	}
	if present != 0 || btime != 0 {
		t.Errorf("a file with no birth time came across as present=%d, btime=%d", present, btime)
	}

	// And the share link follows the same file.
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT dev, ino, btime_present, btime_ns FROM share_link WHERE id = 1`).
		Scan(&dev, &ino, &present, &btime); err != nil {
		t.Fatalf("reading the link: %v", err)
	}
	if dev != 2 || ino != 3 || present != 1 || btime != 4 {
		t.Errorf("the link points at (%d, %d, %d, %d)", dev, ino, present, btime)
	}

	if _, err := os.Stat(filepath.Join(dir, store.CacheFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the import wrote a cache, which is the one thing it does not carry")
	}
}

// An in-flight upload keeps what has arrived, as rows rather than as a blob.
func TestImportUnpacksTheIntervalSet(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)
	if _, err := fromrust.Import(ctx, dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	rows, err := d.SQL().QueryContext(ctx, `SELECT lo, hi FROM upload_interval ORDER BY lo`)
	if err != nil {
		t.Fatalf("reading intervals: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var got [][2]int64
	for rows.Next() {
		var lo, hi int64
		if serr := rows.Scan(&lo, &hi); serr != nil {
			t.Fatalf("scanning: %v", serr)
		}
		got = append(got, [2]int64{lo, hi})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading intervals: %v", err)
	}
	want := [][2]int64{{0, 10}, {20, 30}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("intervals %v, want %v", got, want)
	}
}

// The old files are what an operator rolls back to, so the import may not
// leave a mark on them.
func TestImportLeavesTheOldFilesAlone(t *testing.T) {
	dir := rustDir(t)
	before, err := os.ReadFile(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("reading auth.db: %v", err)
	}
	if _, ierr := fromrust.Import(context.Background(), dir); ierr != nil {
		t.Fatalf("Import: %v", ierr)
	}
	after, err := os.ReadFile(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("re-reading auth.db: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("auth.db went from %d bytes to %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("auth.db changed at byte %d", i)
		}
	}
}

// This is not a merge, and a second run against a directory that already has
// one is far more likely to be a mistake than an intention.
func TestImportRefusesAnExistingState(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir); err != nil {
		t.Fatalf("the first import: %v", err)
	}
	if _, err := fromrust.Import(context.Background(), dir); !errors.Is(err, fromrust.ErrStateExists) {
		t.Fatalf("the second import returned %v, want ErrStateExists", err)
	}
}

// A directory that is not one of ours says so, rather than writing an empty
// state.db and reporting success.
func TestImportRefusesADirectoryWithNoAuthDatabase(t *testing.T) {
	if _, err := fromrust.Import(context.Background(), t.TempDir()); err == nil {
		t.Fatal("an empty directory was imported")
	}
}
