package state_test

import (
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
)

// A snapshot answers every target it was asked about, including one with no
// locks over it: a missing entry and an empty one are the same answer, and a
// caller that has to tell them apart will get it wrong.
func TestASnapshotAnswersEveryTarget(t *testing.T) {
	t.Parallel()
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "a")

	if err := d.AdmitDavLock(ctx,
		lockAt("held", "/a", state.LockExclusive, state.LockDepthZero, 1), 0); err != nil {
		t.Fatal(err)
	}

	targets := []state.LockTarget{
		{Share: 1, Path: "/a"},
		{Share: 1, Path: "/b"},
		{Share: 2, Path: "/a"},
	}

	snap, err := d.SnapshotDavLocks(ctx, targets, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(snap.Targets) != len(targets) {
		t.Errorf("the snapshot holds %d targets, want %d", len(snap.Targets), len(targets))
	}
	if got := snap.Covering(targets[0]); len(got) != 1 || got[0].Token != "held" {
		t.Errorf("the locked target got %+v, want the held lock", got)
	}
	if got := snap.Covering(targets[1]); len(got) != 0 {
		t.Errorf("an unlocked path got %+v", got)
	}
	if got := snap.Covering(targets[2]); len(got) != 0 {
		t.Errorf("the same path in another share got %+v", got)
	}
}

// Coverage in a snapshot follows the same rule as admission: the named path,
// and anything under a depth-infinity ancestor with a real path boundary.
func TestWhatASnapshotCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		lockPath  string
		lockDepth int64
		target    string
		covered   bool
	}{
		{"the named path", "/a", state.LockDepthZero, "/a", true},
		{"below a depth-infinity lock", "/a", state.LockDepthInfinity, "/a/b/c", true},
		{"below a depth-zero lock", "/a", state.LockDepthZero, "/a/b", false},
		{"an unrelated path", "/a", state.LockDepthInfinity, "/b", false},
		{"a prefix that is not a boundary", "/a", state.LockDepthInfinity, "/ab", false},
		{"the root", "/", state.LockDepthInfinity, "/deep/path", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := open(t)
			ctx := context.Background()
			seedUser(t, d, 1, "a")

			if err := d.AdmitDavLock(ctx,
				lockAt("held", c.lockPath, state.LockExclusive, c.lockDepth, 1), 0); err != nil {
				t.Fatal(err)
			}

			target := state.LockTarget{Share: 1, Path: c.target}
			snap, err := d.SnapshotDavLocks(ctx, []state.LockTarget{target}, 0)
			if err != nil {
				t.Fatal(err)
			}

			got := len(snap.Covering(target)) > 0
			if got != c.covered {
				t.Errorf("%s: covered %v, want %v", c.name, got, c.covered)
			}
		})
	}
}

// An expired lock covers nothing, so a request is not refused for a lock that
// has already run out.
func TestASnapshotIgnoresAnExpiredLock(t *testing.T) {
	t.Parallel()
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "a")

	old := lockAt("old", "/a", state.LockExclusive, state.LockDepthZero, 1)
	old.ExpiresNs = 1000
	if err := d.AdmitDavLock(ctx, old, 0); err != nil {
		t.Fatal(err)
	}

	target := state.LockTarget{Share: 1, Path: "/a"}
	snap, err := d.SnapshotDavLocks(ctx, []state.LockTarget{target}, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Covering(target); len(got) != 0 {
		t.Errorf("an expired lock covered a target: %+v", got)
	}
}

// An empty target list is answered without touching the database, since there
// is nothing to be consistent about.
func TestAnEmptySnapshotIsEmpty(t *testing.T) {
	t.Parallel()
	d, _ := open(t)

	snap, err := d.SnapshotDavLocks(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Targets) != 0 {
		t.Errorf("an empty request produced %d targets", len(snap.Targets))
	}
	if got := snap.Covering(state.LockTarget{Share: 1, Path: "/a"}); got != nil {
		t.Errorf("an empty snapshot reported coverage: %+v", got)
	}
}

// Many targets in one share read that share's locks once rather than per
// target, so a large COPY does not scan the table once per endpoint.
func TestOneSharesLocksAreReadOnce(t *testing.T) {
	t.Parallel()
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "a")

	if err := d.AdmitDavLock(ctx,
		lockAt("held", "/", state.LockExclusive, state.LockDepthInfinity, 1), 0); err != nil {
		t.Fatal(err)
	}

	targets := make([]state.LockTarget, 0, 50)
	for i := 0; i < 50; i++ {
		targets = append(targets, state.LockTarget{Share: 1, Path: "/p" + string(rune('a'+i%26))})
	}

	snap, err := d.SnapshotDavLocks(ctx, targets, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if len(snap.Covering(target)) != 1 {
			t.Fatalf("%s was not covered by the root lock", target.Path)
		}
	}
}
