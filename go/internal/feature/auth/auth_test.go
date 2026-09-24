package auth_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
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
	sink  *countingSink
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
	sink := &countingSink{}
	svc := auth.New(auth.Config{Store: store, StoreDir: dir, Clock: clk})
	svc.SetAccessChangeSink(sink)
	if _, err := svc.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("OpenMasterKey: %v", err)
	}
	return fixture{svc: svc, store: store, dir: dir, sink: sink}
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
