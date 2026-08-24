// Linux only, because what it tests is.
//go:build linux

package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// TestExistenceRuleOverHTTP is the "Done when" proof: a path that does not
// exist and a path outside every grant produce byte-identical 404 responses
// through the real chain. The two cases share one assertion because the
// property is that they are indistinguishable, and a test that asserts each
// one separately can pass for the wrong reason.
func TestExistenceRuleOverHTTP(t *testing.T) {
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

	// The auth service with a real account and a real session.
	svc := newTestAuth(t, dir, st, clk)
	if _, uerr := svc.CreateUser(context.Background(), "alice", "Alice", pwSecret(t, "correct-horse")); uerr != nil {
		t.Fatalf("CreateUser: %v", uerr)
	}
	// The session the request carries.
	sess, err := svc.CreateSession(context.Background(), 1, "127.0.0.1", "test", 1, 0)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The core domain with one share and READ grant for user 1 on its root.
	ev := acl.NewEvaluator()
	coreSvc, err := core.New(st, core.Options{ACL: ev, Clock: clk})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	host := t.TempDir()
	if mderr := os.MkdirAll(host+"/docs", 0o775); mderr != nil {
		t.Fatalf("mkdir: %v", mderr)
	}
	if werr := os.WriteFile(host+"/docs/a.txt", []byte("hello"), 0o664); werr != nil {
		t.Fatalf("write a.txt: %v", werr)
	}
	// The share root is the directory the data lives in; the grant projects
	// it under the share's own name, which is the label a client path
	// matches. Without the label the whole resolution is dead, and a test
	// that fails open on it proves nothing.
	if rerr := coreSvc.RegisterShare(context.Background(), core.ShareDef{
		ID: 1, Name: "docs", Host: host + "/docs", Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("RegisterShare: %v", rerr)
	}
	g := acl.Grant{User: 1, Share: 1, Subpath: acl.NewPath(), Allow: acl.Read | acl.Create | acl.Write | acl.Delete, Inherit: true, Label: "docs"}
	if gerr := insertGrant(st, g, 1); gerr != nil {
		t.Fatalf("insertGrant: %v", gerr)
	}
	if lerr := ev.LoadFromState(context.Background(), st.State().SQL()); lerr != nil {
		t.Fatalf("reloading grants: %v", lerr)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := &httpapi.State{
		Log:     log,
		Clock:   clk,
		Auth:    svc,
		Core:    coreSvc,
		Trusted: mw.NewTrustedSet([]netip.Prefix{mustPrefix("127.0.0.0/8")}),
		Hosts:   mw.NewHostSet([]string{"localhost"}),
		CSRFKey: make([]byte, 32),
		Limiter: mw.NewRateLimiter(1000, 10000, clk),
	}
	deps := handler.Deps{
		Core: coreSvc, Auth: svc, Clock: clk, Log: log,
		Limiter: state.Limiter, Trusted: state.Trusted, Hosts: state.Hosts,
		CSRFKey: make([]byte, 32), WatchCap: func() int { return 4096 },
	}
	gate, err := NewSetupGate(context.Background(), svc, clk, dir)
	if err != nil {
		t.Fatalf("NewSetupGate: %v", err)
	}
	table := routes(deps, gate)
	if err := route.Validate(table); err != nil {
		t.Fatalf("route.Validate: %v", err)
	}
	state.SetLookup(route.From(table))
	full := httpapi.Chain(state)(mux(table, nil, nil, nil))

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/fs/list?path="+path, nil)
		req.Host = "localhost"
		req.AddCookie(&http.Cookie{Name: mw.SessionCookie, Value: sessionTokenHex(sess), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		rec := httptest.NewRecorder()
		full.ServeHTTP(rec, req)
		return rec
	}

	missing := do("docs/does-not-exist")
	outside := do("other/also-missing")

	if missing.Code != http.StatusNotFound || outside.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want both 404", missing.Code, outside.Code)
	}
	if !bytes.Equal(missing.Body.Bytes(), outside.Body.Bytes()) {
		t.Fatalf("a missing path and a path outside a grant are distinguishable:\nmissing: %s\noutside: %s",
			missing.Body.String(), outside.Body.String())
	}

	// The grant must actually resolve, or both cases above fail for the same
	// wrong reason: a listing of the granted share answers 200 with the file.
	ok := do("docs")
	if ok.Code != http.StatusOK {
		t.Fatalf("listing the granted share = %d, want 200 (the grant must resolve)", ok.Code)
	}
	if !bytes.Contains(ok.Body.Bytes(), []byte("a.txt")) {
		t.Fatalf("the listing does not contain the file: %s", ok.Body.String())
	}
}

func sessionTokenHex(sess auth.Session) string {
	return hex.EncodeToString(sess.Token.Reveal())
}

func mustPrefix(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

// insertGrant writes one ACL grant row, the same shape the core's own tests
// use but reachable from this package.
func insertGrant(st *store.Store, g acl.Grant, createdNs int64) error {
	return st.State().Write(context.Background(), func(tx *sql.Tx) error {
		var group any
		if g.Group != 0 {
			group = g.Group
		}
		_, err := tx.ExecContext(context.Background(), grantInsert,
			g.User, group, g.Share, g.Subpath.String(),
			int64(g.Allow), int64(g.Deny), boolInt(g.Inherit), g.Label, createdNs)
		return err
	})
}

const grantInsert = `
INSERT INTO "grant"(user, "group", share, subpath, allow, deny, inherit, label, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
