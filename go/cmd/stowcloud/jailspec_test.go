//go:build linux

package main

import "testing"

// A share's parent is what the domain grants, so a folder added from the admin
// screen later is already inside it. Granting the share itself is what made
// every run-time share unreachable until a restart.
func TestAShareGrantsItsParent(t *testing.T) {
	if got := shareGrantPath("/shares/files"); got != "/shares" {
		t.Fatalf("shareGrantPath(/shares/files) = %q, want /shares", got)
	}
	if got := shareGrantPath("/srv/media/photos"); got != "/srv/media" {
		t.Fatalf("shareGrantPath(/srv/media/photos) = %q, want /srv/media", got)
	}
}

// Granting "/" would put the whole filesystem in the domain, which is the one
// thing the sandbox exists to prevent. A share directly under the root keeps
// its own narrow rule instead.
func TestAShareUnderTheRootDoesNotGrantTheRoot(t *testing.T) {
	for _, in := range []string{"/data", "/data/", "//data"} {
		if got := shareGrantPath(in); got != "/data" {
			t.Errorf("shareGrantPath(%q) = %q, want /data", in, got)
		}
	}
	if got := shareGrantPath("/"); got != "/" {
		t.Errorf("shareGrantPath(/) = %q, want /", got)
	}
}

// A trailing slash and a doubled separator name the same directory, so they
// must not produce two rules for one path.
func TestTheGrantPathIsCleaned(t *testing.T) {
	if got := shareGrantPath("/shares/files/"); got != "/shares" {
		t.Fatalf("a trailing slash gave %q, want /shares", got)
	}
	if got := shareGrantPath("/shares//files"); got != "/shares" {
		t.Fatalf("a doubled separator gave %q, want /shares", got)
	}
}
