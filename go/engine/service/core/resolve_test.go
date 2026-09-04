//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// grantAt persists one grant scoped to a subpath with an explicit permission
// set, then reloads the evaluator: the two-step discipline every grant write
// follows.
func grantAt(
	t *testing.T, c *Core, st *state.DB,
	user int64, share ShareID, subpath, label string, allow acl.Perms,
) {
	t.Helper()
	ctx := context.Background()
	holder := user
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: subpath,
		Allow:   uint16(allow),
		Inherit: true,
		Label:   label,
	}, 0); err != nil {
		t.Fatalf("persisting a grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

// share registers a share over a fresh host directory and returns both the
// definition and the host path, so a test can populate the tree.
func share(t *testing.T, c *Core, id ShareID, name string) (ShareDef, string) {
	t.Helper()
	d := def(t, id, name)
	if err := c.RegisterShare(context.Background(), d); err != nil {
		t.Fatalf("RegisterShare(%s): %v", name, err)
	}
	return d, d.Host
}

func vpath(t *testing.T, s string) vfs.Vpath {
	t.Helper()
	p, err := vfs.ParseVpath(s)
	if err != nil {
		t.Fatalf("parsing the vpath %q: %v", s, err)
	}
	return p
}

func safe(t *testing.T, s string) vfs.SafePath {
	t.Helper()
	p, err := vfs.ParseSafePath(s)
	if err != nil {
		t.Fatalf("parsing the safe path %q: %v", s, err)
	}
	return p
}

// readableShare is the setup most of these tests want: one user, one
// registered share holding one file, and a read grant over the whole thing.
func readableShare(t *testing.T) (c *Core, st *state.DB, host string) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")
	_, host = share(t, c, 10, "documents")
	if err := os.WriteFile(filepath.Join(host, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing a file into the share: %v", err)
	}
	grantRead(t, c, st, 1, 10, "Documents")
	return c, st, host
}

// TestMissingAndForbiddenAreOneAnswer is the core property: every refusal a
// stranger could use to map the tree answers with the same error, whether it
// arose at the grant table or at the filesystem.
func TestMissingAndForbiddenAreOneAnswer(t *testing.T) {
	c, st, _ := readableShare(t)
	// A second share this user holds no grant over at all.
	share(t, c, 20, "private")
	seedUser(t, st, 2, "bob")
	grantRead(t, c, st, 2, 20, "Private")

	// Each case yields the error a client would receive for the path.
	// Resolve does not stat, so the missing-path answer arises one layer
	// down, in the operation that used the resolution; the property is that
	// the two are the same string.
	touch := func(path string) error {
		r, err := c.Resolve(1, vpath(t, path), acl.Read)
		if err != nil {
			return err
		}
		_, sterr := r.Root().Stat(r.Path())
		return mapVFSErr(sterr)
	}

	cases := []struct {
		name string
		path string
	}{
		{"a label outside every grant", "nosuchlabel/a.txt"},
		{"a label naming a share the caller cannot read", "Private/a.txt"},
		{"a path missing inside a granted share", "Documents/gone.txt"},
		{"an absolute host path", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := touch(tc.path)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("reaching %q = %v, want ErrNotFound", tc.path, err)
			}
			if err.Error() != ErrNotFound.Error() {
				t.Fatalf("reaching %q said %q, want the bare %q so the layer is not observable",
					tc.path, err, ErrNotFound)
			}
		})
	}
}

func TestADenialIsOnlyEarnedAfterTheLabelMatched(t *testing.T) {
	c, _, _ := readableShare(t)

	_, err := c.Resolve(1, vpath(t, "Documents/readme.txt"), acl.Write)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("resolving with a permission the read-only grant lacks = %v, want ErrDenied", err)
	}

	// The same missing permission under a label the caller does not hold is
	// not a denial at all.
	if _, err := c.Resolve(1, vpath(t, "Elsewhere/readme.txt"), acl.Write); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolving under an unmatched label = %v, want ErrNotFound", err)
	}
}

func TestTheVirtualRootIsNeverResolved(t *testing.T) {
	c, _, _ := readableShare(t)
	for _, raw := range []string{"", "/"} {
		if _, err := c.Resolve(1, vpath(t, raw), acl.Read); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Resolve(%q) = %v, want ErrNotFound", raw, err)
		}
	}
}

