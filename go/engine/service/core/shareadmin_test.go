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

func TestTheIdSchemeRoundTripsAndReservesTheHomeId(t *testing.T) {
	for _, rowid := range []int64{1, 2, 4096, 1_000_000} {
		id, err := shareIDOf(rowid)
		if err != nil {
			t.Fatalf("shareIDOf(%d): %v", rowid, err)
		}
		if got := rowIDOf(id); got != rowid {
			t.Fatalf("the id scheme did not round-trip %d, got %d", rowid, got)
		}
		// The homes share sits below the base, and rowids are positive, so
		// no stored share can ever mint it.
		if id == homeShareID {
			t.Fatalf("rowid %d minted the reserved home share id", rowid)
		}
		if id <= dynamicShareIDBase {
			t.Fatalf("rowid %d minted %d, which is not above the base", rowid, id)
		}
	}

	// An overflowing rowid is corruption and is refused, never truncated
	// into a collision with a real share.
	if _, err := shareIDOf(1 << 40); err == nil {
		t.Fatal("an overflowing rowid was accepted")
	}
}

func TestCreateShareMintsDurablyAndRefusesADuplicateName(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	host := t.TempDir()

	got, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: host})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if got.ID <= dynamicShareIDBase {
		t.Fatalf("the minted id is %d, want one above the base", got.ID)
	}
	if _, ok := c.ShareRoot(got.ID); !ok {
		t.Fatal("the created share has no live root")
	}

	rows, err := st.ListShares(ctx)
	if err != nil {
		t.Fatalf("listing shares: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "documents" {
		t.Fatalf("the durable rows are %+v, want the one share", rows)
	}

	if _, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: t.TempDir()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a duplicate name returned %v, want ErrConflict", err)
	}
}

// UpdateShare refuses a patch that names a different backend than the one
// the share was created with. Repointing a share at a different backend
// would leave every grant, share link and cached identity naming data that
// is no longer there.
func TestUpdateShareRefusesABackendChange(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()

	got, err := c.CreateShare(ctx, ShareSpec{Name: "docs", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if got.Backend != BackendLocal {
		t.Fatalf("a share created with no backend is %q, want %q", got.Backend, BackendLocal)
	}

	other := BackendS3
	if _, err := c.UpdateShare(ctx, got.ID, SharePatch{Backend: &other}); !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("changing backend from %q to %q returned %v, want ErrUnprocessable",
			BackendLocal, other, err)
	}
	// The refused patch changed nothing: the share is still local and still
	// registered under its original root.
	if def, ok := c.Share(got.ID); !ok || def.Backend != BackendLocal {
		t.Fatalf("the share after a refused backend change is %+v", def)
	}

	// A patch naming the backend the share already has is not a change,
	// and passes through.
	same := BackendLocal
	if _, err := c.UpdateShare(ctx, got.ID, SharePatch{Backend: &same}); err != nil {
		t.Fatalf("a patch naming the current backend was refused: %v", err)
	}
}

func TestAShareThatWillNotRegisterLeavesNoDanglingRow(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()

	// A host that does not exist fails the admission gate after the row has
	// already committed.
	_, err := c.CreateShare(ctx, ShareSpec{
		Name: "absent", Host: filepath.Join(t.TempDir(), "nothing-here"),
	})
	if err == nil {
		t.Fatal("a share over a missing host registered")
	}

	rows, lerr := st.ListShares(ctx)
	if lerr != nil {
		t.Fatalf("listing shares: %v", lerr)
	}
	// The row is rolled back rather than left behind, or a later reload
	// would resurrect a share the caller was told did not exist.
	if len(rows) != 0 {
		t.Fatalf("a failed creation left %d rows behind", len(rows))
	}

	// The reload is the actual consequence the rollback exists to prevent,
	// so it is asserted rather than inferred from the row count.
	rejected, rerr := c.ReloadPersistedShares(ctx)
	if rerr != nil {
		t.Fatalf("ReloadPersistedShares: %v", rerr)
	}
	if len(rejected) != 0 || len(c.Shares()) != 0 {
		t.Fatalf("a reload resurrected the rolled-back share: rejected=%+v shares=%+v",
			rejected, c.Shares())
	}
}

// CreateShare tells a genuinely missing path apart from one the sandbox
// refuses, reporting the distinct token for each. A real Landlock domain
// is not driven here: a directory whose own mode denies reading reproduces
// the same OpenShareRoot-succeeds/proveReadable-fails asymmetry
// RegisterShareRoot classifies, and RejectionKind reports it as "ungranted"
// rather than folding it into the generic "unreadable". A truly absent
// path still reports "missing", proving the new case did not swallow it.
func TestCreateShareNamesASandboxRefusalApartFromAMissingPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the mode bit this test depends on")
	}
	c, _ := newCore(t)
	ctx := context.Background()

	parent := t.TempDir()
	denied := filepath.Join(parent, "denied")
	if err := os.Mkdir(denied, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() {
		if cerr := os.Chmod(denied, 0o755); cerr != nil {
			t.Errorf("restoring the directory mode: %v", cerr)
		}
	})

	_, err := c.CreateShare(ctx, ShareSpec{Name: "denied", Host: denied})
	var broken *ShareBrokenError
	if !errors.As(err, &broken) {
		t.Fatalf("CreateShare over a denied directory returned %v, want a ShareBrokenError", err)
	}
	if broken.Reason != "ungranted" {
		t.Fatalf("the reason is %q, want ungranted", broken.Reason)
	}

	_, err = c.CreateShare(ctx, ShareSpec{
		Name: "missing", Host: filepath.Join(parent, "nothing-here"),
	})
	if !errors.As(err, &broken) {
		t.Fatalf("CreateShare over a missing directory returned %v, want a ShareBrokenError", err)
	}
	if broken.Reason != "missing" {
		t.Fatalf("the reason is %q, want missing", broken.Reason)
	}
}

