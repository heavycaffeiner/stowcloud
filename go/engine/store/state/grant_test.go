package state_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The homes share is registered live under a reserved id and never gets a
// share_definition row, which is exactly why grant.share carries no foreign
// key. An immediately-enforced key would refuse every home grant.
const homeShare int64 = 999_999

func TestPersistGrantRefusesAGrantThatSaysNothing(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	seedGroup(t, d, 1, "g")

	for name, g := range map[string]state.GrantRow{
		"neither principal":         {Share: 5, Allow: 1},
		"both principals":           {User: id64(1), Group: id64(1), Share: 5, Allow: 1},
		"no share":                  {User: id64(1), Allow: 1},
		"neither allows nor denies": {User: id64(1), Share: 5},
	} {
		if _, err := d.PersistGrant(ctx, g, 1); !errors.Is(err, state.ErrGrantMalformed) {
			t.Errorf("%s returned %v, want a refusal", name, err)
		}
	}

	// And nothing was written on the way to any of those refusals.
	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d grants exist after four refusals", len(got))
	}
}

// A grant that only denies is how an inherited allow is carved back out for
// one subtree. Refusing it made the exception unexpressible, so a folder
// inside a granted tree could not be closed off.
func TestPersistGrantStoresADenyOnlyGrant(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id, err := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 5, Subpath: "team/private", Deny: 0b1, Inherit: true,
	}, 1)
	if err != nil {
		t.Fatalf("a deny-only grant was refused: %v", err)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Allow != 0 || got[0].Deny != 0b1 {
		t.Fatalf("the stored grant is %+v", got)
	}
}

// A second grant for a subject that already holds one over the same share and
// subpath is refused by the unique index rather than stored as an
// indistinguishable duplicate. The first grant survives untouched.
func TestPersistGrantRefusesADuplicateOverTheSameShareAndSubpath(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	g := state.GrantRow{User: id64(1), Share: 5, Subpath: "team/private", Allow: 0b1}
	id, err := d.PersistGrant(ctx, g, 1)
	if err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}

	if _, derr := d.PersistGrant(ctx, g, 2); !errors.Is(derr, state.ErrGrantAlreadyExists) {
		t.Fatalf("a duplicate grant returned %v, want ErrGrantAlreadyExists", derr)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("after the refusal the grants are %+v, want only the original", got)
	}
}

func TestPersistGrantRoundTripsAndStampsTheCallersClock(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 7, "u")

	want := state.GrantRow{
		User: id64(7), Share: 42, Subpath: "docs/private",
		Allow: 0b1011, Deny: 0b0100, Inherit: true, Label: "read only",
	}
	id, err := d.PersistGrant(ctx, want, 1_700_000_000)
	if err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d grants, want 1", len(got))
	}
	g := got[0]
	if g.ID != id {
		t.Errorf("id %d, want %d", g.ID, id)
	}
	if g.User == nil || *g.User != 7 || g.Group != nil {
		t.Errorf("principal came back as user %v, group %v", g.User, g.Group)
	}
	if g.Share != 42 || g.Subpath != "docs/private" {
		t.Errorf("target came back as share %d, subpath %q", g.Share, g.Subpath)
	}
	if g.Allow != want.Allow || g.Deny != want.Deny || !g.Inherit || g.Label != want.Label {
		t.Errorf("read back %+v, want %+v", g, want)
	}
	if g.CreatedNs != 1_700_000_000 {
		t.Errorf("stamped %d, want the caller's clock value", g.CreatedNs)
	}
}

func TestAGroupGrantStoresNoUser(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedGroup(t, d, 3, "editors")

	if _, err := d.PersistGrant(ctx, state.GrantRow{Group: id64(3), Share: 1, Allow: 1}, 1); err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}
	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if got[0].User != nil {
		t.Errorf("a group grant carries user %d", *got[0].User)
	}
	if got[0].Group == nil || *got[0].Group != 3 {
		t.Errorf("a group grant came back with group %v", got[0].Group)
	}
}