func TestTraversalIsRefusedAtParseOrAtResolve(t *testing.T) {
	c, _, _ := readableShare(t)

	// Refused at the parse boundary, before a Vpath exists at all.
	for _, raw := range []string{"Documents/../../etc/passwd", "Documents//a.txt", "Documents/./a"} {
		if _, err := vfs.ParseVpath(raw); !errors.Is(err, vfs.ErrInvalidName) {
			t.Fatalf("ParseVpath(%q) = %v, want ErrInvalidName", raw, err)
		}
	}

	// A backslash is not a separator here, so it names one ordinary
	// component that simply is not on disk.
	r, err := c.Resolve(1, vpath(t, `Documents/a\b`), acl.Read)
	if err != nil {
		t.Fatalf(`resolving "a\b": %v`, err)
	}
	if got := r.Path().String(); got != `a\b` {
		t.Fatalf(`the backslash name resolved to %q, want "a\b" as one component`, got)
	}
}

func TestASymlinkOutOfTheShareIsRefusedByTheRootsPolicy(t *testing.T) {
	c, _, host := readableShare(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("writing the file outside the share: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(host, "escape.txt")); err != nil {
		t.Fatalf("creating the escaping symlink: %v", err)
	}

	// Resolution itself does not touch disk, so the refusal lands where the
	// path is used. What this asserts is that the core wires the root whose
	// policy refuses it.
	r, err := c.Resolve(1, vpath(t, "Documents/escape.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving a path naming a symlink: %v", err)
	}
	if _, err := r.Root().Stat(r.Path()); !errors.Is(err, vfs.ErrSymlinkDenied) {
		t.Fatalf("stat through the resolved root = %v, want ErrSymlinkDenied", err)
	}
	if _, err := pathExists(r.Root(), r.Path()); !errors.Is(err, ErrDenied) {
		t.Fatalf("pathExists over an escaping symlink = %v, want ErrDenied", err)
	}
}

func TestAGrantSubpathIsLaidOnTheFrontOfTheClientPath(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	_, host := share(t, c, 10, "documents")
	if err := os.MkdirAll(filepath.Join(host, "a", "b", "c"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	grantAt(t, c, st, 1, 10, "a/b", "Scoped", acl.Read|acl.Write)

	r, err := c.Resolve(1, vpath(t, "Scoped/c"), acl.Read)
	if err != nil {
		t.Fatalf("resolving under a scoped grant: %v", err)
	}
	if got := r.Path().String(); got != "a/b/c" {
		t.Fatalf("the resolved path is %q, want a/b/c", got)
	}
	if !r.Has(acl.Write) {
		t.Fatalf("the resolution carries perms %v, want the subpath grant's write bit", r.Perms())
	}
	// The grant scopes the caller to its subtree, so a sibling of the
	// subpath is not reachable by naming it.
	if _, err := c.Resolve(1, vpath(t, "a/b"), acl.Read); !errors.Is(err, ErrNotFound) {
		t.Fatalf("naming the share-relative path directly = %v, want ErrNotFound", err)
	}
}

func TestANameTheCreationTableRefusesStillResolves(t *testing.T) {
	c, _, host := readableShare(t)
	if err := os.Mkdir(filepath.Join(host, "CON"), 0o755); err != nil {
		t.Fatalf("creating the CON directory: %v", err)
	}

	r, err := c.Resolve(1, vpath(t, "Documents/CON"), acl.Read)
	if err != nil {
		t.Fatalf("resolving a directory named CON: %v", err)
	}
	if _, err := r.Root().Stat(r.Path()); err != nil {
		t.Fatalf("stating the resolved CON directory: %v", err)
	}
	// Creating that name through this server is the half that is refused.
	if err := requireCreatableLeaf(r.Path()); !errors.Is(err, vfs.ErrInvalidName) {
		t.Fatalf("requireCreatableLeaf(CON) = %v, want ErrInvalidName", err)
	}
}

func TestRequireCreatableLeafPassesTheRootAndAnOrdinaryName(t *testing.T) {
	if err := requireCreatableLeaf(vfs.RootPath()); err != nil {
		t.Fatalf("requireCreatableLeaf on the share root: %v", err)
	}
	if err := requireCreatableLeaf(safe(t, "a/b/report.txt")); err != nil {
		t.Fatalf("requireCreatableLeaf on an ordinary name: %v", err)
	}
	if err := requireCreatableLeaf(safe(t, "a/b:c")); !errors.Is(err, vfs.ErrInvalidName) {
		t.Fatalf("requireCreatableLeaf on a colon name = %v, want ErrInvalidName", err)
	}
}

func TestABrokenShareSaysSoRatherThanReportingThePathMissing(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	c.RegisterBroken(ShareDef{ID: 11, Name: "archive"}, vfs.ErrNotFound)
	grantRead(t, c, st, 1, 11, "Archive")

	_, err := c.Resolve(1, vpath(t, "Archive/a.txt"), acl.Read)
	var broken *ShareBrokenError
	if !errors.As(err, &broken) {
		t.Fatalf("resolving into a broken share = %v, want a ShareBrokenError", err)
	}
	if broken.Share != "archive" || broken.Reason != "missing" {
		t.Fatalf("the broken error is %+v, want archive/missing", broken)
	}
	if !errors.Is(err, ErrShareBroken) {
		t.Fatal("the broken error does not match ErrShareBroken")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a broken share reported the caller's own path as missing")
	}
}

func TestACorruptGrantRefusesTheResolutionRatherThanGuessing(t *testing.T) {
	t.Run("a share id that does not fit", func(t *testing.T) {
		c, st := newCore(t)
		seedUser(t, st, 1, "ada")
		grantRead(t, c, st, 1, 10, "Documents")
		// Rewrite the loaded grant's share id past uint32 without going
		// through the store, which refuses nothing here but would make the
		// registry lookup the thing that failed instead.
		c.acl.ReplaceGrants([]acl.Grant{{
			ID: 1, User: 1, Share: 1 << 40, Allow: acl.Read, Inherit: true, Label: "Documents",
		}})

		_, err := c.Resolve(1, vpath(t, "Documents/a.txt"), acl.Read)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("an overflowing share id = %v, want ErrNotFound", err)
		}
		if err.Error() == ErrNotFound.Error() {
			t.Fatal("the corrupt-grant refusal carries no context")
		}
	})

	t.Run("a subpath component that does not validate", func(t *testing.T) {
		c, st := newCore(t)
		seedUser(t, st, 1, "ada")
		share(t, c, 10, "documents")
		grantRead(t, c, st, 1, 10, "Documents")
		c.acl.ReplaceGrants([]acl.Grant{{
			ID:      1,
			User:    1,
			Share:   10,
			Subpath: acl.NewPath("a", ".scmeta"),
			Allow:   acl.Read,
			Inherit: true,
			Label:   "Documents",
		}})

		_, err := c.Resolve(1, vpath(t, "Documents/b.txt"), acl.Read)
		if !errors.Is(err, vfs.ErrReservedName) {
			t.Fatalf("a grant subpath naming a control prefix = %v, want ErrReservedName", err)
		}
	})
}

func TestAResolutionIsACapabilityCarryingOnlyItsOwnBits(t *testing.T) {
	c, _, _ := readableShare(t)
	r, err := c.Resolve(1, vpath(t, "Documents/readme.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if !r.Has(acl.Read) {
		t.Fatal("a read resolution does not report the read bit")
	}
	if r.Has(acl.Write) {
		t.Fatalf("a read-only resolution reports perms %v, which include write", r.Perms())
	}
	if err := r.Require(acl.Read); err != nil {
		t.Fatalf("Require(Read) on a read resolution: %v", err)
	}
	if err := r.Require(acl.Write); !errors.Is(err, ErrDenied) {
		t.Fatalf("Require(Write) = %v, want ErrDenied", err)
	}
	if r.User() != 1 || r.Share() != 10 {
		t.Fatalf("the resolution reports user %d share %d, want 1 and 10", r.User(), r.Share())
	}
	if r.Root() == nil {
		t.Fatal("the resolution carries no root")
	}
}

func TestResolveUnderNarrowsAndNeverWidens(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	_, host := share(t, c, 10, "documents")
	if err := os.MkdirAll(filepath.Join(host, "a", "deep"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	grantAt(t, c, st, 1, 10, "", "Documents", acl.Read|acl.Download)

	parent, err := c.Resolve(1, vpath(t, "Documents/a"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the parent: %v", err)
	}

	child, err := c.ResolveUnder(parent, safe(t, "a/deep"), acl.Read)
	if err != nil {
		t.Fatalf("descending to a child: %v", err)
	}
	if child.Path().String() != "a/deep" {
		t.Fatalf("the child path is %q, want a/deep", child.Path())
	}
	if child.User() != parent.User() || child.Share() != parent.Share() ||
		child.Root() != parent.Root() || child.Perms() != parent.Perms() {
		t.Fatal("the child does not carry the parent's user, share, root and perms")
	}

	// A byte-prefix sibling is not under the parent, so it is refused before
	// any permission logic runs.
	if _, err := c.ResolveUnder(parent, safe(t, "ab"), acl.Read); !errors.Is(err, ErrDenied) {
		t.Fatalf("descending to a byte-prefix sibling = %v, want ErrDenied", err)
	}
	if _, err := c.ResolveUnder(parent, safe(t, "other"), acl.Read); !errors.Is(err, ErrDenied) {
		t.Fatalf("descending outside the subtree = %v, want ErrDenied", err)
	}
	// A bit the parent never held cannot be picked up by descending.
	if _, err := c.ResolveUnder(parent, safe(t, "a/deep"), acl.Write); !errors.Is(err, ErrDenied) {
		t.Fatalf("descending with a permission the parent lacks = %v, want ErrDenied", err)
	}
}

func TestEntryAtProjectsTheResolvedPathItself(t *testing.T) {
	c, _, _ := readableShare(t)
	r, err := c.Resolve(1, vpath(t, "Documents/readme.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	st, err := r.Root().Stat(r.Path())
	if err != nil {
		t.Fatalf("stating the resolved path: %v", err)
	}

	e := c.EntryAt(r, st)
	if e.Name != "readme.txt" || e.Path.String() != "readme.txt" {
		t.Fatalf("the entry is named %q at %q, want readme.txt", e.Name, e.Path)
	}
	if e.IsDir || e.Kind != vfs.KindFile {
		t.Fatalf("the entry reports kind %v isDir %v, want a file", e.Kind, e.IsDir)
	}
	if e.Size != 5 {
		t.Fatalf("the entry reports size %d, want 5", e.Size)
	}
	if e.Ident.Share != 10 || e.Ident.Ino != st.Ino || e.Ident.Dev != st.Dev {
		t.Fatalf("the entry's identity is %+v, want the share and the stat's dev/ino", e.Ident)
	}
	wantETag, wantWeak := FileETag(st)
	if e.ETag != wantETag || e.ETagWeak != wantWeak {
		t.Fatalf("the entry's validator is %q/%v, want %q/%v", e.ETag, e.ETagWeak, wantETag, wantWeak)
	}
	if e.Perms != r.Perms() {
		t.Fatalf("the entry carries perms %v, want the resolution's %v", e.Perms, r.Perms())
	}
}

func TestVpathForRoundTripsWithResolveAndRefusesAnInvisibleShare(t *testing.T) {
	c, st, _ := readableShare(t)

	rest, err := vfs.ParseSharePath("readme.txt")
	if err != nil {
		t.Fatalf("parsing the share path: %v", err)
	}
	p, err := c.VpathFor(1, 10, rest)
	if err != nil {
		t.Fatalf("VpathFor: %v", err)
	}
	if p.String() != "Documents/readme.txt" {
		t.Fatalf("VpathFor produced %q, want Documents/readme.txt", p)
	}
	r, err := c.Resolve(1, p, acl.Read)
	if err != nil {
		t.Fatalf("resolving the produced vpath: %v", err)
	}
	if r.Path().String() != "readme.txt" || r.Share() != 10 {
		t.Fatalf("the round trip landed at share %d path %q", r.Share(), r.Path())
	}

	// A share the user holds no readable grant over has no label to project
	// it under, so there is no URL to give them.
	share(t, c, 20, "private")
	seedUser(t, st, 2, "bob")
	grantRead(t, c, st, 2, 20, "Private")
	if _, err := c.VpathFor(1, 20, rest); err == nil {
		t.Fatal("VpathFor produced a path under a share the user cannot see")
	}
}

// grantSubpathRead persists a read grant over one folder inside a share, which
// is what an account granted a subfolder rather than the whole share holds.
func grantSubpathRead(
	t *testing.T, c *Core, st *state.DB, user int64, share ShareID, subpath, label string,
) {
	t.Helper()
	ctx := context.Background()
	holder := user
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: subpath,
		Allow:   uint16(acl.Read | acl.Download),
		Inherit: true,
		Label:   label,
	}, 0); err != nil {
		t.Fatalf("persisting a subpath grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

// A grant over a folder inside a share projects that folder as the root the
// client sees, so crossing back out must not put the subpath on the front
// again. It did, and the URL for a file the account could see named a path
// that does not exist: "Game/Game/file" for a grant over "Game".
//
// Two depths, because the bug is the subpath's length: one component and
// three, and the second is what proves nothing is hard-coded to one level.
func TestVpathForStripsTheGrantSubpathAtAnyDepth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		subpath  string
		label    string
		relative string
		want     string
	}{
		{"one level", "Game", "Game", "Game/save.dat", "Game/save.dat"},
		{"one level, nested file", "Game", "Game", "Game/mods/a.pak", "Game/mods/a.pak"},
		{"three levels", "a/b/c", "Deep", "a/b/c/file.txt", "Deep/file.txt"},
		{"three levels, nested", "a/b/c", "Deep", "a/b/c/d/e.txt", "Deep/d/e.txt"},
		// The granted folder itself, which is the projected root.
		{"the folder itself", "Game", "Game", "Game", "Game"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, st := newCore(t)
			seedUser(t, st, 1, "ada")
			share(t, c, 10, "share")
			grantSubpathRead(t, c, st, 1, 10, tc.subpath, tc.label)

			rest, err := vfs.ParseSharePath(tc.relative)
			if err != nil {
				t.Fatalf("parsing the share path: %v", err)
			}
			got, err := c.VpathFor(1, 10, rest)
			if err != nil {
				t.Fatalf("VpathFor: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("VpathFor produced %q, want %q", got, tc.want)
			}

			// And the answer resolves back to the share-relative path it came
			// from, which is what makes the URL fetch the file it names.
			r, rerr := c.Resolve(1, got, acl.Read)
			if rerr != nil {
				t.Fatalf("resolving the produced vpath: %v", rerr)
			}
			if r.Path().String() != tc.relative {
				t.Fatalf("the round trip landed at %q, want %q", r.Path(), tc.relative)
			}
		})
	}
}

// Two grants on one share, each projected under its own label. The answer for
// a path is the label whose subpath actually contains it, and where both
// contain it the deeper one wins: that is the folder the client is looking at.
func TestVpathForPicksTheDeepestGrantThatContainsThePath(t *testing.T) {
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")
	share(t, c, 10, "share")
	grantSubpathRead(t, c, st, 1, 10, "team", "Team")
	grantSubpathRead(t, c, st, 1, 10, "team/game", "Game")

	for _, tc := range []struct {
		relative, want string
	}{
		{"team/notes.txt", "Team/notes.txt"},
		{"team/game/save.dat", "Game/save.dat"},
		{"team/game", "Game"},
	} {
		rest, err := vfs.ParseSharePath(tc.relative)
		if err != nil {
			t.Fatalf("parsing %q: %v", tc.relative, err)
		}
		got, err := c.VpathFor(1, 10, rest)
		if err != nil {
			t.Fatalf("VpathFor(%q): %v", tc.relative, err)
		}
		if got.String() != tc.want {
			t.Errorf("VpathFor(%q) = %q, want %q", tc.relative, got, tc.want)
		}
	}

	// A path outside every grant on the share has no label to project it
	// under, so there is no URL to hand back.
	outside, err := vfs.ParseSharePath("elsewhere/secret.txt")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if _, err := c.VpathFor(1, 10, outside); err == nil {
		t.Error("VpathFor produced a path for a folder no grant covers")
	}
}

func TestPathExistsFoldsMissingButNotARefusal(t *testing.T) {
	c, _, host := readableShare(t)
	r, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}

	ok, err := pathExists(r.Root(), safe(t, "readme.txt"))
	if err != nil || !ok {
		t.Fatalf("pathExists over a present file = %v, %v, want true and no error", ok, err)
	}

	ok, err = pathExists(r.Root(), safe(t, "gone.txt"))
	if err != nil || ok {
		t.Fatalf("pathExists over a missing file = %v, %v, want false and no error", ok, err)
	}

	// An unreadable parent is an error, never a "no": folding it to false
	// would let a mutation treat an occupied destination as free.
	if merr := os.MkdirAll(filepath.Join(host, "locked", "inner"), 0o755); merr != nil {
		t.Fatalf("building the locked tree: %v", merr)
	}
	if cerr := os.Chmod(filepath.Join(host, "locked"), 0o000); cerr != nil {
		t.Fatalf("removing the directory's permissions: %v", cerr)
	}
	t.Cleanup(func() {
		if cerr := os.Chmod(filepath.Join(host, "locked"), 0o755); cerr != nil {
			t.Errorf("restoring the directory's permissions: %v", cerr)
		}
	})
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the refusal cannot be produced")
	}
	ok, err = pathExists(r.Root(), safe(t, "locked/inner"))
	if ok {
		t.Fatal("pathExists reported a path under an unreadable directory as present")
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("pathExists under an unreadable directory = %v, want ErrDenied", err)
	}
}

func TestUniqueSiblingNameSuffixesTheStemNotTheExtension(t *testing.T) {
	c, _, host := readableShare(t)
	r, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	write := func(name string) {
		t.Helper()
		if werr := os.WriteFile(filepath.Join(host, name), nil, 0o644); werr != nil {
			t.Fatalf("writing %q: %v", name, werr)
		}
	}

	write("a.txt")
	got, err := c.uniqueSiblingName(r.Root(), safe(t, "a.txt"))
	if err != nil {
		t.Fatalf("uniqueSiblingName: %v", err)
	}
	if got.String() != "a (2).txt" {
		t.Fatalf("uniqueSiblingName produced %q, want a (2).txt", got)
	}

	write("a (2).txt")
	got, err = c.uniqueSiblingName(r.Root(), safe(t, "a.txt"))
	if err != nil {
		t.Fatalf("uniqueSiblingName with the first suffix taken: %v", err)
	}
	if got.String() != "a (3).txt" {
		t.Fatalf("uniqueSiblingName produced %q, want a (3).txt", got)
	}

	// A leading dot is a hidden file's name, not an extension.
	write(".bashrc")
	got, err = c.uniqueSiblingName(r.Root(), safe(t, ".bashrc"))
	if err != nil {
		t.Fatalf("uniqueSiblingName over a dotfile: %v", err)
	}
	if got.String() != ".bashrc (2)" {
		t.Fatalf("uniqueSiblingName produced %q, want .bashrc (2)", got)
	}

	write("noext")
	got, err = c.uniqueSiblingName(r.Root(), safe(t, "noext"))
	if err != nil {
		t.Fatalf("uniqueSiblingName over a dotless name: %v", err)
	}
	if got.String() != "noext (2)" {
		t.Fatalf("uniqueSiblingName produced %q, want noext (2)", got)
	}
}

func TestUniqueSiblingNameGivesUpAtItsBound(t *testing.T) {
	c, _, host := readableShare(t)
	r, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	// A lowered bound, so the refusal is exercised without minting ten
	// thousand files.
	for _, name := range []string{"a.txt", "a (2).txt", "a (3).txt"} {
		if werr := os.WriteFile(filepath.Join(host, name), nil, 0o644); werr != nil {
			t.Fatalf("writing %q: %v", name, werr)
		}
	}
	if _, err := c.uniqueSiblingNameWithin(r.Root(), safe(t, "a.txt"), 4); !errors.Is(err, ErrConflict) {
		t.Fatalf("uniqueSiblingNameWithin past its bound = %v, want ErrConflict", err)
	}
}

func TestLastDot(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a.b", 1},
		{".bashrc", 0},
		{"noext", -1},
		{"a.b.c", 3},
		{"", -1},
	}
	for _, tc := range cases {
		if got := lastDot(tc.in); got != tc.want {
			t.Fatalf("lastDot(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
