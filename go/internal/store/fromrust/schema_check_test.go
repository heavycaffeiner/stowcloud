package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	_ "modernc.org/sqlite" // the driver the fixture below is written with
	"strings"
	"testing"
)

// The importer's statements, checked against a schema this package did not
// write.
//
// Every fixture in this package's other tests is hand-written, which means a
// fixture and the statement that reads it can agree with each other and both
// be wrong. That happened: the share table was read with a column name the
// Rust build never had, and the fixture declared the same wrong name, so the
// suite was green and the first real migration failed at the first row.
//
// The schema below is transcribed from the databases the Rust build actually
// creates on a first run. It is checked in rather than generated, because a
// generated one needs that build present and the point is to catch a drift
// between the two trees without one.

// rustSchema is every table the Rust build creates, with its real columns.
//
// Regenerate by starting the Rust server against an empty directory and
// reading each database's own schema. Any addition there must appear here and
// in the inventory, or the migration drops a table nobody classified.
func rustSchema() map[string]map[string][]string {
	return map[string]map[string][]string{
		"acl.db": {
			"acl_migration": {"key", "done_ns"},
			"grant_":        {"id", "principal_kind", "principal_id", "share", "subpath", "allow", "deny", "inherit", "label", "created_ns"},
		},
		"auth.db": {
			"app_password":    {"id", "token_hash", "user", "name", "scope_perms", "scope_shares", "created_ns", "last_used_ns", "last_ip", "last_ua", "expires_ns", "wipe_requested"},
			"audit":           {"ts_ns", "actor", "event", "target", "ip", "ua", "result", "detail"},
			"group_":          {"id", "name"},
			"key_version":     {"id", "ver"},
			"login_challenge": {"token_hash", "user", "expires_ns", "amr"},
			"membership":      {"user", "group_"},
			"oidc_flow":       {"state_hash", "binding_hash", "nonce_hash", "code_verifier", "mode", "link_user", "return_to", "created_ns", "expires_ns"},
			"oidc_identity":   {"issuer", "subject", "user", "linked_ns", "last_login_ns"},
			"recovery_code":   {"user", "code_hash", "used_ns"},
			"session":         {"id_hash", "user", "created_ns", "last_seen_ns", "absolute_expiry_ns", "ip_first", "ua_first", "amr"},
			"totp_used":       {"user", "time_step"},
			"user":            {"id", "name", "display", "pw_hash", "totp_secret", "disabled", "quota_bytes", "usage_bytes", "created_ns", "smb_opt_out", "smb_enabled", "role"},
			"user_smb_secret": {"user", "nt_hash_ct", "key_ver", "source", "updated_ns"},
		},
		"dav-locks.db": {
			"dav_lock": {"token", "fileid", "share", "path", "principal", "owner", "depth", "scope", "expires_ns", "timeout_s"},
		},
		"index.db": {
			"index_settings": {"id", "name_enabled"},
		},
		"jobs.db": {
			"job_results": {"job_id", "seq", "path", "status", "error", "will_copy"},
			"jobs":        {"id", "owner", "kind", "state", "done", "total", "current", "created_at", "updated_at"},
		},
		"journal.db": {
			"write_event": {"user", "share", "path", "op", "at_ns"},
		},
		"links.db": {
			"share_link": {"id", "token_hash", "token_enc", "share", "path", "fileid", "owner", "perms", "password_hash", "expires_ns", "max_downloads", "downloads", "label", "note", "created_ns"},
		},
		"meta.db": {
			"dav_prop":  {"fileid", "ns", "name", "value"},
			"diretag":   {"share", "fileid", "etag", "rsize", "rcount", "gen", "valid"},
			"node":      {"id", "share", "parent", "name", "dev", "ino", "btime_ns", "flags", "size", "mtime_ns"},
			"share_gen": {"share", "gen"},
		},
		"settings.db": {
			"settings_overrides": {"id", "json"},
		},
		"shares.db": {
			"share_":                  {"id", "name", "host_path", "created_ns"},
			"share_identity_override": {"share_id", "name", "host_path"},
			"share_trash_override":    {"share_id", "enabled"},
		},
		"upload.db": {
			"upload_alias":          {"tid", "user", "session", "share", "dest", "created_ns"},
			"upload_chunk_settings": {"id", "chunk_min", "chunk_default"},
			"upload_sessions":       {"id", "user", "share", "dest", "part_name", "spool_dir", "mode", "total_len", "chunk_size", "random_access", "received", "next_name", "write_head", "spooled_names", "if_match", "filename", "mtime_ns", "mime", "relative_path", "verify", "verify_digest", "created_ns", "expires_ns", "state", "chunk_min_at_creation"},
			"upload_touched_dirs":   {"share", "dir"},
		},
	}
}

