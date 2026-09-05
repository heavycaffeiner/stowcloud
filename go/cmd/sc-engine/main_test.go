//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/mountinfo"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
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
			// namedShareDirs are granted independent of any mount rule, and
			// this host may have some of them for real; strip them so this
			// table keeps testing only the four mount rules.
			got := withoutNamedShareDirs(shareRoots([]mountinfo.Mount{c.mount}))
			admitted := len(got) == 1 && got[0] == filepath.Clean(c.mount.Point)
			if admitted != c.want {
				t.Errorf("shareRoots(%+v) = %v, want admitted=%v", c.mount, got, c.want)
			}
		})
	}
}

// withoutNamedShareDirs drops whatever namedShareDirs contributed, so a
// mount-rule assertion is not thrown off by directories real on the host
// running the test.
func withoutNamedShareDirs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !slices.Contains(namedShareDirs(), p) {
			out = append(out, p)
		}
	}
	return out
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

	got := withoutNamedShareDirs(shareRoots(mounts))
	if !slices.Equal(got, want) {
		t.Fatalf("shareRoots = %v, want %v", got, want)
	}
	if !slices.IsSorted(got) {
		t.Errorf("shareRoots did not return a sorted result: %v", got)
	}
}

// namedShareDirs covers the single-root host: no bind mounts are reported at
// all, and the result still has to contain whichever named directories are
// actually present on this machine, since that is the only source that can
// grant anything there.
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

// A named directory that does not exist on this host must not appear, even
// though it is on the list; existence is checked, not assumed.
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

// Nothing outside the two sources is ever admitted, regardless of how this
// particular machine is laid out.
func TestShareRootsRefusesPathsOutsideBothSources(t *testing.T) {
	got := shareRoots(nil)
	for _, refused := range []string{"/etc", "/usr", "/boot", "/var"} {
		if slices.Contains(got, refused) {
			t.Errorf("shareRoots(nil) admitted %s, which neither source names", refused)
		}
	}
}

// A mount point and a named directory that name the same path must collapse
// to a single entry, same as two mounts reported for the same point do.
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

// granted reports whether spec grants exactly path, which is a different
// question from whether path is reachable: a grant on a parent makes the
// child reachable too, and the point of an exact grant is that it does not.
func granted(spec jail.Spec, path string) bool {
	for _, g := range spec.GrantBeneath {
		if g.Path == path {
			return true
		}
	}
	return false
}

// A VeraCrypt container is one file the engine opens, so it is granted as
// itself. Granting its parent instead would hand over every other container
// an operator keeps beside it, which is the mistake this pins.
func TestJailSpecGrantsAContainerFileAndNotItsDirectory(t *testing.T) {
	dir := t.TempDir()
	container := filepath.Join(dir, "photos.hc")

	spec := jailSpec(runtimecfg.Defaults(), t.TempDir(), nil, nil, []string{container})

	if !granted(spec, container) {
		t.Errorf("the container file is not granted: %v", spec.GrantBeneath)
	}
	if granted(spec, dir) {
		t.Errorf("the container's directory is granted, which hands over every container beside it: %v", spec.GrantBeneath)
	}
}

// A share host is a directory somebody adds folders beside, so its parent is
// what gets granted.
func TestJailSpecGrantsAShareHostsParent(t *testing.T) {
	parent := t.TempDir()
	host := filepath.Join(parent, "photos")

	spec := jailSpec(runtimecfg.Defaults(), t.TempDir(), nil, []string{host}, nil)

	if !granted(spec, parent) {
		t.Errorf("the share host's parent is not granted: %v", spec.GrantBeneath)
	}
}

// "/" is the one path this sandbox exists to withhold, so a share host
// directly beneath it grants the host itself rather than the root.
func TestJailSpecNeverGrantsTheRoot(t *testing.T) {
	spec := jailSpec(runtimecfg.Defaults(), t.TempDir(), []string{"/"}, []string{"/srv"}, []string{"/"})

	if granted(spec, "/") {
		t.Errorf("the root is granted: %v", spec.GrantBeneath)
	}
	if !granted(spec, "/srv") {
		t.Errorf("a share host directly beneath the root is not granted as itself: %v", spec.GrantBeneath)
	}
}
