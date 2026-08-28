//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// homed is a core with homes enabled over a fresh host directory.
func homed(t *testing.T) (c *Core, st *state.DB, host string) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")
	host = filepath.Join(t.TempDir(), "homes")
	if err := c.EnableHomes(context.Background(), host); err != nil {
		t.Fatalf("EnableHomes: %v", err)
	}
	return c, st, host
}

// homeGrants is every grant on the homes share, which is the marker the
// once-per-user gate reads.
func homeGrants(t *testing.T, st *state.DB) []state.GrantRow {
	t.Helper()
	rows, err := st.ListGrants(context.Background(), state.GrantFilter{Share: homeShareID})
	if err != nil {
		t.Fatalf("listing home grants: %v", err)
	}
	return rows
}

func TestEnableHomesRegistersTheReservedShareAndRefusesTwice(t *testing.T) {
	c, _, host := homed(t)
	ctx := context.Background()

	def, ok := c.Share(homeShareID)
	if !ok {
		t.Fatal("the homes share is not registered")
	}
	if def.Name != homeLabel {
		t.Fatalf("the homes share is labelled %q, want %q", def.Name, homeLabel)
	}

	info, err := os.Stat(host)
	if err != nil {
		t.Fatalf("stat of the homes host: %v", err)
	}
	// The mode keeps other local users out of the tree that holds every home.
	if perm := info.Mode().Perm(); perm != homeHostMode {
		t.Fatalf("the homes host has mode %o, want %o", perm, homeHostMode)
	}

	if err := c.EnableHomes(ctx, host); err == nil {
		t.Fatal("enabling homes twice succeeded")
	}
}

func TestTheFirstEnsureHomeCreatesOneDirectoryAndOneScopedGrant(t *testing.T) {
	c, st, host := homed(t)
	ctx := context.Background()

	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("ensureHome: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host, "1")); err != nil {
		t.Fatalf("the home directory was not created: %v", err)
	}

	grants := homeGrants(t, st)
	if len(grants) != 1 {
		t.Fatalf("the first ensureHome wrote %d grants, want one", len(grants))
	}
	g := grants[0]
	if g.User == nil || *g.User != 1 {
		t.Fatalf("the grant names user %+v, want 1", g.User)
	}
	// The subpath scoping is the security property: the whole tree is one
	// share, so scoping is the only wall between users' homes.
	if g.Subpath != "1" {
		t.Fatalf("the grant is scoped to %q, want the user's own directory", g.Subpath)
	}
	if acl.Perms(g.Allow) != homePerms || !g.Inherit || g.Label != homeLabel {
		t.Fatalf("the grant is %+v, want the full inherited home set", g)
	}

	// The reload happened, so the evaluator serves the grant now rather than
	// after a restart.
	if !c.userHasHome(1) {
		t.Fatal("the evaluator does not see the home grant after ensureHome")
	}
}

func TestASecondEnsureHomeWritesNothing(t *testing.T) {
	c, st, _ := homed(t)
	ctx := context.Background()

	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("first ensureHome: %v", err)
	}
	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("second ensureHome: %v", err)
	}
	if got := len(homeGrants(t, st)); got != 1 {
		t.Fatalf("two calls left %d grants, want one", got)
	}
}

func TestConcurrentEnsureHomeProducesOneHome(t *testing.T) {
	c, st, host := homed(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		task.Go(ctx, "home: concurrent ensure", func() {
			defer wg.Done()
			if err := c.ensureHome(ctx, 1); err != nil {
				t.Errorf("concurrent ensureHome: %v", err)
			}
		})
	}
	wg.Wait()

	if got := len(homeGrants(t, st)); got != 1 {
		t.Fatalf("concurrent calls left %d grants, want one", got)
	}
	entries, err := os.ReadDir(host)
	if err != nil {
		t.Fatalf("reading the homes host: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "1" {
		t.Fatalf("the homes host holds %d entries, want one home", len(entries))
	}
}

func TestANewHomeIsSeededFromTheTemplate(t *testing.T) {
	c, _, host := homed(t)
	ctx := context.Background()

	// An operator drops files into .template and every later first login
	// receives them. An empty-mkdir-only implementation loses this silently.
	if err := os.MkdirAll(filepath.Join(host, templateName, "docs"), 0o755); err != nil {
		t.Fatalf("building the template: %v", err)
	}
	writeFile(t, host, filepath.Join(templateName, "welcome.txt"), "hello")
	writeFile(t, host, filepath.Join(templateName, "docs", "guide.txt"), "read me")

	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("ensureHome: %v", err)
	}
	if got := readHost(t, host, "1/welcome.txt"); got != "hello" {
		t.Fatalf("the seeded file holds %q", got)
	}
	if got := readHost(t, host, "1/docs/guide.txt"); got != "read me" {
		t.Fatalf("the seeded nested file holds %q", got)
	}
}

