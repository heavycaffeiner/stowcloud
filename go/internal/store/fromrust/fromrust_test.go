package fromrust_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/fromrust"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"

	_ "modernc.org/sqlite" // the driver the fixtures are written with
)

// nowNs is what the fixtures are dated against, so "expired" and "active" are
// facts about the data rather than about when the suite runs.
const nowNs int64 = 1_700_000_000_000_000_000

func testClock() clock.Clock { return clock.Fixed(time.Unix(0, nowNs)) }

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

	// One lock a client is still holding and one that lapsed. expires_ns is
	// text in this schema, which is why the import parses it.
	mkdb(t, filepath.Join(dir, "dav-locks.db"),
		`CREATE TABLE dav_lock (token TEXT PRIMARY KEY, fileid INTEGER NOT NULL, share INTEGER NOT NULL,
		   path TEXT NOT NULL, principal INTEGER NOT NULL, owner TEXT NOT NULL, depth INTEGER NOT NULL,
		   scope INTEGER NOT NULL, expires_ns TEXT NOT NULL, timeout_s INTEGER NOT NULL)`,
		`INSERT INTO dav_lock VALUES ('held', 5, 7, 'docs/a.txt', 1, 'alice', 0, 0,
		   '`+strconv.FormatInt(nowNs+60_000_000_000, 10)+`', 60)`,
		`INSERT INTO dav_lock VALUES ('lapsed', 6, 7, 'docs/b.txt', 1, 'alice', 0, 0,
		   '`+strconv.FormatInt(nowNs-1, 10)+`', 60)`,
	)

	// The Phase 4 durable sources. One admin-created share (whose id the
	// dynamic-share base is added to at registration, never here), an override
	// for a config-defined share, a trash override, and a running job plus its
	// results. A second job belongs to a user the import refuses to carry, so
	// it is dropped with a reason.
	mkdb(t, filepath.Join(dir, "shares.db"),
		`CREATE TABLE share_ (id INTEGER PRIMARY KEY, name TEXT NOT NULL, host_path TEXT NOT NULL,
		   created_at INTEGER NOT NULL)`,
		`INSERT INTO share_ VALUES (7, 'photos', '/srv/photos', 100)`,
		`CREATE TABLE share_identity_override (share_id INTEGER PRIMARY KEY, name TEXT NOT NULL,
		   host_path TEXT NOT NULL)`,
		`INSERT INTO share_identity_override VALUES (2, 'renamed', '/srv/renamed')`,
		`CREATE TABLE share_trash_override (share_id INTEGER PRIMARY KEY, enabled INTEGER NOT NULL)`,
		`INSERT INTO share_trash_override VALUES (2, 1)`,
	)

	mkdb(t, filepath.Join(dir, "jobs.db"),
		`CREATE TABLE jobs (id TEXT PRIMARY KEY, owner INTEGER NOT NULL, kind TEXT NOT NULL,
		   state TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL,
		   current TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO jobs VALUES ('j1', 1, 'copy', 'running', 2, 4, 'b.txt', 100, 150)`,
		`INSERT INTO jobs VALUES ('j2', 99, 'copy', 'running', 0, 2, NULL, 100, 100)`,
		`CREATE TABLE job_results (job_id TEXT NOT NULL, seq INTEGER NOT NULL, path TEXT NOT NULL,
		   status TEXT NOT NULL, error TEXT, will_copy INTEGER NOT NULL DEFAULT 0,
		   PRIMARY KEY (job_id, seq))`,
		`INSERT INTO job_results VALUES ('j1', 0, 'a.txt', 'ok', NULL, 0)`,
		`INSERT INTO job_results VALUES ('j1', 1, 'b.txt', 'failed', 'some lower-layer prose', 1)`,
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
	rep, err := fromrust.Import(context.Background(), dir, testClock())
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
		{"webdav locks", `SELECT count(*) FROM dav_lock`, 1},
		{"persisted shares", `SELECT count(*) FROM share_definition`, 1},
		{"identity overrides", `SELECT count(*) FROM share_identity_override`, 1},
		{"trash overrides", `SELECT count(*) FROM share_trash_override`, 1},
		{"operations", `SELECT count(*) FROM operation`, 1},
		{"operation results", `SELECT count(*) FROM operation_result`, 2},
	} {
		if got := count(t, d, tc.query); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.what, got, tc.want)
		}
	}

	if rep.Copied["user"] != 2 {
		t.Errorf("the report says %+v", rep.Copied)
	}
}

