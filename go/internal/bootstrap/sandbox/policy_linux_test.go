//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	securitylinux "github.com/heavycaffeiner/hanami/security/linux"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/system/jail"
)

func hasWritablePath(policy securitylinux.Policy, path string) bool {
	for _, got := range policy.WritablePaths {
		if got == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func findGrant(policy securitylinux.Policy, path string) (securitylinux.Grant, bool) {
	for _, grant := range policy.Grants {
		if grant.Path == filepath.Clean(path) {
			return grant, true
		}
	}
	return securitylinux.Grant{}, false
}

func TestBuildPolicyMapsHardeningModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  jail.Policy
		want securitylinux.PolicyMode
	}{
		{"required", jail.Required, securitylinux.ModeRequired},
		{"preferred", jail.Preferred, securitylinux.ModePreferred},
		{"off", jail.Off, securitylinux.ModeOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := runtimecfg.Defaults()
			values.Hardening = tc.set
			if got := BuildPolicy(values, t.TempDir(), nil, nil, nil).Mode; got != tc.want {
				t.Fatalf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildPolicyPreservesProcessHardeningLayers(t *testing.T) {
	values := runtimecfg.Defaults()
	policy := BuildPolicy(values, t.TempDir(), nil, nil, nil)
	if !policy.Landlock || !policy.Seccomp || !policy.ExceptExec {
		t.Fatalf("hardening layers = landlock %v seccomp %v except-exec %v", policy.Landlock, policy.Seccomp, policy.ExceptExec)
	}
	want := []string{"ptrace", "process_vm_readv", "process_vm_writev", "mount", "kexec_load", "kexec_file_load", "bpf", "userfaultfd"}
	if len(policy.DenySyscalls) != len(want) {
		t.Fatalf("denied syscalls = %v, want %v", policy.DenySyscalls, want)
	}
	for i := range want {
		if policy.DenySyscalls[i] != want[i] {
			t.Fatalf("denied syscalls = %v, want %v", policy.DenySyscalls, want)
		}
	}
}

func TestBuildPolicyGrantsRuntimeExecutableWhenPresent(t *testing.T) {
	if _, err := os.Stat("/stowcloud"); err != nil {
		t.Skip("runtime executable is not present outside the container image")
	}
	policy := BuildPolicy(runtimecfg.Defaults(), t.TempDir(), nil, nil, nil)
	grant, ok := findGrant(policy, "/stowcloud")
	want := securitylinux.RightReadFile
	if !ok || grant.Access&want != want {
		t.Fatalf("runtime executable grant = %+v, present %v", grant, ok)
	}
}

func TestBuildPolicyGrantsConfiguredDirectories(t *testing.T) {
	values := runtimecfg.Defaults()
	values.SMB.Enabled = true
	values.SMBConfigDir = filepath.Join(t.TempDir(), "smb-config")
	values.SMBSocket = filepath.Join(t.TempDir(), "run", "smb.sock")
	values.ThumbnailDir = filepath.Join(t.TempDir(), "thumbs")
	dataDir := filepath.Join(t.TempDir(), "data")
	root := filepath.Join(t.TempDir(), "mount")
	parent := filepath.Join(t.TempDir(), "shares")
	policy := BuildPolicy(values, dataDir, []string{root}, []string{filepath.Join(parent, "photos")}, nil)
	for _, path := range []string{dataDir, values.SMBConfigDir, filepath.Dir(values.SMBSocket), values.ThumbnailDir, root, parent} {
		if !hasWritablePath(policy, path) {
			t.Errorf("policy does not grant writable path %q: %+v", path, policy.WritablePaths)
		}
	}
}

func TestBuildPolicyRefusesRootAndGrantsExactContainerRights(t *testing.T) {
	containerDir := t.TempDir()
	container := filepath.Join(containerDir, "photos.hc")
	policy := BuildPolicy(runtimecfg.Defaults(), t.TempDir(), []string{"/"}, []string{"/srv"}, []string{"/", container})
	if hasWritablePath(policy, "/") {
		t.Fatal("policy grants filesystem root")
	}
	if hasWritablePath(policy, containerDir) {
		t.Fatal("policy grants the container parent")
	}
	grant, ok := findGrant(policy, container)
	if !ok {
		t.Fatalf("policy does not grant exact container: %+v", policy.Grants)
	}
	want := securitylinux.RightReadFile | securitylinux.RightWriteFile | securitylinux.RightTruncate
	if grant.Access != want {
		t.Fatalf("container rights = %#x, want %#x", grant.Access, want)
	}
	if !hasWritablePath(policy, "/srv") {
		t.Fatalf("share host directly below root was not granted as itself: %+v", policy.WritablePaths)
	}
}

func TestBuildPolicyResolverGrantIsReadOnly(t *testing.T) {
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		t.Skip("this host has no /etc/resolv.conf")
	}
	policy := BuildPolicy(runtimecfg.Defaults(), t.TempDir(), nil, nil, nil)
	grant, ok := findGrant(policy, "/etc/resolv.conf")
	if !ok {
		t.Fatalf("resolver grant missing: %+v", policy.Grants)
	}
	if grant.Access != securitylinux.RightReadFile {
		t.Fatalf("resolver rights = %#x, want read-file only", grant.Access)
	}
	if hasWritablePath(policy, "/etc") {
		t.Fatal("policy grants all of /etc")
	}
}