func TestOneUserCannotReachAnotherUsersHome(t *testing.T) {
	c, st, host := homed(t)
	ctx := context.Background()
	seedUser(t, st, 2, "bob")

	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("ada's home: %v", err)
	}
	if err := c.ensureHome(ctx, 2); err != nil {
		t.Fatalf("bob's home: %v", err)
	}
	writeFile(t, host, "1/private.txt", "ada's")

	// Each home projects under the same label, so bob's "Home" is his own
	// subpath and nothing under ada's is addressable from it.
	r, err := c.Resolve(2, vpath(t, "Home"), acl.Read)
	if err != nil {
		t.Fatalf("resolving bob's home: %v", err)
	}
	entries, err := r.root.ReadDir(r.Path(), 0)
	if err != nil {
		t.Fatalf("listing bob's home: %v", err)
	}
	for _, e := range entries {
		if e.Name == "private.txt" {
			t.Fatal("bob sees a file from ada's home")
		}
	}
	if r.Path().String() != "2" {
		t.Fatalf("bob's home resolved to %q, want his own subpath", r.Path().String())
	}
}

func TestTheTemplateIsNotReachableAsAHome(t *testing.T) {
	c, _, host := homed(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(host, templateName), 0o755); err != nil {
		t.Fatalf("building the template: %v", err)
	}
	writeFile(t, host, filepath.Join(templateName, "seed.txt"), "x")
	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("ensureHome: %v", err)
	}

	// Homes are named by numeric user id, and .template is not a number, so
	// no grant scopes anybody to it.
	r, err := c.Resolve(1, vpath(t, "Home"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the home: %v", err)
	}
	if r.Path().String() == templateName {
		t.Fatal("a home resolved onto the template directory")
	}

	// Naming the template through the label lands under the caller's own
	// subpath, so it addresses a path inside their home rather than the
	// template itself. The grant subpath on the front is what does this.
	sub, err := c.Resolve(1, vpath(t, "Home/"+templateName), acl.Read)
	if err != nil {
		t.Fatalf("resolving under the home: %v", err)
	}
	if sub.Path().String() != "1/"+templateName {
		t.Fatalf("the template name resolved to %q, want it under the caller's own home",
			sub.Path().String())
	}
	// And nothing of the template's content is reachable there.
	if _, serr := sub.Root().Stat(sub.Path()); serr == nil {
		t.Fatal("the template's own directory was reachable through a home grant")
	}
}

func TestAHomeDirectoryWithNoGrantIsAdopted(t *testing.T) {
	c, st, host := homed(t)
	ctx := context.Background()

	// A crash between the mkdir and the grant leaves this shape. The next
	// call must tolerate the directory and persist the grant, rather than
	// failing and leaving the user without a home forever.
	if err := os.Mkdir(filepath.Join(host, "1"), 0o755); err != nil {
		t.Fatalf("simulating the crash: %v", err)
	}
	if err := c.ensureHome(ctx, 1); err != nil {
		t.Fatalf("adopting an existing directory: %v", err)
	}
	if got := len(homeGrants(t, st)); got != 1 {
		t.Fatalf("adoption left %d grants, want one", got)
	}
}

