//go:build linux

package adapters

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	storage "github.com/stowcloud/storage"
)

func TestLocalConformsToNeutralHierarchy(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(host, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := vfs.OpenShareRoot(1, host, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("closing root: %v", closeErr)
		}
	})
	adapter, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	var hierarchy storage.ReadHierarchy = adapter
	var health storage.HealthChecker = adapter
	var space storage.SpaceReporter = adapter
	var materializer storage.Materializer = adapter
	var renamer storage.Renamer = adapter
	_ = hierarchy
	_ = health
	_ = space
	_ = materializer
	_ = renamer

	ctx := context.Background()
	filePath, err := storage.ParsePath("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := adapter.Stat(ctx, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != storage.KindFile || entry.Size != 5 || entry.Path != filePath {
		t.Fatalf("entry = %+v", entry)
	}
	f, err := adapter.OpenRead(ctx, filePath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if closeErr := f.Close(); err != nil {
		t.Fatal(err)
	} else if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "hello" {
		t.Fatalf("read %q", got)
	}

	rootPath := storage.RootPath()
	entries, err := adapter.ReadDir(ctx, rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadDir returned %d entries, want 2: %+v", len(entries), entries)
	}
	if got := adapter.Health(ctx); got.Status != storage.HealthOK || got.Err != nil {
		t.Fatalf("health = %+v", got)
	}
	available, err := adapter.Space(ctx, rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if available.Total == 0 || available.Free > available.Total {
		t.Fatalf("space = %+v", available)
	}

	lease, err := adapter.Materialize(ctx, filePath)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := lease.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("materialized read %q", buf)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRenameUsesRootConfinement(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "from"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := vfs.OpenShareRoot(1, host, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("closing root: %v", closeErr)
		}
	})
	adapter, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	from, err := storage.ParsePath("from")
	if err != nil {
		t.Fatal(err)
	}
	to, err := storage.ParsePath("to")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rename(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host, "to")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host, "from")); !os.IsNotExist(err) {
		t.Fatalf("source stat = %v", err)
	}
}

func TestLocalRejectsInvalidAndEscapingPaths(t *testing.T) {
	host := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(host, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := vfs.OpenShareRoot(1, host, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("closing root: %v", closeErr)
		}
	})
	adapter, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	if _, parseErr := storage.ParsePath("../secret"); parseErr == nil {
		t.Fatal("public path parser accepted traversal")
	}
	link, err := storage.ParsePath("link")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.OpenRead(context.Background(), link); !errors.Is(err, vfs.ErrSymlinkDenied) {
		t.Fatalf("OpenRead through escaping symlink = %v, want ErrSymlinkDenied", err)
	}
}
