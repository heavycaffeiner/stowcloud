//go:build linux

// Package sandbox assembles the process security policy and runs jailed helper
// processes during bootstrap.
package sandbox

import (
	"os"
	"path/filepath"

	securitylinux "github.com/heavycaffeiner/hanami/security/linux"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
)

// BuildPolicy builds the server's process policy from one startup configuration
// snapshot. Exact-file grants remain separate from directory grants: a
// container file must not expose its siblings.
func BuildPolicy(values runtimecfg.Values, dataDir string, roots, shareHosts, exactPaths []string) securitylinux.Policy {
	mode := policyMode(values.Hardening)
	policy := securitylinux.Policy{
		Mode:       mode,
		Landlock:   mode != securitylinux.ModeOff,
		Seccomp:    mode != securitylinux.ModeOff,
		ExceptExec: true,
	}
	if mode != securitylinux.ModeOff {
		policy.DenySyscalls = []string{
			"ptrace",
			"process_vm_readv",
			"process_vm_writev",
			"mount",
			"kexec_load",
			"kexec_file_load",
			"bpf",
			"userfaultfd",
		}
	}
	seen := make(map[string]bool)
	grantAll := func(path string) {
		clean := filepath.Clean(path)
		if path == "" || clean == "/" || clean == "." || seen[clean] {
			return
		}
		seen[clean] = true
		policy.WritablePaths = append(policy.WritablePaths, clean)
	}
	grantAll(dataDir)
	if values.SMB.Enabled && values.SMBConfigDir != "" {
		grantAll(values.SMBConfigDir)
	}
	if values.SMB.Enabled && values.SMBSocket != "" {
		if dir := filepath.Dir(values.SMBSocket); dir != "" && dir != "/" {
			grantAll(dir)
		}
	}
	if values.ThumbnailDir != "" {
		grantAll(values.ThumbnailDir)
	}

	policy.Grants = append(policy.Grants, outboundGrants()...)

	for _, root := range roots {
		if root != "" {
			grantAll(root)
		}
	}
	for _, host := range shareHosts {
		if host == "" {
			continue
		}
		clean := filepath.Clean(host)
		parent := filepath.Dir(clean)
		if parent == "/" || parent == "." || parent == clean {
			parent = clean
		}
		grantAll(parent)
	}
	for _, path := range exactPaths {
		clean := filepath.Clean(path)
		if path == "" || clean == "/" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		policy.Grants = append(policy.Grants, securitylinux.Grant{
			Path: clean,
			Access: securitylinux.RightReadFile |
				securitylinux.RightWriteFile |
				securitylinux.RightTruncate,
		})
	}
	return policy
}

func policyMode(policy interface{ String() string }) securitylinux.PolicyMode {
	switch policy.String() {
	case "off":
		return securitylinux.ModeOff
	case "preferred":
		return securitylinux.ModePreferred
	default:
		return securitylinux.ModeRequired
	}
}

// outboundGrants are the read-only files needed by the resolver and TLS trust
// store. Missing paths are skipped so images with a different layout remain
// startable.
func outboundGrants() []securitylinux.Grant {
	candidates := []securitylinux.Grant{
		{Path: "/stowcloud", Access: securitylinux.RightReadFile | securitylinux.RightExecute},
		{Path: "/etc/resolv.conf", Access: securitylinux.RightReadFile},
		{Path: "/etc/hosts", Access: securitylinux.RightReadFile},
		{Path: "/etc/nsswitch.conf", Access: securitylinux.RightReadFile},
		{Path: "/etc/ssl/certs", Access: securitylinux.RightReadFile | securitylinux.RightReadDirectory | securitylinux.RightExecute},
		{Path: "/etc/pki/tls/certs", Access: securitylinux.RightReadFile | securitylinux.RightReadDirectory | securitylinux.RightExecute},
	}
	out := make([]securitylinux.Grant, 0, len(candidates))
	for _, grant := range candidates {
		if _, err := os.Stat(grant.Path); err == nil {
			out = append(out, grant)
		}
	}
	return out
}