// The regression the cascade decision rests on: a home grant names a share
// that is never a share_definition row.
func TestAHomeGrantNeedsNoShareDefinitionRow(t *testing.T) {
	ctx := context.Background()
	d, f := open(t)
	seedUser(t, d, 1, "u")

	if _, err := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: homeShare, Allow: 0xFFFF,
	}, 1); err != nil {
		t.Fatalf("a grant on the reserved home share was refused: %v", err)
	}

	var n int
	if err := f.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM share_definition`).Scan(&n); err != nil {
		t.Fatalf("counting shares: %v", err)
	}
	if n != 0 {
		t.Errorf("%d share rows exist, want none", n)
	}
}

func TestListGrantsNarrowsByEachFilter(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "one")
	seedUser(t, d, 2, "two")
	seedGroup(t, d, 9, "g")

	for _, g := range []state.GrantRow{
		{User: id64(1), Share: 10, Allow: 1},
		{User: id64(2), Share: 10, Allow: 1},
		{User: id64(1), Share: 20, Allow: 1},
		{Group: id64(9), Share: 20, Allow: 1},
	} {
		if _, err := d.PersistGrant(ctx, g, 1); err != nil {
			t.Fatalf("PersistGrant: %v", err)
		}
	}

	for name, tc := range map[string]struct {
		filter state.GrantFilter
		want   int
	}{
		"no filter":  {state.GrantFilter{}, 4},
		"by user":    {state.GrantFilter{User: 1}, 2},
		"by group":   {state.GrantFilter{Group: 9}, 1},
		"by share":   {state.GrantFilter{Share: 20}, 2},
		"user+share": {state.GrantFilter{User: 1, Share: 20}, 1},
		"no matches": {state.GrantFilter{User: 4242}, 0},
	} {
		got, err := d.ListGrants(ctx, tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s returned %d grants, want %d", name, len(got), tc.want)
		}
	}
}

// Who a grant is for and which share it covers identify the grant, so an
// update cannot move either: the statement has no columns for them.
func TestUpdateGrantMovesOnlyThePermissions(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id, err := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 10, Subpath: "a", Allow: 1, Label: "before",
	}, 1)
	if err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}
	if uerr := d.UpdateGrant(ctx, id, 0b111, 0b001, true, "after"); uerr != nil {
		t.Fatalf("UpdateGrant: %v", uerr)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	g := got[0]
	if g.Allow != 0b111 || g.Deny != 0b001 || !g.Inherit || g.Label != "after" {
		t.Errorf("the update did not land: %+v", g)
	}
	if g.User == nil || *g.User != 1 || g.Share != 10 || g.Subpath != "a" {
		t.Errorf("the update moved what identifies the grant: %+v", g)
	}
}

// An empty label clears it rather than storing an empty string, so "no
// label" is one value on disk.
func TestClearingALabelStoresNull(t *testing.T) {
	ctx := context.Background()
	d, f := open(t)
	seedUser(t, d, 1, "u")

	id, err := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 1, Allow: 1, Label: "named",
	}, 1)
	if err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}
	if uerr := d.UpdateGrant(ctx, id, 1, 0, false, ""); uerr != nil {
		t.Fatalf("UpdateGrant: %v", uerr)
	}

	var label *string
	if serr := f.SQL().QueryRowContext(ctx,
		`SELECT label FROM "grant" WHERE id = ?`, id).Scan(&label); serr != nil {
		t.Fatalf("reading the label: %v", serr)
	}
	if label != nil {
		t.Errorf("a cleared label stored %q", *label)
	}
	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if got[0].Label != "" {
		t.Errorf("a cleared label reads back as %q", got[0].Label)
	}
}

func TestUpdateAndDeleteOfAnUnknownGrantAreRefusals(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if err := d.UpdateGrant(ctx, 4242, 1, 0, false, ""); !errors.Is(err, state.ErrNoSuchGrant) {
		t.Errorf("updating an unknown grant returned %v, want ErrNoSuchGrant", err)
	}
	if err := d.DeleteGrant(ctx, 4242); !errors.Is(err, state.ErrNoSuchGrant) {
		t.Errorf("deleting an unknown grant returned %v, want ErrNoSuchGrant", err)
	}
}

func TestDeleteGrantRemovesExactlyThatRow(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	var ids []int64
	for i := range 3 {
		id, err := d.PersistGrant(ctx, state.GrantRow{
			User: id64(1), Share: 1, Subpath: strconv.Itoa(i), Allow: 1,
		}, 1)
		if err != nil {
			t.Fatalf("PersistGrant: %v", err)
		}
		ids = append(ids, id)
	}
	if err := d.DeleteGrant(ctx, ids[1]); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d grants after one delete, want 2", len(got))
	}
	for _, g := range got {
		if g.ID == ids[1] {
			t.Error("the deleted grant is still listed")
		}
	}
}

// The cascade lives inside DeleteShare rather than in a foreign key, and
// both halves commit together.
func TestDeleteShareTakesEveryGrantNamingIt(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	rowid, err := d.InsertShare(ctx, state.ShareRow{Name: "docs", Host: "/srv"}, 1)
	if err != nil {
		t.Fatalf("InsertShare: %v", err)
	}
	shareID := 1_000_000 + rowid

	for i := range 2 {
		if _, perr := d.PersistGrant(ctx, state.GrantRow{
			User: id64(1), Share: shareID, Subpath: strconv.Itoa(i), Allow: 1,
		}, 1); perr != nil {
			t.Fatalf("PersistGrant: %v", perr)
		}
	}
	// A grant on another share, which must survive.
	if _, perr := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: shareID + 1, Allow: 1,
	}, 1); perr != nil {
		t.Fatalf("PersistGrant: %v", perr)
	}

	if derr := d.DeleteShare(ctx, rowid, shareID); derr != nil {
		t.Fatalf("DeleteShare: %v", derr)
	}

	shares, err := d.ListShares(ctx)
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(shares) != 0 {
		t.Errorf("%d shares survived the delete", len(shares))
	}
	grants, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].Share != shareID+1 {
		t.Errorf("after the cascade the grants are %+v, want only the other share's", grants)
	}
}

// The cascade is one transaction, so a failure in either half leaves
// neither: there is no window where the grants are gone and the share is not.
func TestTheCascadeIsAtomic(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	rowid, err := d.InsertShare(ctx, state.ShareRow{Name: "docs", Host: "/srv"}, 1)
	if err != nil {
		t.Fatalf("InsertShare: %v", err)
	}
	shareID := 1_000_000 + rowid
	if _, perr := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: shareID, Allow: 1,
	}, 1); perr != nil {
		t.Fatalf("PersistGrant: %v", perr)
	}

	// The same two statements DeleteShare runs, with a failure injected
	// after the first: the grant delete has to roll back with it.
	sentinel := errors.New("the share delete failed")
	err = d.Write(ctx, func(tx *sql.Tx) error {
		if _, xerr := tx.ExecContext(ctx, `DELETE FROM "grant" WHERE share = ?`, shareID); xerr != nil {
			return xerr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("the injected failure returned %v", err)
	}

	grants, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(grants) != 1 {
		t.Errorf("%d grants after a rolled-back cascade, want the original 1", len(grants))
	}
	shares, err := d.ListShares(ctx)
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(shares) != 1 {
		t.Errorf("%d shares after a rolled-back cascade, want 1", len(shares))
	}
}

// Deleting the principal takes the grant with it, which is what the foreign
// keys that do exist are for.
func TestDeletingAUserCascadesToItsGrants(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	if _, err := d.PersistGrant(ctx, state.GrantRow{User: id64(1), Share: 1, Allow: 1}, 1); err != nil {
		t.Fatalf("PersistGrant: %v", err)
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx, `DELETE FROM user WHERE id = 1`)
		return xerr
	}); err != nil {
		t.Fatalf("deleting the user: %v", err)
	}

	got, err := d.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d grants outlived their principal", len(got))
	}
}

func TestMembershipsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "one")
	seedUser(t, d, 2, "two")
	seedGroup(t, d, 10, "editors")
	seedGroup(t, d, 20, "readers")

	pairs := [][2]int64{{1, 10}, {1, 20}, {2, 10}}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		for _, p := range pairs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO membership(user, "group") VALUES (?, ?)`, p[0], p[1]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding memberships: %v", err)
	}

	got, err := d.Memberships(ctx)
	if err != nil {
		t.Fatalf("Memberships: %v", err)
	}
	if len(got) != len(pairs) {
		t.Fatalf("%d memberships, want %d", len(got), len(pairs))
	}
	seen := map[[2]int64]bool{}
	for _, m := range got {
		seen[[2]int64{m.User, m.Group}] = true
	}
	for _, p := range pairs {
		if !seen[p] {
			t.Errorf("membership %v is missing", p)
		}
	}
}

