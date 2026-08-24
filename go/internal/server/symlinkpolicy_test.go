// Linux only, because what it tests is.
//go:build linux

package server

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// shareRaw is a minimal valid config with one share.
func shareRaw(policy string) raw {
	r := raw{}
	r.Server.DataDir = "/tmp/x"
	r.HTTP.AppHosts = []string{"localhost"}
	r.Shares = append(r.Shares, struct {
		Name             string `toml:"name"`
		HostPath         string `toml:"host_path"`
		SharedExternally bool   `toml:"shared_externally"`
		TrashEnabled     bool   `toml:"trash_enabled"`
		SymlinkPolicy    string `toml:"symlink_policy"`
	}{Name: "docs", HostPath: "/tmp/s", SymlinkPolicy: policy})
	return r
}

// A share's symlink policy reaches the share.
//
// The type, its three modes and a resolver that branches on all three all
// existed, and there was no config key: every share got Deny whatever an
// operator wrote, and nothing said so.
func TestAShareCarriesItsSymlinkPolicy(t *testing.T) {
	for _, c := range []struct {
		name string
		want vfs.SymlinkPolicy
	}{
		{"deny", vfs.SymlinkDeny},
		{"within_share", vfs.SymlinkWithinShare},
		{"follow", vfs.SymlinkFollow},
	} {
		cfg, err := Validate(shareRaw(c.name))
		if err != nil {
			t.Fatalf("symlink_policy %q: %v", c.name, err)
		}
		if got := cfg.Shares[0].Symlink; got != c.want {
			t.Errorf("symlink_policy %q became %v, want %v", c.name, got, c.want)
		}
	}
}

// An absent key is the restrictive policy.
func TestAShareWithNoSymlinkPolicyDenies(t *testing.T) {
	cfg, err := Validate(shareRaw(""))
	if err != nil {
		t.Fatalf("a share with no policy: %v", err)
	}
	if got := cfg.Shares[0].Symlink; got != vfs.SymlinkDeny {
		t.Fatalf("an unset policy became %v, want deny", got)
	}
}

// A name this build does not implement is refused rather than defaulted.
//
// An operator who wrote a typo and silently got Deny believes the share
// follows links that it does not, and the difference is invisible until
// somebody's link fails to open.
func TestAnUnknownSymlinkPolicyIsRefused(t *testing.T) {
	_, err := Validate(shareRaw("sometimes"))
	if err == nil {
		t.Fatal("an unknown symlink policy was accepted")
	}
	if !strings.Contains(err.Error(), "sometimes") {
		t.Errorf("the refusal does not name the value: %v", err)
	}
	if !strings.Contains(err.Error(), "docs") {
		t.Errorf("the refusal does not name the share: %v", err)
	}
}

// The size guard's switch and its bounds have to agree.
//
// Turning it on with neither bound set is a guard that can never trip, which
// reads as protection and is not.
func TestTheSizeGuardRefusesToBeOnWithNoBound(t *testing.T) {
	r := raw{}
	r.Server.DataDir = "/tmp/x"
	r.HTTP.AppHosts = []string{"localhost"}
	r.DB.SizeGuard = true

	if _, err := Validate(r); err == nil {
		t.Fatal("size_guard on with no bound was accepted")
	}

	floor := uint64(1 << 30)
	r.DB.MinFreeBytes = &floor
	cfg, err := Validate(r)
	if err != nil {
		t.Fatalf("size_guard with a floor: %v", err)
	}
	if !cfg.DBGuard.Enabled() || cfg.DBGuard.MinFreeBytes != floor {
		t.Errorf("the floor did not reach the guard: %+v", cfg.DBGuard)
	}
}

// The bounds are stored and not applied while the switch is off, so an
// operator can set the numbers before turning it on.
func TestTheSizeGuardIsOffUnlessSwitchedOn(t *testing.T) {
	r := raw{}
	r.Server.DataDir = "/tmp/x"
	r.HTTP.AppHosts = []string{"localhost"}
	floor := uint64(1 << 30)
	r.DB.MinFreeBytes = &floor

	cfg, err := Validate(r)
	if err != nil {
		t.Fatalf("a bound with the switch off: %v", err)
	}
	if cfg.DBGuard.Enabled() {
		t.Fatal("the guard is enabled with size_guard off")
	}
}
