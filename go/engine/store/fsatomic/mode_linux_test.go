//go:build linux

package fsatomic

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// O_CREAT filters the mode through the process umask, so a key file created
// under a permissive default would end up wider than intended and one
// created under a hostile umask would end up unreadable by its own creator.
// A umask refusing every bit is the strongest form of the question: nothing
// short of an explicit fchmod produces the exact mode asked for.
func TestReplaceFileDurableAppliesExactModeDespiteHostileUmask(t *testing.T) {
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