// A database written before step 13 can hold duplicates, because nothing
// stopped them. The step folds them onto the first grant instead of refusing
// to open, and the fold preserves what the evaluator was already answering:
// the union of the allows and of the denies.
func TestTheMigrationFoldsDuplicateGrants(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// One version short of the fold step (step 13), by absolute position
	// rather than counted back from the current head: steps 1-12 are
	// released and never move, so this stays correct as later steps are
	// appended, which a relative count from the end does not.
	spec := state.Spec(path)
	spec.Migrations = spec.Migrations[:12]
	old, err := dbfile.Open(ctx, spec)
	if err != nil {
		t.Fatalf("opening one version short of the fold: %v", err)
	}
	if werr := old.Write(ctx, func(tx *sql.Tx) error {
		if _, xerr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (1, 'u', '', 0)`); xerr != nil {
			return xerr
		}
		// Three rows for one subject over one share and subpath: two that
		// reach only that path, one that reaches the subtree. The first two
		// fold together and the third must survive on its own.
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO "grant"(id, user, share, subpath, allow, deny, inherit, label, created_ns)
			 VALUES (10, 1, 5, 'team', 1, 0, 0, 'first', 1),
			        (11, 1, 5, 'team', 2, 8, 0, 'second', 2),
			        (12, 1, 5, 'team', 4, 0, 1, NULL, 3)`)
		return xerr
	}); werr != nil {
		t.Fatalf("planting the duplicates: %v", werr)
	}
	if cerr := old.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	f, err := dbfile.Open(ctx, state.Spec(path))
	if err != nil {
		t.Fatalf("the fold did not let the database open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})

	got, err := state.New(f).ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	rows := map[int64]state.GrantRow{}
	for _, g := range got {
		rows[g.ID] = g
	}
	if len(rows) != 2 {
		t.Fatalf("%d grants survived the fold, want 2: %+v", len(rows), got)
	}

	// The earliest id stands, carrying both allows and the deny the second row
	// contributed, and it keeps the label it already had.
	switch kept, ok := rows[10]; {
	case !ok:
		t.Fatal("the first grant did not survive the fold")
	case kept.Allow != 3:
		t.Errorf("the folded allow is %d, want 3", kept.Allow)
	case kept.Deny != 8:
		t.Errorf("the folded deny is %d, want 8", kept.Deny)
	case kept.Inherit:
		t.Error("the fold turned a grant over one path into one over a subtree")
	case kept.Label != "first":
		t.Errorf("the folded label is %q, want %q", kept.Label, "first")
	}

	// The inheriting row is a different grant, so it is not folded in. Merging
	// it would let the first row's bits reach every path underneath.
	switch sub, ok := rows[12]; {
	case !ok:
		t.Fatal("the inheriting grant was folded away")
	case sub.Allow != 4 || !sub.Inherit:
		t.Errorf("the inheriting grant reads %+v, want allow 4 and inherit", sub)
	}

	// And the index the step builds now refuses what it just cleaned up.
	if _, perr := state.New(f).PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 5, Subpath: "team", Allow: 1,
	}, 4); !errors.Is(perr, state.ErrGrantAlreadyExists) {
		t.Errorf("a duplicate after the fold returned %v, want ErrGrantAlreadyExists", perr)
	}
}