func TestUpdateShareAppliesOnlyThePatchedFields(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	created, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	trash := true
	got, err := c.UpdateShare(ctx, created.ID, SharePatch{TrashEnabled: &trash})
	if err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	if !got.TrashEnabled {
		t.Fatal("the trash toggle did not apply")
	}
	// A nil field leaves its value alone, which is what lets a screen edit
	// one thing without resetting the rest.
	if got.Name != "documents" {
		t.Fatalf("the name became %q", got.Name)
	}

	rows, err := st.ListShares(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("listing shares: %v (%d rows)", err, len(rows))
	}
	if !rows[0].TrashEnabled {
		t.Fatal("the toggle did not reach the durable row")
	}

	if _, err := c.UpdateShare(ctx, 424242, SharePatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updating an unknown id returned %v, want ErrNotFound", err)
	}
}

func TestUpdateShareKeepsTheRowWhenTheNewPathWillNotOpen(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	created, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "gone")
	if _, err := c.UpdateShare(ctx, created.ID, SharePatch{Host: &missing}); err == nil {
		t.Fatal("repointing at a missing path succeeded")
	}

	// The row stays written: dropping it would hide the edit that caused the
	// failure, and refusing the write would make a repointed path unfixable
	// when the old path is also gone.
	rows, lerr := st.ListShares(ctx)
	if lerr != nil || len(rows) != 1 {
		t.Fatalf("listing shares: %v (%d rows)", lerr, len(rows))
	}
	if rows[0].Host != missing {
		t.Fatalf("the durable host is %q, want the edited one", rows[0].Host)
	}
	// And the share is visibly broken rather than silently gone.
	if c.ShareBroken(created.ID) == nil {
		t.Fatal("the share is not marked broken after a failed repoint")
	}
}

