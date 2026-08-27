//go:build linux

package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// Item 15. A file handle's Space() agrees with the path-based Space() for
// the same file, proving the two accounting paths cannot silently disagree.
func TestFileSpaceAgreesWithThePathProbe(t *testing.T) {
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
	defer closeAfter(f.f, "f")

	byHandle, err := f.Space()
	if err != nil {
		t.Fatalf("space by handle: %v", err)
	}
	if byHandle.Total != byPath.Total {
		t.Fatalf("handle reports total %d, path reports %d", byHandle.Total, byPath.Total)
	}
	// Available moves under us: anything else on the host writes to the same
	// filesystem between the two probes. A block size read at the wrong scale
	// is off by a factor, so a one percent band separates the two.
	if !within(byHandle.Available, byPath.Available, 100) {
		t.Fatalf("handle reports available %d, path reports %d", byHandle.Available, byPath.Available)
	}
}

// within reports whether a and b differ by no more than 1/ratio of the larger.
func within(a, b uint64, ratio uint64) bool {
	hi, lo := a, b
	if lo > hi {
		hi, lo = lo, hi
	}
	return hi-lo <= hi/ratio
}

// Item 16. DirDev answers for the directory asked about, not the share
// root: the root's own device for the root itself, a file's parent's
// device for a file (the same device absent a nested mount), and an error,
// not a wrong device, for a path whose parent does not exist.
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

func TestSpaceAnswersForThePathAskedAbout(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "x")
	r := share(t, host, denyPolicy())

	root, err := r.Space(RootPath())
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	if root.Total == 0 {
		t.Fatal("total is zero")
	}
	if root.Available > root.Free {
		t.Fatal("available exceeds free, so the reserved blocks were counted as writable")
	}
	file, err := r.Space(mustParse(t, "f"))
	if err != nil {
		t.Fatalf("space for a file: %v", err)
	}
	if file.Total != root.Total {
		t.Fatal("a file was answered from a different filesystem than its parent")
	}
}
