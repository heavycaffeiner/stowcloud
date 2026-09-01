//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// newCore builds a Core over real databases in a temporary directory. Real
// ones rather than fakes: the registry's own behavior does not touch them,
// but construction loads the grant table, and a Roots test needs a grant
// that went through the write path a running server uses.
func newCore(t *testing.T) (*Core, *state.DB) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

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

	c, err := New(ctx, Options{State: st, Cache: ca, ACL: acl.NewEvaluator()})
	if err != nil {
		t.Fatalf("building the core: %v", err)
	}
	return c, st
}

// def is a share definition over a fresh directory.
func def(t *testing.T, id ShareID, name string) ShareDef {
	t.Helper()
	return ShareDef{ID: id, Name: name, Host: t.TempDir(), Policy: vfs.DefaultSharePolicy()}
}

func seedUser(t *testing.T, st *state.DB, id int64, name string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)`, id, name)
		return err
	}); err != nil {
		t.Fatalf("seeding user %d: %v", id, err)
	}
}

// grantRead persists one readable grant over a whole share and reloads the
// evaluator, which is the two-step discipline every grant write follows.
func grantRead(t *testing.T, c *Core, st *state.DB, user int64, share ShareID, label string) {
	t.Helper()
	ctx := context.Background()
	holder := user
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: "",
		Allow:   uint16(acl.Read),
		Inherit: true,
		Label:   label,
	}, 0); err != nil {
		t.Fatalf("persisting a grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

// openDescriptors counts this process's open files. The registry's promise
// that a replaced root is closed is a promise about this number.
func openDescriptors(t *testing.T) int {
	t.Helper()
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("reading /proc/self/fd: %v", err)
	}
	return len(names)
}

func TestARegisteredShareIsVisibleThroughEveryAccessor(t *testing.T) {
	c, _ := newCore(t)
	d := def(t, 7, "documents")

	if err := c.RegisterShare(context.Background(), d); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}

	got, ok := c.Share(7)
	if !ok {
		t.Fatal("Share reported the share as unregistered")
	}
	if got.Name != "documents" || got.BrokenReason != "" {
		t.Fatalf("Share returned %+v, want documents with no broken reason", got)
	}
	if _, ok := c.ShareRoot(7); !ok {
		t.Fatal("ShareRoot reported no live root for a registered share")
	}
	if err := c.ShareBroken(7); err != nil {
		t.Fatalf("ShareBroken on a live share: %v", err)
	}
}

func TestSharesListsEveryShareByAscendingID(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	for _, id := range []ShareID{40, 9, 22} {
		if err := c.RegisterShare(ctx, def(t, id, "share")); err != nil {
			t.Fatalf("RegisterShare(%d): %v", id, err)
		}
	}
	c.RegisterBroken(def(t, 3, "gone"), vfs.ErrNotFound)

	var ids []ShareID
	for _, d := range c.Shares() {
		ids = append(ids, d.ID)
	}
	want := []ShareID{3, 9, 22, 40}
	if len(ids) != len(want) {
		t.Fatalf("Shares returned %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("Shares returned %v, want %v", ids, want)
		}
	}
}

func TestABrokenShareStaysListedAndHandsOutNoRoot(t *testing.T) {
	c, _ := newCore(t)
	cause := errors.New("the disk did not come back")
	c.RegisterBroken(ShareDef{ID: 4, Name: "archive"}, cause)

	got, ok := c.Share(4)
	if !ok {
		t.Fatal("a broken share is absent from Share")
	}
	if got.BrokenReason != "unavailable" {
		t.Fatalf("BrokenReason is %q, want unavailable", got.BrokenReason)
	}
	if _, ok := c.ShareRoot(4); ok {
		t.Fatal("ShareRoot handed out a root for a broken share")
	}
	if err := c.ShareBroken(4); !errors.Is(err, cause) {
		t.Fatalf("ShareBroken returned %v, want the registered cause", err)
	}
	if len(c.Shares()) != 1 {
		t.Fatalf("Shares returned %d entries, want the broken one", len(c.Shares()))
	}
}

func TestAnUnregisteredShareIsAbsentRatherThanBroken(t *testing.T) {
	c, _ := newCore(t)
	if err := c.ShareBroken(99); err != nil {
		t.Fatalf("ShareBroken on an unknown id: %v", err)
	}
	if _, ok := c.Share(99); ok {
		t.Fatal("Share claimed an unknown id is registered")
	}
	if _, ok := c.ShareRoot(99); ok {
		t.Fatal("ShareRoot handed out a root for an unknown id")
	}
}

func TestRejectionKindNamesWhyTheShareWouldNotOpen(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing", vfs.ErrNotFound, "missing"},
		{"unreadable", vfs.ErrDenied, "unreadable"},
		{"sandbox denied", vfs.ErrSandboxDenied, "ungranted"},
		{"anything else", errors.New("i/o error"), "unavailable"},
		{
			"an admission refusal",
			&vfs.AdmissionError{Path: "/mnt/x", Type: vfs.FsNfs, Reason: "no"},
			"nfs",
		},
		{
			"an admission refusal, wrapped",
			errf(&vfs.AdmissionError{Path: "/mnt/x", Type: vfs.FsOverlay}, "registering"),
			"overlay",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RejectionKind(tc.err); got != tc.want {
				t.Fatalf("RejectionKind(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestReRegisteringAShareClosesTheRootItReplaces(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	d := def(t, 1, "documents")

	if err := c.RegisterShare(ctx, d); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	before := openDescriptors(t)
	for range 20 {
		if err := c.RegisterShare(ctx, d); err != nil {
			t.Fatalf("re-registering: %v", err)
		}
	}
	if after := openDescriptors(t); after > before {
		t.Fatalf("open descriptors went from %d to %d across 20 re-registrations", before, after)
	}
}

func TestReplacingAnEntryWithItsOwnRootLeavesTheRootOpen(t *testing.T) {
	c, _ := newCore(t)
	d := def(t, 1, "documents")
	if err := c.RegisterShare(context.Background(), d); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	root, ok := c.ShareRoot(1)
	if !ok {
		t.Fatal("no live root after registration")
	}

	d.TrashEnabled = true
	c.replaceEntry(&shareEntry{def: d, root: root})

	if err := root.Alive(); err != nil {
		t.Fatalf("the root was closed by a replacement that reused it: %v", err)
	}
	got, _ := c.Share(1)
	if !got.TrashEnabled {
		t.Fatal("the replacement did not install the new definition")
	}
}

func TestProbeMovesAShareWhoseDirectoryWentAwayToBroken(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	host := filepath.Join(t.TempDir(), "share")
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatalf("creating the share directory: %v", err)
	}
	d := ShareDef{ID: 2, Name: "removable", Host: host, Policy: vfs.DefaultSharePolicy()}
	if err := c.RegisterShare(ctx, d); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	if err := os.Remove(host); err != nil {
		t.Fatalf("removing the share directory: %v", err)
	}

	broke, healed := c.ProbeShares(ctx)
	if len(broke) != 1 || broke[0].ID != 2 {
		t.Fatalf("ProbeShares reported broke=%v, want the removed share", broke)
	}
	if broke[0].BrokenReason != "missing" {
		t.Fatalf("the reported reason is %q, want missing", broke[0].BrokenReason)
	}
	if len(healed) != 0 {
		t.Fatalf("ProbeShares reported healed=%v, want nothing", healed)
	}
	if c.ShareBroken(2) == nil {
		t.Fatal("the share is still live after the probe")
	}
	if _, ok := c.ShareRoot(2); ok {
		t.Fatal("a broken share still hands out a root")
	}
}

func TestProbeHealsAShareWhosePathCameBackAndReportsOnlyTransitions(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	host := filepath.Join(t.TempDir(), "share")
	d := ShareDef{ID: 5, Name: "removable", Host: host, Policy: vfs.DefaultSharePolicy()}
	c.RegisterBroken(d, vfs.ErrNotFound)

	if broke, healed := c.ProbeShares(ctx); len(broke) != 0 || len(healed) != 0 {
		t.Fatalf("a probe over a still-missing path reported broke=%v healed=%v", broke, healed)
	}
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatalf("creating the share directory: %v", err)
	}

	broke, healed := c.ProbeShares(ctx)
	if len(broke) != 0 {
		t.Fatalf("ProbeShares reported broke=%v, want nothing", broke)
	}
	if len(healed) != 1 || healed[0].ID != 5 {
		t.Fatalf("ProbeShares reported healed=%v, want the recovered share", healed)
	}
	if healed[0].BrokenReason != "" {
		t.Fatalf("the healed definition still carries reason %q", healed[0].BrokenReason)
	}
	if _, ok := c.ShareRoot(5); !ok {
		t.Fatal("the healed share hands out no root")
	}

	broke, healed = c.ProbeShares(ctx)
	if len(broke) != 0 || len(healed) != 0 {
		t.Fatalf("a steady-state probe reported broke=%v healed=%v", broke, healed)
	}
}

func TestProbeRunsTheAdmissionGateAgainOnRetry(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	// procfs is a real, readable directory this server refuses to serve, so
	// it stands in for the disk that came back on a filesystem the
	// admission gate rejects, without needing a mount.
	d := ShareDef{ID: 6, Name: "refused", Host: "/proc", Policy: vfs.DefaultSharePolicy()}
	c.RegisterBroken(d, vfs.ErrNotFound)

	broke, healed := c.ProbeShares(ctx)
	if len(broke) != 0 || len(healed) != 0 {
		t.Fatalf("ProbeShares reported broke=%v healed=%v over a refused filesystem", broke, healed)
	}
	if c.ShareBroken(6) == nil {
		t.Fatal("a share on a refused filesystem healed on probe")
	}
}

func TestUnregisterRemovesTheEntryAndClosesTheRoot(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	if err := c.RegisterShare(ctx, def(t, 1, "documents")); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	root, ok := c.ShareRoot(1)
	if !ok {
		t.Fatal("no live root after registration")
	}

	c.UnregisterShare(1)

	if _, ok := c.Share(1); ok {
		t.Fatal("the share is still registered after UnregisterShare")
	}
	if err := root.Close(); err == nil {
		t.Fatal("the root was still open after UnregisterShare")
	}
}

func TestUnregisterHandlesABrokenShareAndAnUnknownID(t *testing.T) {
	c, _ := newCore(t)
	c.RegisterBroken(ShareDef{ID: 8, Name: "gone"}, vfs.ErrNotFound)

	c.UnregisterShare(8)
	if _, ok := c.Share(8); ok {
		t.Fatal("the broken share survived UnregisterShare")
	}
	c.UnregisterShare(404)
}

func TestRootsCarryTheRegistrysFactsAndKeepABrokenShareListed(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	seedUser(t, st, 1, "ada")

	live := def(t, 10, "documents")
	live.TrashEnabled = true
	live.SharedExternally = true
	if err := c.RegisterShare(ctx, live); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	c.RegisterBroken(ShareDef{ID: 11, Name: "archive"}, vfs.ErrNotFound)

	grantRead(t, c, st, 1, 10, "Documents")
	grantRead(t, c, st, 1, 11, "Archive")

	roots := c.Roots(1)
	if len(roots) != 2 {
		t.Fatalf("Roots returned %d entries, want both shares", len(roots))
	}
	byShare := map[int64]acl.RootEntry{}
	for _, r := range roots {
		byShare[r.Share] = r
	}

	got := byShare[10]
	if !got.TrashEnabled || !got.SharedExternally || got.BrokenReason != "" {
		t.Fatalf("the live root is %+v, want trash and external set and no reason", got)
	}
	if got.Label != "Documents" {
		t.Fatalf("the live root is labeled %q, want Documents", got.Label)
	}

	got = byShare[11]
	if got.BrokenReason != "missing" {
		t.Fatalf("the broken root carries reason %q, want missing", got.BrokenReason)
	}
}

func TestRootsLeaveAnUnregisteredGrantAlone(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	grantRead(t, c, st, 1, 12, "Ghost")

	roots := c.Roots(1)
	if len(roots) != 1 {
		t.Fatalf("Roots returned %d entries, want the one grant", len(roots))
	}
	if roots[0].BrokenReason != "" || roots[0].TrashEnabled {
		t.Fatalf("a grant naming no registered share was decorated: %+v", roots[0])
	}
}

func TestTheRegistryTakesConcurrentReadsAndWrites(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	hosts := make([]string, 4)
	for i := range hosts {
		hosts[i] = t.TempDir()
	}

	var wg sync.WaitGroup
	for i := range hosts {
		id := ShareID(i + 1)
		host := hosts[i]
		wg.Add(2)
		task.Go(ctx, "registry: register and unregister loop", func() {
			defer wg.Done()
			for range 50 {
				d := ShareDef{ID: id, Name: "s", Host: host, Policy: vfs.DefaultSharePolicy()}
				if err := c.RegisterShare(ctx, d); err != nil {
					t.Errorf("RegisterShare: %v", err)
					return
				}
				c.UnregisterShare(id)
			}
		})
		task.Go(ctx, "registry: accessor loop", func() {
			defer wg.Done()
			for range 50 {
				c.Shares()
				c.ShareRoot(id)
				// Nothing in this test registers a broken share, so the
				// only answer a racing read may see is nil: registered or
				// absent, both report the share as not broken.
				if err := c.ShareBroken(id); err != nil {
					t.Errorf("ShareBroken during a concurrent read: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}
