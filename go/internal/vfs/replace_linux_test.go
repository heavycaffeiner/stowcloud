//go:build linux

package vfs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// O_CREAT filters the mode through umask, so a key file created under the
// default 022 would be 0644 and readable by everything on the box. The umask is
// set to refuse every bit here, which is the strongest form of the question.
func TestReplaceFileDurableTakesTheExactModeDespiteUmask(t *testing.T) {
	// The directory comes first. A umask of 0777 refuses the mkdir that
	// TempDir does, so setting it any earlier fails the test before the thing
	// it is about has run.
	dir := t.TempDir()
	old := unix.Umask(0o777)
	defer unix.Umask(old)

	path := filepath.Join(dir, "master.key")
	if err := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, werr := f.Write([]byte("k"))
		return werr
	}); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode %04o, want 0600", got)
	}
	assertNoStagingResidue(t, dir)
}
