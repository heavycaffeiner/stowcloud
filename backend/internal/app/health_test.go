// Linux only, matching the package under test.
//go:build linux

package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tokens come back sorted and deduplicated, so one deployment state is one
// response rather than several spellings of it.
func TestHealthReasonsAreSortedAndDeduplicated(t *testing.T) {
	got := HealthOf(HealthDegraded, []HealthReason{
		ReasonSMBAgent, ReasonIndexStale, ReasonSMBAgent, ReasonCacheDatabase, ReasonIndexStale,
	})

	want := []HealthReason{ReasonCacheDatabase, ReasonIndexStale, ReasonSMBAgent}
	if len(got.Reasons) != len(want) {
		t.Fatalf("got %v, want %v", got.Reasons, want)
	}
	for i := range want {
		if got.Reasons[i] != want[i] {
			t.Fatalf("got %v, want %v", got.Reasons, want)
		}
	}

	// Same state, different arrival order, same bytes.
	other := HealthOf(HealthDegraded, []HealthReason{
		ReasonIndexStale, ReasonCacheDatabase, ReasonSMBAgent,
	})
	a, aerr := json.Marshal(got)
	b, berr := json.Marshal(other)
	if aerr != nil || berr != nil {
		t.Fatalf("encoding: %v %v", aerr, berr)
	}
	if string(a) != string(b) {
		t.Errorf("the same state produced two responses:\n  %s\n  %s", a, b)
	}
}

// Anything outside the vocabulary is dropped rather than passed through. An
// unknown value is the shape an accidental interpolation takes, and this
// response has no credential behind it.
func TestAnUnknownReasonIsDropped(t *testing.T) {
	got := HealthOf(HealthFailing, []HealthReason{
		ReasonStateDatabase,
		HealthReason("opening /srv/stowcloud/data/state.db: permission denied"),
		HealthReason("share_unservable: /mnt/photos"),
		HealthReason("alice@example.test"),
		HealthReason(""),
	})

	if len(got.Reasons) != 1 || got.Reasons[0] != ReasonStateDatabase {
		t.Fatalf("the projection carried %v", got.Reasons)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	// Nothing that could only have come from this installation.
	for _, leak := range []string{"/srv", "/mnt", "permission denied", "alice", "@"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the response carries %q: %s", leak, raw)
		}
	}
}

// An unrecognised status is failing rather than ok: whatever produced it is
// not something to describe here, and ok would be the wrong guess.
func TestAnUnknownStatusBecomesFailing(t *testing.T) {
	for _, s := range []HealthStatus{"", "unknown", "OK", "healthy"} {
		if got := HealthOf(s, nil); got.Status != HealthFailing {
			t.Errorf("the status %q became %q", s, got.Status)
		}
	}
	for _, s := range []HealthStatus{HealthOK, HealthDegraded, HealthFailing} {
		if got := HealthOf(s, nil); got.Status != s {
			t.Errorf("the status %q became %q", s, got.Status)
		}
	}
}