func TestRetryShareHealsAFixedPathAndRefusesAStillBrokenOne(t *testing.T) {
	c, _ := newCore(t)
	ctx := context.Background()
	host := filepath.Join(t.TempDir(), "later")

	def := ShareDef{ID: 1_000_001, Name: "documents", Host: host, Policy: vfs.DefaultSharePolicy()}
	c.RegisterBroken(def, errors.New("the disk was not there"))

	if _, err := c.RetryShare(ctx, def.ID); err == nil {
		t.Fatal("retrying a still-missing path succeeded")
	}
	if c.ShareBroken(def.ID) == nil {
		t.Fatal("a failed retry left the share unmarked")
	}

	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatalf("restoring the path: %v", err)
	}
	got, err := c.RetryShare(ctx, def.ID)
	if err != nil {
		t.Fatalf("retrying a fixed path: %v", err)
	}
	if got.BrokenReason != "" {
		t.Fatalf("the healed share still reports %q", got.BrokenReason)
	}
	if c.ShareBroken(def.ID) != nil {
		t.Fatal("the healed share is still marked broken")
	}

	if _, err := c.RetryShare(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retrying an unknown id returned %v, want ErrNotFound", err)
	}
}

func TestDeleteShareRemovesTheRowTheEntryAndItsGrants(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	seedUser(t, st, 1, "ada")
	created, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	grantAt(t, c, st, 1, created.ID, "", "Documents", acl.Read)
	if len(c.Roots(1)) != 1 {
		t.Fatal("the grant did not take effect")
	}

	if err := c.DeleteShare(ctx, created.ID); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	if _, ok := c.Share(created.ID); ok {
		t.Fatal("the deleted share is still registered")
	}
	rows, lerr := st.ListShares(ctx)
	if lerr != nil || len(rows) != 0 {
		t.Fatalf("the durable row survived: %v (%d rows)", lerr, len(rows))
	}
	// The cascade removed the grant, and the reload is what stops the
	// evaluator serving it in this process rather than at the next restart.
	if got := len(c.Roots(1)); got != 0 {
		t.Fatalf("the user still projects %d roots after the share was deleted", got)
	}

	if err := c.DeleteShare(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting an unknown id returned %v, want ErrNotFound", err)
	}
}

func TestDeleteShareWorksOnABrokenShare(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	created, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	// A broken entry has no root, and removing one is exactly the case that
	// used to answer 500 and leave the share permanently stuck.
	c.RegisterBroken(created, errors.New("the disk went away"))

	if err := c.DeleteShare(ctx, created.ID); err != nil {
		t.Fatalf("deleting a broken share: %v", err)
	}
	rows, lerr := st.ListShares(ctx)
	if lerr != nil || len(rows) != 0 {
		t.Fatalf("the row survived: %v (%d rows)", lerr, len(rows))
	}
}

