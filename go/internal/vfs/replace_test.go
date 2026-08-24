package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFileDurableReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, werr := f.Write([]byte("new"))
		return werr
	}); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("the file holds %q", got)
	}
}

// A writer that fails part way is the case this helper exists for: the old key
// has to still be the key.
func TestReplaceFileDurableLeavesTheOldFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("the writer gave up")
	err := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		if _, werr := f.Write([]byte("half a k")); werr != nil {
			return werr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("replacing returned %v, want the writer's own error", err)
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "old" {
		t.Fatalf("a failed replacement changed the file to %q", got)
	}
	assertNoStagingResidue(t, dir)
}

func assertNoStagingResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsStagingName(e.Name()) {
			t.Errorf("a staging file survived: %s", e.Name())
		}
	}
}
