//go:build linux

package core

import (
	"testing"
)

// TestRootsPutTheStoredRootOrderFirst proves a stored order puts its labels
// first, in the order stored, ahead of every root the account was not asked
// to reorder.
func TestRootsPutTheStoredRootOrderFirst(t *testing.T) {
	t.Parallel()
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")

	grantRead(t, c, st, 1, 10, "Files")
	grantRead(t, c, st, 1, 11, "Media")
	grantRead(t, c, st, 1, 12, "Backups")

	if err := st.SetRootOrder(t.Context(), 1, []string{"Backups", "Files"}); err != nil {
		t.Fatalf("SetRootOrder: %v", err)
	}

	roots := c.Roots(1)
	if len(roots) != 3 {
		t.Fatalf("Roots returned %d entries, want 3", len(roots))
	}
	var labels []string
	for _, r := range roots {
		labels = append(labels, r.Label)
	}
	want := []string{"Backups", "Files", "Media"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("order is %v, want %v", labels, want)
		}
	}
}

// TestARootOrderNamingAnUnknownLabelInventsNothing proves a stored label
// naming no current root is skipped rather than invented as an entry.
func TestARootOrderNamingAnUnknownLabelInventsNothing(t *testing.T) {
	t.Parallel()
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")

	grantRead(t, c, st, 1, 10, "Files")

	if err := st.SetRootOrder(t.Context(), 1, []string{"Ghost", "Files"}); err != nil {
		t.Fatalf("SetRootOrder: %v", err)
	}

	roots := c.Roots(1)
	if len(roots) != 1 || roots[0].Label != "Files" {
		t.Fatalf("Roots returned %+v, want exactly the one real root", roots)
	}
}

// TestNoStoredRootOrderKeepsTheDefaultOrder proves an account with no
// stored order sees exactly what it saw before this feature existed: the
// evaluator's own discovery order, untouched.
func TestNoStoredRootOrderKeepsTheDefaultOrder(t *testing.T) {
	t.Parallel()
	c, st := newCore(t)
	seedUser(t, st, 1, "ada")

	grantRead(t, c, st, 1, 10, "Files")
	grantRead(t, c, st, 1, 11, "Media")

	withOrder := c.Roots(1)
	var labels []string
	for _, r := range withOrder {
		labels = append(labels, r.Label)
	}
	if len(labels) != 2 || labels[0] != "Files" || labels[1] != "Media" {
		t.Fatalf("default order is %v, want [Files Media]", labels)
	}
}
