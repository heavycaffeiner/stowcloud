package auth_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The tests use a fixed weak-but-legal password, because the floor is the
// thing under test in exactly one place and everywhere else it is noise.
const testPassword = "correct horse battery"

func pw(s string) secret.Secret { return secret.New([]byte(s)) }

// fixture is a service over a fresh database, with its key opened.
type fixture struct {
	svc   *auth.Service
	store *state.DB
	dir   string
	// published counts what the credential renderer was asked for, so a test
	// can assert that a change reached the sidecar rather than stopping at
	// the database.
	published *int
	sink      *countingSink
	// passwdGID records the group the account renderer was handed, which is
	// the one value of that file this package chooses nothing about.
	passwdGID *uint32
}

type countingSink struct{ n int }

func (c *countingSink) AccessChanged(context.Context) { c.n++ }

func newFixture(t *testing.T) fixture {
	t.Helper()
	return newFixtureWithClock(t, nil)
}

func newFixtureWithClock(t *testing.T, clk clock.Clock) fixture {
	t.Helper()
	dir := t.TempDir()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	store := state.New(f)

	published := 0
	var passwdGID uint32
	sink := &countingSink{}
	svc := auth.New(auth.Config{
		Store:      store,
		StoreDir:   dir,
		Clock:      clk,
		PassdbPath: filepath.Join(dir, "passdb"),
		RenderPassdb: func(creds []auth.SMBCredential) ([]byte, error) {
			published++
			var out []byte
			for _, c := range creds {
				out = append(out, c.Name...)
				out = append(out, '\n')
			}
			return out, nil
		},
		// The real renderer lives in the SMB package, which this tier may not
		// import. What is under test here is which accounts and which uid
		// reach it, so the shape it writes is the simplest one a test can
		// read back.
		RenderPasswd: func(creds []auth.SMBCredential, gid uint32) ([]byte, error) {
			passwdGID = gid
			var out []byte
			for _, c := range creds {
				out = fmt.Appendf(out, "%s:%d\n", c.Name, c.UID)
			}
			return out, nil
		},
	})
	svc.SetAccessChangeSink(sink)

	// The environment must not name a key: the tests run in one process and a
	// leaked variable from another test would be a refusal nobody expected.
	t.Setenv("SC_MASTER_KEY_FILE", filepath.Join(dir, "master.key"))
	if _, err := svc.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("OpenMasterKey: %v", err)
	}
	return fixture{svc: svc, store: store, dir: dir, published: &published, sink: sink, passwdGID: &passwdGID}
}

// account creates one and returns its id.
func (f fixture) account(t *testing.T, name string) int64 {
	t.Helper()
	id, err := f.svc.CreateUser(context.Background(), name, "", pw(testPassword))
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return id
}

// admin creates an administrator and returns its id.
func (f fixture) admin(t *testing.T, name string) int64 {
	t.Helper()
	id, err := f.svc.CreateAdmin(context.Background(), name, "", pw(testPassword))
	if err != nil {
		t.Fatalf("CreateAdmin(%q): %v", name, err)
	}
	return id
}

// breakAuditLog makes appending to the log fail and leaves every other write
// working, which is the state the never-fail-the-action contract is about.
func breakAuditLog(t *testing.T, f fixture) {
	t.Helper()
	if err := f.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`CREATE TRIGGER audit_is_full BEFORE INSERT ON audit
			 BEGIN SELECT RAISE(ABORT, 'the audit log cannot be written'); END`)
		return err
	}); err != nil {
		t.Fatalf("breaking the audit log: %v", err)
	}
}

// newServiceWithMembership is a second service over the same database, wired
// with a membership callback. The callback is a construction-time seam, so a
// test that watches it builds its own service rather than mutating one.
func newServiceWithMembership(t *testing.T, f fixture, onMembership func()) *auth.Service {
	t.Helper()
	svc := auth.New(auth.Config{
		Store:        f.store,
		StoreDir:     f.dir,
		OnMembership: onMembership,
	})
	if _, err := svc.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("OpenMasterKey: %v", err)
	}
	return svc
}
