//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/mountinfo"
)

// shareRoots is exercised against mountinfo.Mount values built by hand, never
// against the real mount table, so a refusal proven here does not depend on
// what happens to be mounted on the machine running the test.
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
		// The bare-metal hole: an ext4 root passes the filesystem-type rule
		// and must be caught by the explicit "/" check on its own.
		{"an ext4 mount at / is refused", mountinfo.Mount{Point: "/", FsType: "ext4"}, false},
		// tmpfs is on the allow-list, so only the kernel-tree rule keeps
		// these out, not the filesystem-type rule.
		{"/proc is refused though tmpfs is admitted", mountinfo.Mount{Point: "/proc", FsType: "tmpfs"}, false},
		{"/sys/firmware is refused though tmpfs is admitted", mountinfo.Mount{Point: "/sys/firmware", FsType: "tmpfs"}, false},
		{"/dev/shm is refused though tmpfs is admitted", mountinfo.Mount{Point: "/dev/shm", FsType: "tmpfs"}, false},
		{"a tmpfs bind at a file is refused", mountinfo.Mount{Point: hostsBind, FsType: "tmpfs"}, false},
		{"an unsupported filesystem is refused", mountinfo.Mount{Point: xfsBind, FsType: "nfs"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shareRoots([]mountinfo.Mount{c.mount})
			admitted := len(got) == 1 && got[0] == filepath.Clean(c.mount.Point)
			if admitted != c.want {
				t.Errorf("shareRoots(%+v) = %v, want admitted=%v", c.mount, got, c.want)
			}
		})
	}
}

// Two mounts reported for the same point collapse to one entry, and the
// result comes back sorted regardless of the order mounts were reported in.
func TestShareRootsDedupesAndSorts(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	mounts := []mountinfo.Mount{
		{Point: b, FsType: "xfs"},
		{Point: a, FsType: "xfs"},
		{Point: a, FsType: "xfs"},
	}

	want := []string{a, b}
	slices.Sort(want)

	got := shareRoots(mounts)
	if !slices.Equal(got, want) {
		t.Fatalf("shareRoots = %v, want %v", got, want)
	}
	if !slices.IsSorted(got) {
		t.Errorf("shareRoots did not return a sorted result: %v", got)
	}
}
