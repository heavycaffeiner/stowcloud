package fromrust_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/fromrust"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// The source is several independent WAL databases with nothing to snapshot
// across them, so an import taken while a server is running can combine a user
// from one instant with the grants from another. The lock is what makes "stop
// the server first" a refusal rather than a sentence in a runbook.
func TestImportRefusesWhileTheDirectoryIsInUse(t *testing.T) {
	dir := rustDir(t)

	held, err := store.LockInstance(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	_, err = fromrust.Import(context.Background(), dir, testClock())
	if !errors.Is(err, store.ErrDataDirInUse) {
		t.Fatalf("importing under a held lock returned %v, want ErrDataDirInUse", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, store.StateFile)); !errors.Is(serr, os.ErrNotExist) {
		t.Error("the refused import wrote a state.db anyway")
	}

	// And it succeeds once the holder is gone, which is what makes the refusal
	// a precondition rather than a permanent block.
	if rerr := held.Release(); rerr != nil {
		t.Fatalf("releasing: %v", rerr)
	}
	if _, ierr := fromrust.Import(context.Background(), dir, testClock()); ierr != nil {
		t.Fatalf("importing after the holder exited: %v", ierr)
	}
}

// Two importers at once. One wins the lock and the other refuses before it has
// created anything, so neither can remove or write the other's staging
// database.
func TestConcurrentImportersDoNotTouchEachOther(t *testing.T) {
	dir := rustDir(t)

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		task.Go(ctx, "concurrent import", func() {
			defer wg.Done()
			_, errs[i] = fromrust.Import(ctx, dir, testClock())
		})
	}
	wg.Wait()

	var won, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, store.ErrDataDirInUse):
			refused++
		default:
			t.Fatalf("an importer failed with something else: %v", err)
		}
	}
	if won != 1 || refused != 1 {
		t.Fatalf("%d importers succeeded and %d refused, want one of each", won, refused)
	}
	assertOneStateFile(t, dir)
}

