package state_test

import (
	"context"
	"testing"
)

// The search section holds two values that are written by different callers at
// different times: the administrator's switch and the rate the last build
// measured. Both live under one key, so writing either has to keep the other.

func TestTheIndexSwitchAndTheBuildRateDoNotOverwriteEachOther(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	if err := d.SetIndexBuildRate(ctx, 12_345); err != nil {
		t.Fatalf("SetIndexBuildRate: %v", err)
	}
	if err := d.SetIndexNameEnabled(ctx, true); err != nil {
		t.Fatalf("SetIndexNameEnabled: %v", err)
	}

	rate, err := d.IndexBuildRate(ctx)
	if err != nil {
		t.Fatalf("IndexBuildRate: %v", err)
	}
	if rate != 12_345 {
		t.Fatalf("the rate is %d after the switch was written, want 12345", rate)
	}

	on, err := d.IndexNameEnabled(ctx)
	if err != nil {
		t.Fatalf("IndexNameEnabled: %v", err)
	}
	if !on {
		t.Fatal("the switch was lost")
	}

	// And the other order, because the bug is symmetrical.
	if serr := d.SetIndexBuildRate(ctx, 999); serr != nil {
		t.Fatalf("SetIndexBuildRate: %v", serr)
	}
	on, err = d.IndexNameEnabled(ctx)
	if err != nil {
		t.Fatalf("IndexNameEnabled: %v", err)
	}
	if !on {
		t.Fatal("writing the rate cleared the switch")
	}
}

// No build has run, so there is no rate. Zero rather than a default, because
// the caller's fallback is a compiled-in guess and the screen says which of
// the two an operator was shown.
func TestAnUnmeasuredBuildRateIsZero(t *testing.T) {
	d := open(t)
	rate, err := d.IndexBuildRate(context.Background())
	if err != nil {
		t.Fatalf("IndexBuildRate: %v", err)
	}
	if rate != 0 {
		t.Fatalf("an unmeasured rate reported %d, want 0", rate)
	}
}
