//go:build !linux

package fsatomic

import (
	"os"
	"testing"
)

// assertMode does not check permission bits off Linux.
//
// The server is Linux-only; the other platforms build so a developer can run
// the rest of the suite. They have no POSIX permission bits to compare
// against, and asserting the 0666 they report for every regular file would
// test the platform rather than this package.
func assertMode(t *testing.T, path string, want os.FileMode, unit int) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