func TestAFreshAccountSeesItsHomeInTheFirstRootListing(t *testing.T) {
	c, st, host := homed(t)

	// The listing is what a client draws as the top-level folder list, and
	// it is often the first call an account makes. Without the eager hook
	// here the home appears only after some later call happens to resolve
	// one, so a new account opens the browser to nothing.
	roots := c.Roots(1)
	if len(roots) != 1 || roots[0].Label != homeLabel {
		t.Fatalf("the first listing is %+v, want the home", roots)
	}
	if _, err := os.Stat(filepath.Join(host, "1")); err != nil {
		t.Fatalf("the listing did not create the home: %v", err)
	}
	if got := len(homeGrants(t, st)); got != 1 {
		t.Fatalf("the listing left %d grants, want one", got)
	}
}

func TestHomesDisabledIsASilentNoOp(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	if err := c.ensureHome(context.Background(), 1); err != nil {
		t.Fatalf("ensureHome with homes off: %v", err)
	}
	if got := len(homeGrants(t, st)); got != 0 {
		t.Fatalf("homes were off and %d grants appeared", got)
	}
	if len(c.Roots(1)) != 0 {
		t.Fatal("a home appeared in the projected root with homes disabled")
	}
}

func TestAFailingHomeDoesNotBreakTheUsersOtherShares(t *testing.T) {
	c, st, host := homed(t)
	_, docsHost := share(t, c, 10, "documents")
	grantAt(t, c, st, 1, 10, "", "Documents", acl.Read)
	writeFile(t, docsHost, "note.txt", "body")

	// An unwritable homes host makes home creation fail. Resolve treats that
	// as warn-and-continue, because the failure domain of home creation must
	// not take down every other share the user can reach.
	if err := os.Chmod(host, 0o500); err != nil {
		t.Fatalf("sealing the homes host: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(host, 0o750); err != nil {
			t.Errorf("unsealing: %v", err)
		}
	})

	r, err := c.Resolve(1, vpath(t, "Documents/note.txt"), acl.Read)
	if err != nil {
		t.Fatalf("a failing home broke an unrelated share: %v", err)
	}
	if r.Path().Name() != "note.txt" {
		t.Fatalf("the resolution landed at %q", r.Path().String())
	}
}

func TestPersistGrantRefusesAGrantNamingNeitherOrBoth(t *testing.T) {
	_, st := newCore(t)
	ctx := context.Background()
	holder, group := int64(1), int64(2)

	if _, err := st.PersistGrant(ctx, state.GrantRow{Share: 10, Allow: 1}, 0); err == nil {
		t.Fatal("a grant naming neither an account nor a group was stored")
	}
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User: &holder, Group: &group, Share: 10, Allow: 1,
	}, 0); err == nil {
		t.Fatal("a grant naming both an account and a group was stored")
	}
}

func TestGrantWrappersReloadTheEvaluator(t *testing.T) {
	c, st, host, _ := writable(t)
	ctx := context.Background()
	seedUser(t, st, 2, "bob")
	writeFile(t, host, "note.txt", "body")

	holder := int64(2)
	id, err := c.CreateGrant(ctx, GrantSpec{
		User: &holder, Share: 10, Label: "Documents",
		Allow: acl.Read, Inherit: true,
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	// The reload is what makes the grant serve traffic now rather than after
	// a restart.
	if _, rerr := c.Resolve(2, vpath(t, "Documents/note.txt"), acl.Read); rerr != nil {
		t.Fatalf("the new grant does not serve: %v", rerr)
	}

	if err := c.UpdateGrant(ctx, id, acl.Read|acl.Download, 0, true, "Documents"); err != nil {
		t.Fatalf("UpdateGrant: %v", err)
	}
	if _, rerr := c.Resolve(2, vpath(t, "Documents/note.txt"), acl.Download); rerr != nil {
		t.Fatalf("the widened grant does not serve: %v", rerr)
	}

	if err := c.DeleteGrant(ctx, id); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	// Without the reload the row is gone and the evaluator keeps answering
	// from what it loaded at startup, so a revoked user keeps their access.
	if _, rerr := c.Resolve(2, vpath(t, "Documents/note.txt"), acl.Read); !errors.Is(rerr, ErrNotFound) {
		t.Fatalf("a revoked grant still resolves, returning %v", rerr)
	}
}
