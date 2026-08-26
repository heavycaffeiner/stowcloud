// Linux only, because what it tests is.
//go:build linux

package main

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/server"
)

// Which new share needs the process rebuilt.
//
// A Landlock domain cannot be widened in a running process, so a folder under
// a directory it never granted is registered, granted, listed, and refused by
// the kernel on every open. A folder beside one already served is not: the
// domain grants each share's parent, and a path_beneath rule covers what
// appears under it afterwards.
//
// Telling those two apart is what stops every added folder costing a restart.

func TestASiblingFolderIsAlreadyInsideTheDomain(t *testing.T) {
	spec := jailSpec(&server.Config{DataDir: "/var/lib/stowcloud"}, []string{"/shares/files"})

	for _, in := range []string{
		"/shares/files", // the share itself
		"/shares/extra", // the sibling an administrator adds next
		"/shares/a/b/c", // and anything under the granted parent
		// The granted directory itself: what the domain holds for a share is
		// its parent, so that directory is inside the domain too.
		"/shares",
		"/var/lib/stowcloud/spool",
	} {
		if !inJail(spec, in) {
			t.Errorf("%s reads as outside the domain, so adding it would cost a restart", in)
		}
	}

	for _, out := range []string{
		"/srv/media",  // a parent the domain has never seen
		"/",           // never granted, and the one thing that must never be
		"/shares-two", // a name the granted one is a string prefix of
	} {
		if inJail(spec, out) {
			t.Errorf("%s reads as inside the domain, so adding it would answer permission denied", out)
		}
	}
}