// A drop is a data-loss boundary, so the report says which table lost rows and
// why. The fixture drops rows for four different reasons and the previous
// report called every one of them an unknown account.
func TestTheReportNamesTheRealReason(t *testing.T) {
	dir := rustDir(t)
	rep, err := fromrust.Import(context.Background(), dir, testClock())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	for _, want := range []fromrust.Drop{
		{Table: "session", Reason: fromrust.ReasonUnknownUser},
		{Table: "membership", Reason: fromrust.ReasonUnknownUser},
		{Table: "grant", Reason: fromrust.ReasonUnknownUser},
		{Table: "dav_prop", Reason: fromrust.ReasonMissingNode},
		{Table: "favorite", Reason: fromrust.ReasonMissingNode},
		{Table: "favorite", Reason: fromrust.ReasonUnknownUser},
		{Table: "upload_session", Reason: fromrust.ReasonUnknownUser},
		{Table: "dav_lock", Reason: fromrust.ReasonExpired},
	} {
		if rep.Dropped[want] != 1 {
			t.Errorf("%s / %q: %d rows, want 1", want.Table, want.Reason, rep.Dropped[want])
		}
	}

	var out bytes.Buffer
	if werr := rep.Write(&out); werr != nil {
		t.Fatalf("writing the report: %v", werr)
	}
	if strings.Contains(out.String(), "WebDAV locks, which expire") {
		t.Error("the report still claims locks are not imported")
	}
	for _, phrase := range []string{
		"the metadata cache holds no row",
		"they had already expired",
	} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("the report does not say %q:\n%s", phrase, out.String())
		}
	}
}

// A lock the client still holds survives the cutover, keyed by the file's
// identity rather than by the node id the old row named.
func TestImportKeepsAnActiveLock(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)
	if _, err := fromrust.Import(ctx, dir, testClock()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	var (
		token, path              string
		dev, ino, present, btime int64
		expires                  int64
	)
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT token, path, dev, ino, btime_present, btime_ns, expires_ns FROM dav_lock`).
		Scan(&token, &path, &dev, &ino, &present, &btime, &expires); err != nil {
		t.Fatalf("reading the lock: %v", err)
	}
	if token != "held" || path != "docs/a.txt" {
		t.Errorf("the surviving lock is %s on %s", token, path)
	}
	if dev != 2 || ino != 3 || present != 1 || btime != 4 {
		t.Errorf("the lock points at (%d, %d, %d, %d)", dev, ino, present, btime)
	}
	if expires != nowNs+60_000_000_000 {
		t.Errorf("the expiry came across as %d", expires)
	}
}

// An active lock whose file the cache cannot resolve stops the import. Dropping
// it would open the write window the lock exists to close, at the one moment
// nobody is watching for it.
func TestImportRefusesAnUnresolvableActiveLock(t *testing.T) {
	dir := rustDir(t)
	mkdb(t, filepath.Join(dir, "dav-locks.db"),
		`UPDATE dav_lock SET fileid = 404 WHERE token = 'held'`)

	_, err := fromrust.Import(context.Background(), dir, testClock())
	if err == nil {
		t.Fatal("an active lock with no resolvable file was imported")
	}
	if !strings.Contains(err.Error(), "held") {
		t.Errorf("the refusal does not name the lock: %v", err)
	}
}

// The TOTP secret moves out of the user row into the table that holds it now,
// and it keeps the key version it was sealed under: re-sealing is the master
// key's own rotation and not an import's business.
func TestImportLiftsTheTotpSecretOutOfTheUserRow(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
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
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
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
	if _, err := fromrust.Import(ctx, dir, testClock()); err != nil {
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

// A Rust link with no file id is a link on the share root, and it is the one
// legitimate path-only representation. It must not be fabricated into a tuple.
func TestImportKeepsAPathOnlyLinkPathOnly(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)
	mkdb(t, filepath.Join(dir, "links.db"),
		`INSERT INTO share_link VALUES (2, X'ee', X'ff', 7, '', NULL, 1, 1, NULL, NULL,
		   NULL, 0, NULL, NULL, 0)`)

	if _, err := fromrust.Import(ctx, dir, testClock()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)

	var dev, ino, present, btime, keyVer *int64
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT dev, ino, btime_present, btime_ns, token_key_ver FROM share_link WHERE id = 2`).
		Scan(&dev, &ino, &present, &btime, &keyVer); err != nil {
		t.Fatalf("reading the link: %v", err)
	}
	if dev != nil || ino != nil || present != nil || btime != nil {
		t.Error("a link with no file id came across carrying an identity")
	}
	// The ciphertext the Rust build sealed has no version in its AAD, and zero
	// is the name for that state rather than an absent column.
	if keyVer == nil || *keyVer != 0 {
		t.Errorf("token_key_ver is %v, want 0 beside the ciphertext", keyVer)
	}
}

