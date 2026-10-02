package state_test

import (
	"context"
	"testing"
)

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
