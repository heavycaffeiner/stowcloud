package acl

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// The two splits a reimplementation collapses by accident stay separate.
func TestPermSplitsStaySeparate(t *testing.T) {
	if Read == Download {
		t.Fatal("DOWNLOAD collapsed into READ: every view-only grant now hands out the bytes")
	}
	if Rename == Move {
		t.Fatal("MOVE collapsed into RENAME: an account can now carry a file out of its only granted subtree")
	}
}

func grant(id, user, share int64, subpath string, allow, deny Perms, inherit bool) Grant {
	return Grant{ID: id, User: user, Share: share, Subpath: ParsePath(subpath), Allow: allow, Deny: deny, Inherit: inherit}
}

func TestEvaluateDepthFirst(t *testing.T) {
	e := NewEvaluator()
	e.ReplaceGrants([]Grant{
		grant(1, 7, 1, "", Read|Write, 0, true), // shallow allow at the root
		grant(2, 7, 1, "b", 0, Read, true),      // a deeper deny of READ
		grant(3, 7, 1, "b", Read, 0, true),      // same-depth allow of READ
	})
	v := Vpath{Share: 1, Path: NewPath("b", "c", "d")}

	// At depth 1 the deny of READ and an allow of READ sit together; the
	// same-depth DENY wins.
	if d := e.Evaluate(7, v, Read); d.Allowed {
		t.Errorf("a same-depth DENY of READ did not beat the allow: %+v", d)
	}
	// WRITE is not denied at depth 1, so the search falls back to the root
	// grant, which allows it.
	if d := e.Evaluate(7, v, Write); !d.Allowed || d.By != 1 {
		t.Errorf("WRITE resolved to %+v, want the root grant", d)
	}
}

func TestEffectiveComposesAcrossDepths(t *testing.T) {
	e := NewEvaluator()
	e.SetMemberships(membership{})
	e.ReplaceGrants([]Grant{
		grant(1, 7, 1, "a", Write, 0, true),
		grant(2, 7, 1, "a/b", Read|Download, 0, true),
	})
	v := Vpath{Share: 1, Path: NewPath("a", "b", "c")}
	// READ|WRITE|DOWNLOAD each granted by one grant, composed by the single-bit
	// probe.
	if got := e.Effective(7, v); !got.Has(Read | Write | Download) {
		t.Errorf("Effective = %v, want read write download", got)
	}
}

// The existence rule: a path outside every grant answers identically to a
// path that does not exist, both as zero permissions and no error.
func TestExistenceRuleForOutOfGrantPaths(t *testing.T) {
	e := NewEvaluator()
	e.ReplaceGrants([]Grant{grant(1, 7, 1, "visible", Read, 0, true)})

	inside := Vpath{Share: 1, Path: NewPath("visible", "file")}
	outside := Vpath{Share: 1, Path: NewPath("secret", "file")}
	nowhere := Vpath{Share: 2, Path: NewPath("anywhere")}

	if got := e.Effective(7, inside); !got.Has(Read) {
		t.Errorf("inside grant: Effective = %v, want read", got)
	}
	if got := e.Effective(7, outside); got != 0 {
		t.Errorf("outside grant: Effective = %v, want zero", got)
	}
	if got := e.Effective(7, nowhere); got != 0 {
		t.Errorf("no grant at all: Effective = %v, want zero", got)
	}
	// The HTTP layer turns both the outside and the nowhere into the same 404.
	if d := e.Evaluate(7, outside, Read); d.Allowed {
		t.Errorf("outside path was allowed: %+v", d)
	}
	if d := e.Evaluate(7, nowhere, Read); d.Allowed {
		t.Errorf("nonexistent path was allowed: %+v", d)
	}
}

func TestGroupMembershipApplies(t *testing.T) {
	e := NewEvaluator()
	e.SetMemberships(membership{7: {3}})
	e.ReplaceGrants([]Grant{grant(1, 0, 1, "", Read, 0, true).withGroup(3)})

	if d := e.Evaluate(7, Vpath{Share: 1, Path: NewPath("x")}, Read); !d.Allowed {
		t.Errorf("a group grant did not apply to a member: %+v", d)
	}
	if d := e.Evaluate(8, Vpath{Share: 1, Path: NewPath("x")}, Read); d.Allowed {
		t.Errorf("a non-member was allowed by a group grant: %+v", d)
	}
}

func (g Grant) withGroup(id int64) Grant { g.Group = id; g.User = 0; return g }

// LoadFromState reads grants and memberships from a real store and evaluates
// against them.
func TestLoadFromState(t *testing.T) {
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	st := state.New(f)
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (7, 'alice', 'x', 0)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO "group"(id, name) VALUES (3, 'staff')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO membership(user, "group") VALUES (7, 3)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO "grant"(id, "group", share, subpath, allow, deny, inherit, created_ns)
			 VALUES (1, 3, 1, 'docs', 1, 0, 1, 0)`); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding the store: %v", err)
	}

	e := NewEvaluator()
	if err := e.LoadFromState(ctx, st.SQL()); err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	if d := e.Evaluate(7, Vpath{Share: 1, Path: NewPath("docs", "a")}, Read); !d.Allowed {
		t.Errorf("a grant read from the store did not apply to a member: %+v", d)
	}
	if got := e.MembershipOf(7); len(got) != 1 || got[0] != 3 {
		t.Errorf("MembershipOf(7) = %v, want [3]", got)
	}
}

// Roots builds the virtual-root projection: one entry per READ-granted rule,
// labeled with the grant's label, and the full effective permission set there.
func TestRoots(t *testing.T) {
	e := NewEvaluator()
	e.ReplaceGrants([]Grant{
		{ID: 1, User: 7, Share: 1, Subpath: NewPath(), Label: "docs", Allow: Read, Inherit: true},
		{ID: 2, User: 7, Share: 2, Subpath: NewPath("media"), Label: "media", Allow: Read | Download, Inherit: true},
		// A deny-only rule contributes no root.
		{ID: 3, User: 7, Share: 3, Subpath: NewPath(), Deny: Delete, Inherit: true},
	})

	roots := e.Roots(7)
	if len(roots) != 2 {
		t.Fatalf("Roots(7) = %d entries, want 2", len(roots))
	}
	if roots[0].Label != "docs" || roots[0].Share != 1 {
		t.Errorf("roots[0] = %+v, want label docs share 1", roots[0])
	}
	if !roots[1].Perms.Has(Read) || !roots[1].Perms.Has(Download) || roots[1].Subpath.String() != "/media" {
		t.Errorf("roots[1] = %+v, want read+download at /media", roots[1])
	}

	// A user with no grants sees an empty root.
	if got := e.Roots(99); len(got) != 0 {
		t.Errorf("Roots(99) = %v, want empty", got)
	}
}

// Roots disambiguates a label collision with a " (2)" suffix in encounter
// order, which is what keeps two shares of the same name apart in the client.
func TestRootsLabelCollision(t *testing.T) {
	e := NewEvaluator()
	e.ReplaceGrants([]Grant{
		{ID: 1, User: 7, Share: 1, Subpath: NewPath(), Label: "docs", Allow: Read, Inherit: true},
		{ID: 2, User: 7, Share: 2, Subpath: NewPath(), Label: "docs", Allow: Read, Inherit: true},
	})
	roots := e.Roots(7)
	if len(roots) != 2 || roots[0].Label != "docs" || roots[1].Label != "docs (2)" {
		t.Fatalf("colliding labels = %+v, want docs and docs (2)", roots)
	}
}
