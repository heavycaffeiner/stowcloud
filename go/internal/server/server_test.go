package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// TestServerTimeouts is D13. It asserts both halves of the timeout contract:
// the set fields are non-zero, and the two stream timeouts are zero on
// purpose. A later edit that sets them has to change this test.
func TestServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler(), nil)
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want non-zero (a zero means no slowloris limit)", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want non-zero", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Errorf("MaxHeaderBytes = %v, want non-zero", srv.MaxHeaderBytes)
	}
	// The deliberate zeros: a whole-request deadline breaks large uploads and
	// downloads, which stream. The slowloris case is covered by
	// ReadHeaderTimeout and streaming handlers hold their own per-read idle
	// deadlines, which is what distinguishes a slow client from a stalled one.
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %v, want zero on purpose", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want zero on purpose", srv.WriteTimeout)
	}
}

// TestConfigValidation refuses the values that would make the server unsafe
// or unbootable, each naming the key.
func TestConfigValidation(t *testing.T) {
	if _, err := Validate(raw{}); err == nil {
		t.Fatal("an empty config must be refused")
	}
	good := raw{}
	good.Server.DataDir = "/tmp/x"
	good.HTTP.AppHosts = []string{"nas.local"}
	cfg, err := Validate(good)
	if err != nil {
		t.Fatalf("a minimal valid config: %v", err)
	}
	if cfg.Listen != ":8443" {
		t.Errorf("default listen = %q, want :8443", cfg.Listen)
	}
	bad := good
	bad.HTTP.TrustedProxyCIDRs = []string{"not a cidr"}
	if _, err := Validate(bad); err == nil {
		t.Fatal("a malformed trusted-proxy CIDR must be refused at startup")
	}
}

// TestSetupGateIsSingleUseAndCloses asserts the two properties that make the
// token worth nothing once spent: a completed bootstrap is permanently closed
// even for the same process, and a spent token cannot be spent twice.
func TestSetupGateIsSingleUseAndCloses(t *testing.T) {
	clk := clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000))
	dir := t.TempDir()
	st, err := store.Open(dir, store.Options{Clock: clk})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})
	svc := newTestAuth(t, dir, st, clk)
	gate, gerr := NewSetupGate(context.Background(), svc, clk, dir)
	if gerr != nil {
		t.Fatalf("NewSetupGate: %v", gerr)
	}
	if !gate.IsRequired(context.Background()) {
		t.Fatal("a fresh deployment must require setup")
	}
	if ierr := gate.Issue(nil); ierr != nil {
		t.Fatalf("issue: %v", ierr)
	}
	token, err := os.ReadFile(filepath.Join(dir, "setup-token"))
	if err != nil {
		t.Fatalf("reading the token file: %v", err)
	}
	token = []byte(strings.TrimSpace(string(token)))
	// The token file is mode 0600.
	info, err := os.Stat(filepath.Join(dir, "setup-token"))
	if err != nil {
		t.Fatalf("stat the token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}

	_, err = gate.Complete(context.Background(), string(token), "admin", pwSecret(t, "correct-horse"), "127.0.0.1")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if gate.IsRequired(context.Background()) {
		t.Fatal("setup must be permanently closed once an administrator exists")
	}
	// The token file is gone.
	if _, err := os.Stat(filepath.Join(dir, "setup-token")); !os.IsNotExist(err) {
		t.Fatal("the spent token file must be removed")
	}
	// A second spend is refused as completed, not accepted.
	if _, err := gate.Complete(context.Background(), string(token), "admin2", pwSecret(t, "correct-horse"), "127.0.0.1"); err == nil {
		t.Fatal("a spent token must not create a second administrator")
	}
}

func TestSetupTokenExpires(t *testing.T) {
	mut := &mutableClock{t: time.Unix(0, 1_700_000_000_000_000_000)}
	dir := t.TempDir()
	st, err := store.Open(dir, store.Options{Clock: mut})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})
	svc := newTestAuth(t, dir, st, mut)
	gate, gerr := NewSetupGate(context.Background(), svc, mut, dir)
	if gerr != nil {
		t.Fatalf("NewSetupGate: %v", gerr)
	}
	if ierr := gate.Issue(nil); ierr != nil {
		t.Fatalf("issue: %v", ierr)
	}
	tokenF, err := os.ReadFile(filepath.Join(dir, "setup-token"))
	if err != nil {
		t.Fatalf("reading the token file: %v", err)
	}
	token := []byte(strings.TrimSpace(string(tokenF)))
	// Fifteen minutes and one second later, the token is dead.
	mut.t = mut.t.Add(15*time.Minute + time.Second)
	_, err = gate.Complete(context.Background(), string(token), "admin", pwSecret(t, "correct-horse"), "127.0.0.1")
	if err == nil || err.Error() != "the setup token has expired" {
		t.Fatalf("an expired token = %v, want the expiry refusal", err)
	}
}

// mutableClock lets a test move the gate's clock forward without rebuilding it.
type mutableClock struct{ t time.Time }

func (m *mutableClock) Now() time.Time                  { return m.t }
func (m *mutableClock) Since(t time.Time) time.Duration { return m.t.Sub(t) }
func (m *mutableClock) Nanos() int64                    { return m.t.UnixNano() }

// newTestAuth builds an auth service over the store with its master key open,
// which is the state every request path sees after a successful startup.
func newTestAuth(t *testing.T, dir string, st *store.Store, clk clock.Clock) *auth.Service {
	t.Helper()
	svc := auth.New(auth.Config{Store: st.State(), StoreDir: dir, Clock: clk})
	if _, err := svc.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("opening the master key: %v", err)
	}
	return svc
}

func pwSecret(t *testing.T, s string) secret.Secret {
	t.Helper()
	return secret.New([]byte(s))
}