func TestReloadPersistedSharesLandsOnTheSameIds(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	host := t.TempDir()
	created, err := c.CreateShare(ctx, ShareSpec{Name: "documents", Host: host})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// A fresh core over the same database is the restart. The cache, the
	// grants and the links all reference the id, so it has to come back the
	// same.
	restarted, err := New(ctx, Options{State: st, Cache: c.cache, ACL: acl.NewEvaluator()})
	if err != nil {
		t.Fatalf("restarting: %v", err)
	}
	rejected, err := restarted.ReloadPersistedShares(ctx)
	if err != nil {
		t.Fatalf("ReloadPersistedShares: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("a healthy share was rejected: %+v", rejected)
	}
	if _, ok := restarted.Share(created.ID); !ok {
		t.Fatalf("the share did not come back under id %d", created.ID)
	}
}

func TestReloadReportsABrokenShareAndKeepsTheRestServing(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	good, err := c.CreateShare(ctx, ShareSpec{Name: "good", Host: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	gone := t.TempDir()
	bad, err := c.CreateShare(ctx, ShareSpec{Name: "bad", Host: gone})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if rerr := os.RemoveAll(gone); rerr != nil {
		t.Fatalf("removing the host: %v", rerr)
	}

	restarted, err := New(ctx, Options{State: st, Cache: c.cache, ACL: acl.NewEvaluator()})
	if err != nil {
		t.Fatalf("restarting: %v", err)
	}
	rejected, err := restarted.ReloadPersistedShares(ctx)
	if err != nil {
		t.Fatalf("ReloadPersistedShares: %v", err)
	}
	if len(rejected) != 1 || rejected[0].Name != "bad" {
		t.Fatalf("the rejections are %+v, want the one bad share", rejected)
	}
	if rejected[0].Kind == "" {
		t.Fatal("the rejection carries no health token")
	}
	// One share this server cannot serve is not an outage of every other.
	if _, ok := restarted.ShareRoot(good.ID); !ok {
		t.Fatal("the healthy share did not come up beside the broken one")
	}
	// The broken one stays listed, carrying why, rather than vanishing.
	if def, ok := restarted.Share(bad.ID); !ok || def.BrokenReason == "" {
		t.Fatalf("the broken share is %+v, want it listed with a reason", def)
	}
}

func TestAnUnreadableSymlinkPolicyFallsToTheStrictest(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	host := t.TempDir()
	if _, err := st.InsertShare(ctx, state.ShareRow{
		Name: "documents", Host: host, SymlinkPolicy: "a-word-this-build-does-not-know",
	}, 0); err != nil {
		t.Fatalf("seeding the row: %v", err)
	}

	rejected, err := c.ReloadPersistedShares(ctx)
	if err != nil {
		t.Fatalf("an unreadable policy refused the start: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("the share was rejected: %+v", rejected)
	}
	def, ok := c.Share(1_000_001)
	if !ok {
		t.Fatal("the share did not register")
	}
	// The strictest setting: never a refused startup, and never a share
	// following links because the instruction not to was unreadable.
	if def.Policy.Symlink != vfs.SymlinkDeny {
		t.Fatalf("the policy fell back to %v, want deny", def.Policy.Symlink)
	}
	// And it still serves: falling back is not the same as refusing the
	// share, which would take a folder offline over one unreadable word.
	if _, ok := c.ShareRoot(1_000_001); !ok {
		t.Fatal("the share registered but has no live root")
	}
}

func TestScanSourcesCoverEveryShareAndNarrowPerEntry(t *testing.T) {
	c, st, host, _ := writable(t)
	ctx := context.Background()
	_ = ctx
	if err := os.MkdirAll(filepath.Join(host, "secret"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}

	// Administrator-scoped: every registered share, whoever is asking.
	all := c.ScanSources()
	if len(all) != 1 || all[0].Share != 10 {
		t.Fatalf("ScanSources returned %+v, want the one share", all)
	}
	if all[0].Allow != nil {
		t.Fatal("the administrator-scoped form carries a filter")
	}

	denyReadAt(t, c, st, 1, 10, "secret", allPerms)
	scoped := c.UserScanSources(1)
	if len(scoped) != 1 || scoped[0].Allow == nil {
		t.Fatalf("UserScanSources returned %+v, want one filtered source", scoped)
	}
	// Per entry, not per share: a grant can start partway down a tree, so a
	// share-level answer would hide a readable subtree or count an
	// unreadable one.
	if !scoped[0].Allow(safe(t, "note.txt"), false) {
		t.Fatal("a readable path was filtered out")
	}
	if scoped[0].Allow(safe(t, "secret"), true) {
		t.Fatal("a denied subtree passed the filter")
	}
}

func TestShareLabelAnswersFromTheGrantProjection(t *testing.T) {
	c, st, _, _ := writable(t)
	seedUser(t, st, 2, "bob")

	if got := c.ShareLabel(1, 10); got != "Documents" {
		t.Fatalf("ShareLabel returned %q, want the projected label", got)
	}
	// An account that cannot see the share gets an empty answer, which is
	// what a renderer reads as "the grant went away".
	if got := c.ShareLabel(2, 10); got != "" {
		t.Fatalf("an ungranted account got the label %q", got)
	}
}
