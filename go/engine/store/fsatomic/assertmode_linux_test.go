//go:build linux

package fsatomic

import (
	"os"
	"testing"
)

// assertMode checks the permission bits a replacement asked for.
//
// The mode is the point of the call on this platform: a key ring and a
// credential sidecar are written 0600, and a replacement that widened them
// would publish a secret. Windows has no POSIX permission bits and reports
// 0666 for every regular file, so the check lives here rather than in the
// shared test.
func assertMode(t *testing.T, path string, want os.FileMode, unit int) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Errorf("unit %d mode %04o, want %04o", unit, got, want)
	}
}
