//go:build linux

package dav_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The content methods are driven against a real core over a real directory.
//
// A fake core would be a second implementation of the thing under test: what
// these methods do is translate a core result into a status, so the result has
// to be a real one. The filesystem is a temporary directory, which is what the
// share layer expects anyway.

const (
	testShare = core.ShareID(1)
	testUser  = core.UserID(1)
)

// fixture is one server's worth of state, plus the directory behind the share.
type fixture struct {
	h    *dav.Handler
	core *core.Core
	dir  string
	// locks is what the handler was given, so a test can arrange a refusal.
	locks *stubLocks
}

// stubLocks answers the guard. The lock table has its own tests; what matters
// here is that a refusal reaches the client as 423 and an admission does not
// stop the write.
type stubLocks struct {
	// refuse is returned for every path when set.
	refuse error
	// refuseAt refuses one path only. A method that writes at two ends has to
	// guard both, and a stub that answers the same for either cannot tell a
	// handler guarding one end from a handler guarding both.
	refuseAt map[string]error
	// sawTokens records what the last guarded write submitted, so a test can
	// check that an If header's tokens travelled.
	sawTokens []string
	// guarded records every path the handler asked about, in order.
	guarded []string
}

func (s *stubLocks) Guard(_ context.Context, _ uint32, path string, _ int64, submitted []string) error {
	s.sawTokens = submitted
	s.guarded = append(s.guarded, path)
	if err, ok := s.refuseAt[path]; ok {
		return err
	}
	return s.refuse
}

func newFixture(t *testing.T) *fixture { return build(t, nil, 0) }

// newFixtureHolding builds one where every resource carries the given lock
// token, which is what an If header naming it evaluates against.
func newFixtureHolding(t *testing.T, tokens ...string) *fixture {
	return build(t, tokens, 0)
}

// newFixtureBounded builds one whose Depth: infinity ceiling is low enough to
// reach without writing ten thousand files.
func newFixtureBounded(t *testing.T, entries int) *fixture {
	return build(t, nil, entries)
}

func build(t *testing.T, held []string, infinityEntries int) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()

	stf, err := dbfile.Open(ctx, state.Spec(filepath.Join(root, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stf.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	st := state.New(stf)

	cf, err := dbfile.Open(ctx, cache.Spec(filepath.Join(root, "cache.db")))
	if err != nil {
		t.Fatalf("opening the cache database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := cf.Close(); cerr != nil {
			t.Errorf("closing the cache database: %v", cerr)
		}
	})
	ca, cerr := cache.New(ctx, cf, st)
	if cerr != nil {
		t.Fatalf("preparing the cache: %v", cerr)
	}

	c, kerr := core.New(ctx, core.Options{State: st, Cache: ca, ACL: acl.NewEvaluator()})
	if kerr != nil {
		t.Fatalf("building the core: %v", kerr)
	}

	// The share's own directory, separate from where the databases live so a
	// listing does not report them.
	shareDir := filepath.Join(root, "share")
	if merr := os.MkdirAll(shareDir, 0o755); merr != nil {
		t.Fatalf("creating the share directory: %v", merr)
	}
	if rerr := c.RegisterShare(ctx, core.ShareDef{
		ID: testShare, Name: "files", Host: shareDir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the share: %v", rerr)
	}

	seedUser(t, st, int64(testUser))
	grantAll(t, c, st, int64(testUser), testShare)

	locks := &stubLocks{}
	return &fixture{
		h: dav.New(dav.Options{
			Core:  c,
			Locks: locks,
			TokensAt: func(context.Context, uint32, string) []string {
				return held
			},
			InfinityEntries: infinityEntries,
		}),
		core:  c,
		dir:   shareDir,
		locks: locks,
	}
}

func seedUser(t *testing.T, st *state.DB, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)`, id, "u")
		return err
	}); err != nil {
		t.Fatalf("seeding the user: %v", err)
	}
}

// grantAll gives the test user everything over the share, so a refusal in a
// test is the method's own decision rather than a missing grant.
func grantAll(t *testing.T, c *core.Core, st *state.DB, user int64, share core.ShareID) {
	t.Helper()
	ctx := context.Background()
	holder := user
	all := acl.Read | acl.Download | acl.Write | acl.Create | acl.Delete | acl.Move
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: "",
		Allow:   uint16(all),
		Inherit: true,
		Label:   "files",
	}, 0); err != nil {
		t.Fatalf("persisting the grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

// resolve turns a share-relative path into what a mount would hand a method.
func (f *fixture) resolve(t *testing.T, path string) core.Resolved {
	t.Helper()
	vp, err := vfs.ParseVpath("/files/" + strings.TrimPrefix(path, "/"))
	if err != nil {
		t.Fatalf("parsing %q: %v", path, err)
	}
	res, rerr := f.core.Resolve(testUser, vp, 0)
	if rerr != nil {
		t.Fatalf("resolving %q: %v", path, rerr)
	}
	return res
}

// write puts a file into the share directly, bypassing the methods so a read
// test does not depend on the write path passing.
func (f *fixture) write(t *testing.T, path, body string) {
	t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("creating the parent of %q: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
}

// mkdir creates a collection directly.
func (f *fixture) mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.dir, path), 0o755); err != nil {
		t.Fatalf("creating %q: %v", path, err)
	}
}

// exists reports whether something is at a share-relative path.
func (f *fixture) exists(path string) bool {
	_, err := os.Stat(filepath.Join(f.dir, path))
	return err == nil
}

// read returns a file's contents from the share.
func (f *fixture) read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir, path))
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return string(body)
}

// request builds a request carrying the given headers.
func request(method, path string, body string, headers map[string]string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, http.NoBody)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}