// Reach is part of a grant's identity, so widening one onto a subtree grant
// the subject already holds is the same collision a second insert is. It has
// to read as a refusal: an administrator flipping a switch on a form must not
// be told the server broke.
func TestUpdateGrantRefusesWideningOntoAnExistingSubtreeGrant(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	narrow, err := d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 5, Subpath: "team", Allow: 1,
	}, 1)
	if err != nil {
		t.Fatalf("storing the grant over one folder: %v", err)
	}
	if _, err = d.PersistGrant(ctx, state.GrantRow{
		User: id64(1), Share: 5, Subpath: "team", Allow: 2, Inherit: true,
	}, 2); err != nil {
		t.Fatalf("storing the grant over the subtree: %v", err)
	}

	if uerr := d.UpdateGrant(ctx, narrow, 1, 0, true, ""); !errors.Is(uerr, state.ErrGrantAlreadyExists) {
		t.Fatalf("widening onto the subtree grant returned %v, want ErrGrantAlreadyExists", uerr)
	}

	// The refused update leaves the grant as it was, rather than half applied.
	got, err := d.ListGrants(ctx, state.GrantFilter{User: 1})
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	for _, g := range got {
		if g.ID == narrow && g.Inherit {
			t.Error("the refused update widened the grant anyway")
		}
	}
}
