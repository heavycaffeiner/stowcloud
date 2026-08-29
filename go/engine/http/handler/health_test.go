// Linux only, matching the package under test.
//go:build linux

package handler

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

// The token check is exact rather than shaped, because a value that merely
// looks like a token is what an interpolated error string would be.
func TestTheTokenCheckIsExact(t *testing.T) {
	for _, good := range []string{"state_database", "smb_agent", " index_stale "} {
		if !SafeHealthToken(good) {
			t.Errorf("%q was rejected", good)
		}
	}
	for _, bad := range []string{
		"",
		"state_database: permission denied",
		"unknown_subsystem",
		"STATE_DATABASE",
		"state database",
	} {
		if SafeHealthToken(bad) {
			t.Errorf("%q was accepted", bad)
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

// A healthy deployment encodes an empty list rather than null, so a client
// iterating the field does not have to test for it.
func TestAHealthyResponseCarriesAnEmptyList(t *testing.T) {
	raw, err := json.Marshal(HealthOf(HealthOK, nil))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != `{"status":"ok","reasons":[]}` {
		t.Errorf("a healthy deployment encoded as %s", raw)
	}
}
