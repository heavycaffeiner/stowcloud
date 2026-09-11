package state_test

import (
	"context"
	"testing"
)

// TestRootOrderRoundTrips proves the stored order comes back exactly as
// written, in position order rather than insertion or alphabetical order.
func TestRootOrderRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	want := []string{"Media", "Files", "Backups"}
	if err := d.SetRootOrder(ctx, 1, want); err != nil {
		t.Fatalf("SetRootOrder: %v", err)
	}

	got, err := d.RootOrder(ctx, 1)
	if err != nil {
		t.Fatalf("RootOrder: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSetRootOrderReplacesRatherThanAppends proves a second write drops
// whatever the first one stored instead of accumulating rows behind it.
func TestSetRootOrderReplacesRatherThanAppends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	if err := d.SetRootOrder(ctx, 1, []string{"Media", "Files", "Backups"}); err != nil {
		t.Fatalf("first SetRootOrder: %v", err)
	}
	if err := d.SetRootOrder(ctx, 1, []string{"Files"}); err != nil {
		t.Fatalf("second SetRootOrder: %v", err)
	}

	got, err := d.RootOrder(ctx, 1)
	if err != nil {
		t.Fatalf("RootOrder: %v", err)
	}
	if len(got) != 1 || got[0] != "Files" {
		t.Errorf("got %v, want [Files]", got)
	}
}

// TestRootOrderOfAnAccountThatNeverSetOneIsEmpty proves an unset order reads
// back as nothing stored, not as an error and not as some invented default.
func TestRootOrderOfAnAccountThatNeverSetOneIsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	got, err := d.RootOrder(ctx, 1)
	if err != nil {
		t.Fatalf("RootOrder: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// TestSetRootOrderIsScopedByAccount proves one account's write never touches
// another's stored order, since the primary key is (user, label).
func TestSetRootOrderIsScopedByAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "a")
	seedUser(t, d, 2, "b")

	if err := d.SetRootOrder(ctx, 1, []string{"Media", "Files"}); err != nil {
		t.Fatalf("SetRootOrder(1): %v", err)
	}
	if err := d.SetRootOrder(ctx, 2, []string{"Files", "Media"}); err != nil {
		t.Fatalf("SetRootOrder(2): %v", err)
	}

	got1, err := d.RootOrder(ctx, 1)
	if err != nil {
		t.Fatalf("RootOrder(1): %v", err)
	}
	if len(got1) != 2 || got1[0] != "Media" || got1[1] != "Files" {
		t.Errorf("account 1 got %v", got1)
	}

	got2, err := d.RootOrder(ctx, 2)
	if err != nil {
		t.Fatalf("RootOrder(2): %v", err)
	}
	if len(got2) != 2 || got2[0] != "Files" || got2[1] != "Media" {
		t.Errorf("account 2 got %v", got2)
	}
}
