//go:build linux

package upload

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

const (
	testUser  = core.UserID(42)
	testShare = core.ShareID(1)
)

// fixture is an engine over real databases and a real share, because what
// this package does is arrange filesystem and database writes in an order,
// and a fake for either proves nothing about the order.
type fixture struct {
	engine *Engine
	core   *core.Core
	state  *state.DB
	host   string
	clk    *steppingClock
}

func newFixture(t *testing.T) *fixture { return newFixtureWithCache(t, "") }

func newFixtureWithCache(t *testing.T, cacheDir string) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	clk := &steppingClock{at: time.Unix(0, 1_700_000_000_000_000_000)}

	stf, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stf.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	st := state.New(stf)

	cf, err := dbfile.Open(ctx, cache.Spec(filepath.Join(dir, "cache.db")))
	if err != nil {
		t.Fatalf("opening the cache database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := cf.Close(); cerr != nil {
			t.Errorf("closing the cache database: %v", cerr)
		}
	})
	ca, err := cache.New(ctx, cf, st)
	if err != nil {
		t.Fatalf("wrapping the cache: %v", err)
	}

	c, err := core.New(ctx, core.Options{State: st, Cache: ca, ACL: acl.NewEvaluator(), Clock: clk})
	if err != nil {
		t.Fatalf("building the core: %v", err)
	}

	host := filepath.Join(t.TempDir(), "share")
	if merr := os.MkdirAll(host, 0o755); merr != nil {
		t.Fatalf("creating the share: %v", merr)
	}
	if rerr := c.RegisterShare(ctx, core.ShareDef{
		ID: testShare, Name: "docs", Host: host, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", rerr)
	}

	if werr := st.Write(ctx, func(tx *sql.Tx) error {
		_, uerr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, 'tester', '', 0)`,
			int64(testUser))
		return uerr
	}); werr != nil {
		t.Fatalf("seeding the account: %v", werr)
	}
	holder := int64(testUser)
	if _, gerr := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(testShare),
		Subpath: "",
		Allow:   uint16(acl.Read | acl.Write | acl.Create | acl.Delete),
		Inherit: true,
		Label:   "Docs",
	}, 0); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := c.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}

	e, err := New(ctx, c, st, Options{Clock: clk, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("building the upload engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the upload engine: %v", cerr)
		}
	})
	return &fixture{engine: e, core: c, state: st, host: host, clk: clk}
}

// resolve is the destination an upload publishes to.
func (f *fixture) resolve(t *testing.T, name string) core.Resolved {
	t.Helper()
	p, err := vfs.ParseVpath("Docs/" + name)
	if err != nil {
		t.Fatalf("parsing the destination: %v", err)
	}
	r, err := f.core.Resolve(testUser, p, acl.Write|acl.Create)
	if err != nil {
		t.Fatalf("resolving %q: %v", name, err)
	}
	return r
}

// root is the live share root, which the write paths take directly.
func (f *fixture) root(t *testing.T) vfs.Root {
	t.Helper()
	root, ok := f.core.ShareRoot(testShare)
	if !ok {
		t.Fatal("the share has no live root")
	}
	return root
}

// create opens a session of the given length against a destination.
func (f *fixture) create(t *testing.T, name string, total uint64, spec SessionSpec) Session {
	t.Helper()
	spec.TotalLen = &total
	s, err := f.engine.Create(context.Background(), f.resolve(t, name), spec)
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	return s
}

// patch writes one chunk through the offset-addressed path.
func (f *fixture) patch(t *testing.T, id SessionID, off uint64, body []byte) uint64 {
	t.Helper()
	n, err := f.engine.PatchAt(context.Background(), f.root(t), id, testUser,
		off, bytes.NewReader(body), nil)
	if err != nil {
		t.Fatalf("PatchAt(%d): %v", off, err)
	}
	return n
}

// steppingClock is a clock a test moves by hand, so an expiry is reached
// without the test waiting for it.
type steppingClock struct {
	at time.Time
}

func (c *steppingClock) advance(d time.Duration)         { c.at = c.at.Add(d) }
func (c *steppingClock) Now() time.Time                  { return c.at }
func (c *steppingClock) Since(t time.Time) time.Duration { return c.at.Sub(t) }
func (c *steppingClock) Nanos() int64                    { return c.at.UnixNano() }

var _ clock.Clock = (*steppingClock)(nil)

func b64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func bytesToHex(b []byte) string { return hex.EncodeToString(b) }

func itoa(n int) string { return strconv.Itoa(n) }

// A create into a directory that is not there says so, rather than reporting a
// session that was never made.
//
// The two facts are different and the caller can act on only one. A folder
// deleted between the client listing it and starting the upload answered "no
// such upload session", which names nothing the client can retry or resume, and
// the secrecy argument behind that error does not apply: the caller has already
// resolved a capability for this path.
func TestACreateIntoAMissingDirectorySaysSo(t *testing.T) {
	f := newFixture(t)

	total := uint64(4)
	_, err := f.engine.Create(context.Background(),
		f.resolve(t, "no-such-folder/file.txt"), SessionSpec{TotalLen: &total})

	if !errors.Is(err, ErrDestMissing) {
		t.Fatalf("Create into a missing directory returned %v, want ErrDestMissing", err)
	}
	// And not the session error, which is the confusion being removed.
	if errors.Is(err, ErrNotFound) {
		t.Error("a missing destination still reports as an unknown session")
	}
}

// An unknown session still reports as one, so the split above did not widen
// into the case that deliberately keeps its answer vague.
func TestAnUnknownSessionStillReportsAsNotFound(t *testing.T) {
	f := newFixture(t)

	var missing SessionID
	_, err := f.engine.Get(context.Background(), missing, testUser)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown session returned %v, want ErrNotFound", err)
	}
	if errors.Is(err, ErrDestMissing) {
		t.Error("an unknown session reports as a missing destination")
	}
}
