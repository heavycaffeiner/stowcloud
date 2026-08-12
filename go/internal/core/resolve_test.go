//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The existence rule, applied in one place: a path outside every grant and a
// path that does not exist produce byte-identical responses. The two cases
// share one table because the property is that they are indistinguishable,
// and a test that asserts each one separately can pass for the wrong reason.

func testClock() clock.Clock { return clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000)) }

// testCore builds a Core over a temp data directory with one share and the
// grants needed to resolve it. It returns the core, the store and the share id.
func testCore(t *testing.T) (*Core, *store.Store, ShareID) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir, store.Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	ev := acl.NewEvaluator()
	c, err := New(s, Options{ACL: ev, Clock: testClock()})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}

	host := t.TempDir()
	if err := os.MkdirAll(filepath.Join(host, "docs"), 0o775); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(host, "docs", "a.txt"), []byte("hello"), 0o664); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := c.RegisterShare(context.Background(), ShareDef{
		ID:     1,
		Name:   "docs",
		Host:   host,
		Policy: vfs.DefaultSharePolicy(),
	}); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}

	// One grant: user 42 has READ on the whole share root.
	g := acl.Grant{User: 42, Share: 1, Subpath: acl.NewPath(), Allow: acl.Read, Inherit: true}
	// The evaluator is empty until we persist the grant. Persist it directly.
	if err := insertGrant(s, g, 1); err != nil {
		t.Fatalf("inserting the grant: %v", err)
	}
	if err := ev.LoadFromState(ctx(), s.State().SQL()); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
	return c, s, 1
}

func ctx() context.Context { return context.Background() }

// insertGrant writes one grant row and returns.
func insertGrant(s *store.Store, g acl.Grant, createdNs int64) error {
	return s.State().Write(ctx(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx(), grantInsertStmt,
			g.User, gidArg(g.Group), g.Share, g.Subpath.String(),
			int64(g.Allow), int64(g.Deny), inheritInt(g.Inherit), g.Label, createdNs)
		return err
	})
}

func gidArg(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// TestOutsideGrantAndMissingAreIndistinguishable is the "Done when" proof: a
// path outside the caller's grant and a path that simply does not exist produce
// the same answer, in a table test. The two failures arrive at different
// layers (Resolve for the first, the operation for the second) but must be the
// same sentinel, because a protocol layer maps both to the same 404 and a
// caller must not be able to tell them apart.
func TestOutsideGrantAndMissingAreIndistinguishable(t *testing.T) {
	c, _, _ := testCore(t)

	cases := []struct {
		name string
		run  func() error
	}{
		{
			"a label outside every grant",
			func() error {
				v, _ := vfs.ParseVpath("nonexistent")
				_, err := c.Resolve(UserID(42), v, acl.Read)
				return err
			},
		},
		{
			"a label the user cannot read",
			func() error {
				v, _ := vfs.ParseVpath("noread")
				_, err := c.Resolve(UserID(42), v, acl.Read)
				return err
			},
		},
		{
			"a path that does not exist inside the grant",
			func() error {
				r, err := resolve(t, c, "docs/missing_dir", acl.Read)
				if err != nil {
					return err
				}
				_, err = c.List(ctx(), r, "")
				return err
			},
		},
	}

	var baseline error
	for i, tc := range cases {
		err := tc.run()
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("case %d (%s): got %v, want ErrNotFound", i, tc.name, err)
		}
		if i == 0 {
			baseline = err
		} else if err == nil || baseline == nil || err.Error() != baseline.Error() {
			t.Errorf("case %d (%s) answers %q, baseline %q; the two must be byte-identical",
				i, tc.name, err, baseline)
		}
	}
}

// TestResolveDeniesWithoutOverridingNotFound covers the other half of the
// rule: a 403 can only be earned by a caller who may know the target exists.
func TestResolveDeniesWithinAReadableShare(t *testing.T) {
	c, _, _ := testCore(t)

	v, _ := vfs.ParseVpath("docs/a.txt")
	_, err := c.Resolve(UserID(42), v, acl.Write)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Resolve(write) = %v, want ErrDenied (the caller knows the share exists)", err)
	}
}

// TestResolvedCanOnlyBeObtainedThroughResolve is the ACL gate's construction
// proof at the type level: nothing in the test (or any other package) can
// build a Resolved literal, because the fields are unexported.
func TestResolvedCanOnlyBeObtainedThroughResolve(t *testing.T) {
	c, _, _ := testCore(t)
	v, _ := vfs.ParseVpath("docs/a.txt")
	r, err := c.Resolve(UserID(42), v, acl.Read)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !r.Has(acl.Read) || r.Has(acl.Write) {
		t.Fatalf("perms = %v, want read only", r.Perms())
	}
}

var _ = errors.Is

// resolve is a test convenience over the single gate.
func resolve(t *testing.T, c *Core, p string, need acl.Perms) (Resolved, error) {
	t.Helper()
	v, perr := vfs.ParseVpath(p)
	if perr != nil {
		t.Fatalf("ParseVpath(%q): %v", p, perr)
	}
	return c.Resolve(UserID(42), v, need)
}

var (
	reqRead  = acl.Read
	reqWrite = acl.Write
)