// A link that names a file the cache cannot resolve stops the import. Making it
// path-only would hand public access to whatever is created at that path next,
// under a token somebody already has.
func TestImportRefusesAnUnresolvableLinkTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup string
		says  string
	}{
		{
			"a node that is gone",
			`UPDATE share_link SET fileid = 404 WHERE id = 1`,
			"404",
		},
		{
			"a node with no birth time",
			`UPDATE share_link SET fileid = 6 WHERE id = 1`,
			"birth time",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := rustDir(t)
			mkdb(t, filepath.Join(dir, "links.db"), tc.setup)

			_, err := fromrust.Import(context.Background(), dir, testClock())
			if err == nil {
				t.Fatal("the link was imported")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// An in-flight upload keeps what has arrived, as rows rather than as a blob.
func TestImportUnpacksTheIntervalSet(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)
	if _, err := fromrust.Import(ctx, dir, testClock()); err != nil {
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
	if _, ierr := fromrust.Import(context.Background(), dir, testClock()); ierr != nil {
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
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
		t.Fatalf("the first import: %v", err)
	}
	if _, err := fromrust.Import(context.Background(), dir, testClock()); !errors.Is(err, fromrust.ErrStateExists) {
		t.Fatalf("the second import returned %v, want ErrStateExists", err)
	}
}

// A directory that is not one of ours says so, rather than writing an empty
// state.db and reporting success.
func TestImportRefusesADirectoryWithNoAuthDatabase(t *testing.T) {
	if _, err := fromrust.Import(context.Background(), t.TempDir(), testClock()); err == nil {
		t.Fatal("an empty directory was imported")
	}
}

// The import publishes with one rename, so a run that fails part way leaves
// nothing behind and the operator can simply run it again. A half-written
// state.db that exists is worse than none: it blocks the retry that would fix
// it.
func TestAFailedImportLeavesNothingAndIsRetryable(t *testing.T) {
	ctx := context.Background()
	dir := rustDir(t)

	// A grant whose principal kind is neither a user nor a group, which the
	// import refuses rather than guesses at, half way through the copy.
	mkdb(t, filepath.Join(dir, "acl.db"),
		`INSERT INTO grant_ VALUES (4, 7, 1, 7, '', 1, 0, 1, NULL, 0)`)

	if _, err := fromrust.Import(ctx, dir, testClock()); err == nil {
		t.Fatal("a row the import cannot read was imported anyway")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), store.StateFile) {
			t.Errorf("a failed import left %s behind", e.Name())
		}
	}

	// With the bad row gone the same directory imports, which is what
	// "retryable" means.
	mkdb(t, filepath.Join(dir, "acl.db"), `DELETE FROM grant_ WHERE id = 4`)
	if _, err := fromrust.Import(ctx, dir, testClock()); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	d := openState(t, dir)
	if n := count(t, d, `SELECT count(*) FROM "grant"`); n != 2 {
		t.Errorf("the retry imported %d grants, want 2", n)
	}
}

// What the rename publishes is the whole database: closing it first
// checkpoints the write-ahead log back into the file, so nothing is left in a
// sidecar the rename does not carry.
func TestTheImportPublishesOneFile(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	var stateFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), store.StateFile) {
			stateFiles = append(stateFiles, e.Name())
		}
	}
	if len(stateFiles) != 1 || stateFiles[0] != store.StateFile {
		t.Errorf("the import left %v, want just %s", stateFiles, store.StateFile)
	}
}
