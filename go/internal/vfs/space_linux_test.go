//go:build linux

package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// An open handle answers the same accounting its path does. The two probes
// exist so a caller holding a file does not re-resolve a name it already has,
// which is only true if they agree.
func TestFileSpaceAgreesWithTheRootProbe(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "x")
	r := share(t, host, denyPolicy())

	byPath, err := r.Space(mustParse(t, "f"))
	if err != nil {
		t.Fatalf("space by path: %v", err)
	}

	f, err := r.OpenRead(mustParse(t, "f"), IntentRead)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	byHandle, err := f.Space()
	if err != nil {
		t.Fatalf("space by handle: %v", err)
	}
	if byHandle.Total != byPath.Total {
		t.Fatalf("handle reports total %d, path reports %d", byHandle.Total, byPath.Total)
	}
}

// DirDev answers for the directory asked about, and for a file it answers from
// the parent that holds it. Without a nested mount both are the share root's
// device, which is what this asserts; the nested case needs a real mount and
// lives in the namespace test.
func TestDirDevAnswersForTheDirectoryAskedAbout(t *testing.T) {
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(host, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(host, "sub", "f"), "x")
	r := share(t, host, denyPolicy())

	root, err := r.DirDev(RootPath())
	if err != nil {
		t.Fatalf("dev of the root: %v", err)
	}
	if root != r.Dev() {
		t.Fatalf("the root directory reports device %d, the share root holds %d", root, r.Dev())
	}
	file, err := r.DirDev(mustParse(t, "sub/f"))
	if err != nil {
		t.Fatalf("dev of a file: %v", err)
	}
	if file != root {
		t.Fatalf("a file reports device %d, its parent %d", file, root)
	}
	if _, err := r.DirDev(mustParse(t, "sub/missing/deeper")); err == nil {
		t.Fatal("a path whose parent does not exist answered a device")
	}
}