// TestTheImporterReadsColumnsTheRustBuildHas compares every column named in a
// SELECT against the real schema.
//
// This is the check that would have caught the share table's wrong column
// name, and it catches it without the Rust build being present.
func TestTheImporterReadsColumnsTheRustBuildHas(t *testing.T) {
	statements, err := os.ReadFile(filepath.Clean("sql.go"))
	if err != nil {
		t.Fatalf("reading the statements: %v", err)
	}

	// Which file each table lives in, inverted from the schema above.
	owner := map[string]map[string][]string{}
	for _, tables := range rustSchema() {
		for table, cols := range tables {
			owner[table] = map[string][]string{"cols": cols}
		}
	}

	selects := regexp.MustCompile(`(?is)SELECT\s+(.+?)\s+FROM\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	for _, m := range selects.FindAllStringSubmatch(string(statements), -1) {
		rawCols, table := m[1], m[2]
		known, ok := owner[table]
		if !ok {
			// A table this schema does not describe: either one the Go build
			// writes itself, or one whose columns are not transcribed here.
			continue
		}
		have := map[string]bool{}
		for _, c := range known["cols"] {
			have[c] = true
		}
		for _, raw := range strings.Split(rawCols, ",") {
			col := strings.Trim(strings.Fields(strings.TrimSpace(raw))[0], `"`)
			if !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`).MatchString(col) {
				continue
			}
			switch strings.ToUpper(col) {
			case "DISTINCT", "COUNT", "MAX", "MIN", "CASE", "NULL":
				continue
			}
			if !have[col] {
				t.Errorf("the importer reads %s.%s, which the Rust build does not have; it has %v",
					table, col, known["cols"])
			}
		}
	}
}

// Every table in the real schema has a recorded disposition. A table nobody
// classified is dropped silently, which is the data loss the inventory exists
// to prevent.
func TestEveryRealTableHasADisposition(t *testing.T) {
	all := inventory()
	for file, tables := range rustSchema() {
		for table := range tables {
			if _, ok := all[file][table]; !ok {
				t.Errorf("%s in %s has no recorded disposition", table, file)
			}
		}
	}
}

// And the check refuses a table it has never heard of, rather than carrying on
// and dropping it.
func TestAnUnclassifiedTableBlocksTheImport(t *testing.T) {
	dir := t.TempDir()
	// The importer refuses a directory with no account database at all, so
	// the fixture is a real one plus the surprise.
	makeDB(t, filepath.Join(dir, "auth.db"),
		`CREATE TABLE user (id INTEGER PRIMARY KEY, name TEXT NOT NULL, display TEXT,
		   pw_hash TEXT, totp_secret BLOB, disabled INTEGER NOT NULL DEFAULT 0,
		   quota_bytes INTEGER, usage_bytes INTEGER, created_ns INTEGER NOT NULL DEFAULT 0,
		   smb_opt_out INTEGER NOT NULL DEFAULT 0, smb_enabled INTEGER NOT NULL DEFAULT 1,
		   role TEXT)`,
	)
	makeDB(t, filepath.Join(dir, "settings.db"),
		`CREATE TABLE settings_overrides (id INTEGER PRIMARY KEY, json TEXT NOT NULL)`,
		// A table a future Rust build might add and this one has never seen.
		`CREATE TABLE something_new (id INTEGER PRIMARY KEY)`,
	)

	s, err := openSources(dir)
	if err != nil {
		t.Fatalf("openSources: %v", err)
	}
	defer s.close()

	rep := &Report{Dropped: map[Drop]int{}}
	cerr := s.checkInventory(context.Background(), rep)
	if !errors.Is(cerr, ErrUnknownTable) {
		t.Fatalf("an unclassified table gave %v, want a refusal", cerr)
	}
	if !strings.Contains(cerr.Error(), "something_new") {
		t.Errorf("the refusal does not name the table: %v", cerr)
	}
}

// makeDB writes a source database with the given statements. This package's
// other tests are an external test package and cannot lend theirs.
func makeDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("closing %s: %v", path, cerr)
		}
	}()
	for _, stmt := range stmts {
		if _, eerr := db.Exec(stmt); eerr != nil {
			t.Fatalf("%s: %v", stmt, eerr)
		}
	}
}
