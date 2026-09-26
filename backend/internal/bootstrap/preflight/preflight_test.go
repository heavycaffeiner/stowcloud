//go:build linux

package preflight

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/mountinfo"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/instance"
)

// A second start against a live data directory is refused before it opens,
// and so before it can migrate, the state database.
func TestLoadRefusesAHeldDataDirectoryBeforeOpeningState(t *testing.T) {
	dir := t.TempDir()
	held, err := instance.Take(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	t.Cleanup(func() {
		if rerr := held.Release(); rerr != nil {
			t.Errorf("releasing the lock: %v", rerr)
		}
	})

	if _, err := Load(context.Background(), Options{DataDir: dir, SkipRootDiscovery: true}); err == nil {
		t.Fatal("Load succeeded against a data directory another owner holds")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("the refused Load touched state.db: %v", err)
	}
}

func TestShareRootsAppliesEachRule(t *testing.T) {
	xfsBind := t.TempDir()
	hostsBind := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsBind, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("writing the fake /etc/hosts fixture: %v", err)
	}
	cases := []struct {
		name  string
		mount mountinfo.Mount
		want  bool
	}{
		{"a bind mount on xfs is granted", mountinfo.Mount{Point: xfsBind, FsType: "xfs"}, true},
		{"an overlay root is refused", mountinfo.Mount{Point: "/", FsType: "overlay"}, false},
		{"an ext4 mount at / is refused", mountinfo.Mount{Point: "/", FsType: "ext4"}, false},
		{"/proc is refused though tmpfs is admitted", mountinfo.Mount{Point: "/proc", FsType: "tmpfs"}, false},
		{"/sys/firmware is refused though tmpfs is admitted", mountinfo.Mount{Point: "/sys/firmware", FsType: "tmpfs"}, false},
		{"/dev/shm is refused though tmpfs is admitted", mountinfo.Mount{Point: "/dev/shm", FsType: "tmpfs"}, false},
		{"a tmpfs bind at a file is refused", mountinfo.Mount{Point: hostsBind, FsType: "tmpfs"}, false},
		{"an unsupported filesystem is refused", mountinfo.Mount{Point: xfsBind, FsType: "nfs"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withoutNamedShareDirs(shareRoots([]mountinfo.Mount{c.mount}))
			admitted := len(got) == 1 && got[0] == filepath.Clean(c.mount.Point)
			if admitted != c.want {
				t.Errorf("shareRoots(%+v) = %v, want admitted=%v", c.mount, got, c.want)
			}
		})
	}
}

func withoutNamedShareDirs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !slices.Contains(namedShareDirs(), p) {
			out = append(out, p)
		}
	}
	return out
}

func TestShareRootsDedupesAndSorts(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	mounts := []mountinfo.Mount{{Point: b, FsType: "xfs"}, {Point: a, FsType: "xfs"}, {Point: a, FsType: "xfs"}}
	want := []string{a, b}
	slices.Sort(want)
	got := withoutNamedShareDirs(shareRoots(mounts))
	if !slices.Equal(got, want) {
		t.Fatalf("shareRoots = %v, want %v", got, want)
	}
	if !slices.IsSorted(got) {
		t.Errorf("shareRoots did not return a sorted result: %v", got)
	}
}

func TestShareRootsGrantsNamedDirsWithNoMounts(t *testing.T) {
	got := shareRoots(nil)
	var present []string
	for _, dir := range namedShareDirs() {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			present = append(present, dir)
		}
	}
	slices.Sort(present)
	if !slices.Equal(got, present) {
		t.Fatalf("shareRoots(nil) = %v, want %v", got, present)
	}
}

func TestShareRootsOmitsMissingNamedDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for _, dir := range namedShareDirs() {
		if dir == missing {
			t.Fatalf("fixture collides with a real named dir: %s", dir)
		}
	}
	got := shareRoots(nil)
	if slices.Contains(got, missing) {
		t.Errorf("shareRoots(nil) contains a directory that was never created: %v", got)
	}
}

func TestShareRootsRefusesPathsOutsideBothSources(t *testing.T) {
	got := shareRoots(nil)
	for _, refused := range []string{"/etc", "/usr", "/boot", "/var"} {
		if slices.Contains(got, refused) {
			t.Errorf("shareRoots(nil) admitted %s, which neither source names", refused)
		}
	}
}

func TestShareRootsDedupesMountAndNamedDir(t *testing.T) {
	var namedDir string
	for _, dir := range namedShareDirs() {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			namedDir = dir
			break
		}
	}
	if namedDir == "" {
		t.Skip("no named share directory exists on this host to overlap a mount with")
	}
	got := shareRoots([]mountinfo.Mount{{Point: namedDir, FsType: "ext4"}})
	count := 0
	for _, p := range got {
		if p == namedDir {
			count++
		}
	}
	if count != 1 {
		t.Errorf("shareRoots reported %s %d times, want exactly once: %v", namedDir, count, got)
	}
}
