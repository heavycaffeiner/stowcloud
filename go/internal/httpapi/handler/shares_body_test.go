//go:build linux

package handler

import (
	"encoding/json"
	"testing"
)

// The admin screen sends host_path, which is what the response carries and
// what the config file calls the same field. This struct read "host" instead,
// so every create from the screen decoded to an empty path and came back as a
// malformed request. The body below is the one the screen actually sends.
func TestACreateBodyFromTheAdminScreenDecodes(t *testing.T) {
	const body = `{"name":"photos","host_path":"/srv/photos"}`

	var req shareRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decoding the admin screen's body: %v", err)
	}
	if req.Name != "photos" {
		t.Errorf("name decoded as %q", req.Name)
	}
	if req.HostPath != "/srv/photos" {
		t.Fatalf("host path decoded as %q, so the create is refused as malformed", req.HostPath)
	}
}

// host_path is the only spelling. "host" was what this struct read while every
// other surface said host_path, and accepting both would keep that split alive
// in the one place it caused the bug.
func TestTheOlderHostSpellingIsNotAccepted(t *testing.T) {
	var req shareRequest
	if err := json.Unmarshal([]byte(`{"name":"docs","host":"/srv/docs"}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.HostPath != "" {
		t.Fatalf("host path decoded as %q from the old spelling, want empty", req.HostPath)
	}
}

// A body with no path is refused; the fix must not turn a malformed request
// into a share pointing nowhere.
func TestABodyWithNoPathIsEmpty(t *testing.T) {
	var req shareRequest
	if err := json.Unmarshal([]byte(`{"name":"n"}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.HostPath != "" {
		t.Fatalf("host path resolved to %q, want empty", req.HostPath)
	}
}

// The patch body carries the same field, so repointing a share from the screen
// works for the same reason creating one does.
func TestAPatchBodyCarriesHostPath(t *testing.T) {
	var req shareRequest
	if err := json.Unmarshal([]byte(`{"host_path":"/srv/moved","trash_enabled":true}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.HostPath != "/srv/moved" {
		t.Fatalf("host path decoded as %q", req.HostPath)
	}
	if req.TrashEnabled == nil || !*req.TrashEnabled {
		t.Fatal("trash_enabled did not decode")
	}
}