// A staging database another run left behind is inert: its name alone cannot
// say whether it belongs to a dead process or a live one, so it is reported and
// left exactly where it is.
func TestAStaleStagingFileIsReportedAndKept(t *testing.T) {
	dir := rustDir(t)
	stale := filepath.Join(dir, store.StateFile+".importing-0123456789abcdef")
	if err := os.WriteFile(stale, []byte("half an import"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := fromrust.Import(context.Background(), dir, testClock())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(rep.Ignored) != 1 || rep.Ignored[0] != filepath.Base(stale) {
		t.Errorf("the report ignored %v, want the stale staging name", rep.Ignored)
	}

	got, rerr := os.ReadFile(stale)
	if rerr != nil {
		t.Fatalf("the import removed a staging file it did not create: %v", rerr)
	}
	if string(got) != "half an import" {
		t.Errorf("the stale staging file now holds %q", got)
	}
}

// A destination that exists is never written through, whatever produced it.
func TestAnExistingStateIsNotTouched(t *testing.T) {
	dir := rustDir(t)
	target := filepath.Join(dir, store.StateFile)
	if err := os.WriteFile(target, []byte("somebody else's database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := fromrust.Import(context.Background(), dir, testClock()); !errors.Is(err, fromrust.ErrStateExists) {
		t.Fatalf("Import returned %v, want ErrStateExists", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "somebody else's database" {
		t.Errorf("the destination now holds %q", got)
	}
}

// A SQLite file nothing here classified stops the migration. Whatever wrote it
// did so because something needed to be durable, and stepping over it would
// report success while leaving it behind.
func TestAnUnknownDatabaseBlocksTheImport(t *testing.T) {
	dir := rustDir(t)
	stray := filepath.Join(dir, "quotas.db")
	if err := os.WriteFile(stray, []byte("SQLite format 3"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fromrust.Import(context.Background(), dir, testClock())
	if !errors.Is(err, fromrust.ErrUnknownDatabase) {
		t.Fatalf("Import returned %v, want ErrUnknownDatabase", err)
	}
	if !strings.Contains(err.Error(), "quotas.db") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}

// The sidecars belong to the database beside them and are not artifacts of
// their own, so they neither block the import nor get reported.
func TestWalAndShmSidecarsAreNotDatabases(t *testing.T) {
	dir := rustDir(t)
	for _, name := range []string{"auth.db-wal", "auth.db-shm"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := fromrust.Import(context.Background(), dir, testClock())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(rep.Ignored) != 0 {
		t.Errorf("the report mentions %v", rep.Ignored)
	}
}

// A table the old build wrote that nothing here classified is a refusal, not a
// silent skip: the first evidence of the alternative is a user noticing
// something stopped working.
func TestAnUnknownTableBlocksTheImport(t *testing.T) {
	dir := rustDir(t)
	mkdb(t, filepath.Join(dir, "auth.db"),
		`CREATE TABLE webauthn_credential (id INTEGER PRIMARY KEY, user INTEGER)`)

	_, err := fromrust.Import(context.Background(), dir, testClock())
	if !errors.Is(err, fromrust.ErrUnknownTable) {
		t.Fatalf("Import returned %v, want ErrUnknownTable", err)
	}
	if !strings.Contains(err.Error(), "webauthn_credential") {
		t.Errorf("the refusal does not name the table: %v", err)
	}
}

// Durable state a later phase owns is refused while it holds rows, and the
// refusal names the phase that adds its destination.
func TestDeferredTablesRefuseOnlyWhenTheyHoldRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  string
		setup []string
		rows  bool
		phase string
	}{
		{
			"the persisted name-index switch", "index.db",
			[]string{
				`CREATE TABLE index_settings (id INTEGER PRIMARY KEY, name_index INTEGER)`,
				`INSERT INTO index_settings VALUES (1, 1)`,
			},
			true, "Phase 8",
		},
		{
			"the compat instance identity", "compat-nc.db",
			[]string{
				`CREATE TABLE nc_instance (id INTEGER PRIMARY KEY, instance TEXT)`,
				`INSERT INTO nc_instance VALUES (1, 'abc')`,
			},
			true, "Phase 10",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := rustDir(t)
			mkdb(t, filepath.Join(dir, tc.file), tc.setup...)

			_, err := fromrust.Import(context.Background(), dir, testClock())
			if !tc.rows {
				if err != nil {
					t.Fatalf("an empty deferred table blocked the import: %v", err)
				}
				return
			}
			if !errors.Is(err, fromrust.ErrDeferredTableHasRows) {
				t.Fatalf("Import returned %v, want ErrDeferredTableHasRows", err)
			}
			if !strings.Contains(err.Error(), tc.phase) {
				t.Errorf("the refusal does not name %s: %v", tc.phase, err)
			}
		})
	}
}

// A short-lived table is discarded on purpose, and the report says so rather
// than leaving the rows unaccounted for.
func TestDiscardedTablesAreReportedWithTheirReason(t *testing.T) {
	dir := rustDir(t)
	mkdb(t, filepath.Join(dir, "auth.db"),
		`CREATE TABLE login_challenge (id INTEGER PRIMARY KEY, user INTEGER)`,
		`INSERT INTO login_challenge VALUES (1, 1)`)

	rep, err := fromrust.Import(context.Background(), dir, testClock())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	var out bytes.Buffer
	if werr := rep.Write(&out); werr != nil {
		t.Fatalf("writing the report: %v", werr)
	}
	if !strings.Contains(out.String(), "login_challenge") ||
		!strings.Contains(out.String(), "retried rather than resumed") {
		t.Errorf("the report does not account for the discarded challenge:\n%s", out.String())
	}
}

func assertOneStateFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), store.StateFile) {
			found = append(found, e.Name())
		}
	}
	if len(found) != 1 || found[0] != store.StateFile {
		t.Errorf("the directory holds %v, want just %s", found, store.StateFile)
	}
}

// The importer carries the Phase 3 durable auth state: SMB ciphertexts keep
// their recorded key version, and only the TOTP replay steps still inside the
// live window come across. A source that carries this state is no longer a
// refusal.
func TestImportPreservesSMBSecretsAndLiveReplaySteps(t *testing.T) {
	dir := rustDir(t)
	// nowStep = nowNs / 30s; a step at nowStep is live, one at step 1000 is
	// long expired.
	liveStep := nowNs / (30 * 1e9)
	mkdb(t, filepath.Join(dir, "auth.db"),
		`CREATE TABLE user_smb_secret (user INTEGER, nt_hash_ct BLOB, key_ver INTEGER)`,
		`INSERT INTO user_smb_secret VALUES (1, X'c0ffee', 5)`,
		`CREATE TABLE totp_used (user INTEGER, time_step INTEGER)`,
		`INSERT INTO totp_used VALUES (1, 1000)`,
		`INSERT INTO totp_used VALUES (1, `+strconv.FormatInt(liveStep, 10)+`)`,
	)

	rep, err := fromrust.Import(context.Background(), dir, testClock())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	st := openState(t, dir)
	var keyVer int
	if err := st.SQL().QueryRow(`SELECT key_ver FROM user_smb_secret WHERE user = 1`).Scan(&keyVer); err != nil {
		t.Fatalf("the SMB ciphertext did not come across: %v", err)
	}
	if keyVer != 5 {
		t.Errorf("the SMB ciphertext's key version = %d, want 5 preserved", keyVer)
	}

	var live, stale int
	if err := st.SQL().QueryRow(`SELECT count(*) FROM totp_used WHERE step = ?`, liveStep).Scan(&live); err != nil {
		t.Fatalf("counting live steps: %v", err)
	}
	if err := st.SQL().QueryRow(`SELECT count(*) FROM totp_used WHERE step = 1000`).Scan(&stale); err != nil {
		t.Fatalf("counting stale steps: %v", err)
	}
	if live != 1 {
		t.Errorf("a live TOTP replay step was not preserved (live=%d)", live)
	}
	if stale != 0 {
		t.Errorf("an expired TOTP replay step was carried across (stale=%d)", stale)
	}
	if rep.Dropped[fromrust.Drop{Table: "totp_used", Reason: fromrust.ReasonStaleFactor}] != 1 {
		t.Errorf("the report does not count the dropped expired replay step: %+v", rep.Dropped)
	}
}

// An SMB ciphertext whose user is missing, or whose recorded key version is
// missing, aborts the import: a credential that vanishes in silence is worse
// than an operator-facing refusal.
func TestImportRefusesASilentSMBLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []string
	}{
		{"a missing user", []string{
			`CREATE TABLE user_smb_secret (user INTEGER, nt_hash_ct BLOB, key_ver INTEGER)`,
			`INSERT INTO user_smb_secret VALUES (99, X'00', 1)`,
		}},
		{"a missing key version", []string{
			`CREATE TABLE user_smb_secret (user INTEGER, nt_hash_ct BLOB, key_ver INTEGER)`,
			`INSERT INTO user_smb_secret VALUES (1, X'00', NULL)`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := rustDir(t)
			mkdb(t, filepath.Join(dir, "auth.db"), tc.rows...)
			if _, err := fromrust.Import(context.Background(), dir, testClock()); err == nil {
				t.Error("the import succeeded over a ciphertext that must be refused")
			}
		})
	}
}
