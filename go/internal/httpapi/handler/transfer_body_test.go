//go:build linux

package handler

import (
	"encoding/json"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
)

// The conflict policy arrives capitalised from the interface, and this surface
// compared it against a lowercase literal inline. Every answer other than the
// default read as "fail", so choosing overwrite in the conflict dialogue asked
// again for the same conflict forever and "keep both" never renamed anything.
func TestTheConflictPolicyTheInterfaceSendsIsUnderstood(t *testing.T) {
	cases := map[string]core.OnConflict{
		"Fail":      core.ConflictFail,
		"Rename":    core.ConflictRename,
		"Overwrite": core.ConflictOverwrite,
		"Skip":      core.ConflictSkip,
		"overwrite": core.ConflictOverwrite,
		"":          core.ConflictFail,
	}
	for sent, want := range cases {
		got, ok := core.ParseOnConflict(sent)
		if !ok {
			t.Errorf("%q was refused, and it is a value the interface sends", sent)
			continue
		}
		if got != want {
			t.Errorf("%q parsed as %v, want %v", sent, got, want)
		}
	}
}

// A policy this build does not have is reported rather than folded into one of
// the four. A client asking for something else must not silently get "fail".
func TestAnUnknownConflictPolicyIsRefused(t *testing.T) {
	if _, ok := core.ParseOnConflict("merge"); ok {
		t.Fatal("an unknown policy was accepted, so a client gets a policy it did not ask for")
	}
}

// The body the browse screen sends for a move or a copy, decoded against the
// struct this surface actually reads.
func TestATransferBodyFromTheBrowseScreenDecodes(t *testing.T) {
	const body = `{"paths":["/docs/a.txt"],"dest":"/photos","on_conflict":"rename"}`

	var req transferRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decoding the browse screen's body: %v", err)
	}
	if len(req.Paths) != 1 || req.Paths[0] != "/docs/a.txt" {
		t.Errorf("paths decoded as %v", req.Paths)
	}
	if req.Dest != "/photos" {
		t.Errorf("dest decoded as %q", req.Dest)
	}
	if req.OnConflict != "rename" {
		t.Fatalf("on_conflict decoded as %q", req.OnConflict)
	}
}
