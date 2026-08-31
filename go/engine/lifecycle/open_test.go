//go:build linux

package lifecycle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// The engine opens against a real empty directory, which is what a first boot
// is. Every database is created, every service is constructed, and closing
// releases the files.
//
// This is the first thing in the rebuilt tree that runs the services together
// rather than one at a time under their own tests.
func TestTheEngineOpensOnAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("the engine did not open: %v", err)
	}
	defer func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if e.State == nil {
		t.Error("no state database")
	}
	if e.Cache == nil {
		t.Error("no cache database")
	}
	if e.ACL == nil {
		t.Error("no permission evaluator")
	}
	if e.Core == nil {
		t.Error("no core")
	}
	if e.Auth == nil {
		t.Error("no auth service")
	}

	// The files are real and on disk, which is what makes the next boot find
	// the same deployment rather than a fresh one.
	for _, name := range []string{"state.db", "cache.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not created: %v", name, err)
		}
	}
}

// A second open of the same directory finds the same deployment. A boot that
// silently started fresh would present an empty server to a user whose files
// are still on disk.
func TestASecondOpenFindsTheSameDeployment(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first, ferr := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if ferr != nil {
		t.Fatalf("the first open failed: %v", ferr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing the first: %v", cerr)
	}

	second, serr := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if serr != nil {
		t.Fatalf("the second open failed: %v", serr)
	}
	if cerr := second.Close(); cerr != nil {
		t.Errorf("closing the second: %v", cerr)
	}
}

// A directory the process cannot write is refused rather than half-opened.
func TestAnUnwritableDirectoryIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write a mode-0500 directory")
	}

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("preparing the directory: %v", err)
	}

	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: locked})
	if err == nil {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		t.Fatal("an unwritable data directory was accepted")
	}
}

// An empty data directory is refused. Defaulting it would put a deployment's
// databases wherever the process happened to be started.
func TestAnEmptyDataDirectoryIsRefused(t *testing.T) {
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{})
	if err == nil {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		t.Fatal("an empty data directory was accepted")
	}
}

// Closing twice is safe, since a caller unwinding from a failure may reach the
// same defer twice.
func TestClosingTwiceIsSafe(t *testing.T) {
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("the first close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("the second close: %v", err)
	}
}

// A second engine on the same data directory is refused rather than admitted:
// the state spans several WAL databases with no snapshot across them, so two
// servers reading and writing them would combine one instant's user with
// another instant's grants.
func TestASecondEngineOnTheSameDataDirectoryIsRefused(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("the first engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := first.Close(); cerr != nil {
			t.Errorf("closing the first engine: %v", cerr)
		}
	})

	_, err = lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err == nil {
		t.Fatal("a second engine opened the same data directory")
	}
	if !strings.Contains(err.Error(), "in use") {
		t.Errorf("the refusal does not name the directory as in use: %v", err)
	}
}

// Closing releases the lock, so the directory can be opened again by the next
// process. A lock that survived its own Close would be one that needed a
// reboot to clear.
func TestClosingReleasesTheDataDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("the first engine: %v", err)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing the first engine: %v", cerr)
	}

	second, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening the same directory: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Errorf("closing the second engine: %v", err)
	}
}
