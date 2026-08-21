package smbpublish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
)

// Publishing is checked by looking at the files, because the sidecar reads
// files and nothing else. A test that only checked the return value would pass
// with nothing written.

// fakeAccounts stands in for the auth service, which cannot be built here
// without a database and a master key. What matters to this package is that
// both files are published, never one.
type fakeAccounts struct {
	passwdPath string
	passdbDone bool
	fail       error
}

func (f *fakeAccounts) PublishPasswdEntries(_ context.Context, path string, _ uint32) error {
	if f.fail != nil {
		return f.fail
	}
	f.passwdPath = path
	return os.WriteFile(path, []byte("alice:x:2001:1000::/nonexistent:/sbin/nologin\n"), 0o600)
}

func (f *fakeAccounts) PublishPassdb(context.Context) error {
	if f.fail != nil {
		return f.fail
	}
	f.passdbDone = true
	return nil
}

func enabledConfig() smb.Config {
	return smb.Config{
		Enabled:     true,
		Workgroup:   "WORKGROUP",
		ServiceUser: "scsvc",
	}
}

func TestPublishingWritesWhatTheSidecarReads(t *testing.T) {
	dir := t.TempDir()
	accounts := &fakeAccounts{}

	// No socket: a bare-metal deployment where something else applies the
	// files. The files still have to be there.
	report, err := Publish(context.Background(), Deps{
		Auth: accounts, ConfigDir: dir,
	}, enabledConfig())
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if !report.OK {
		t.Errorf("the report is not ok: %s", report.Error)
	}

	conf, rerr := os.ReadFile(filepath.Join(dir, "smb.conf"))
	if rerr != nil {
		t.Fatalf("no configuration was written: %v", rerr)
	}
	if !strings.Contains(string(conf), "[global]") {
		t.Errorf("the configuration is not one:\n%s", conf)
	}
	// The closed case, because this process cannot see the host's devices.
	// The sidecar rewrites it in the namespace that can.
	if !strings.Contains(string(conf), "interfaces = lo") {
		t.Errorf("the configuration does not render the closed network case:\n%s", conf)
	}

	if accounts.passwdPath == "" || !accounts.passdbDone {
		t.Error("only one of the two credential files was published, which is what makes a login fail as an unknown user")
	}
}

// The policy file is how the sidecar learns the two things it cannot infer.
func TestThePolicyFileCarriesBothFlags(t *testing.T) {
	dir := t.TempDir()
	cfg := enabledConfig()
	cfg.AllowPublicBind = true
	cfg.Interfaces = []string{"192.168.1.10"}

	if _, err := Publish(context.Background(), Deps{Auth: &fakeAccounts{}, ConfigDir: dir}, cfg); err != nil {
		t.Fatalf("publishing: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "network.policy"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"allow_public_bind=1", "pinned_interfaces=1"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the policy is missing %s: %q", want, body)
		}
	}

	// The sidecar has to read them back as what was meant.
	policy := smbagent.ReadPolicy(filepath.Join(dir, "network.policy"))
	if !policy.AllowPublicBind || !policy.PinnedInterfaces {
		t.Fatalf("the sidecar read the policy as %+v, want both set", policy)
	}
}

// A pin is not written when nothing was pinned, or detection would be turned
// off on a deployment that never asked for that and SMB would answer on
// loopback only.
func TestNoPinIsWrittenWhenNothingWasPinned(t *testing.T) {
	dir := t.TempDir()
	if _, err := Publish(context.Background(), Deps{Auth: &fakeAccounts{}, ConfigDir: dir}, enabledConfig()); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "network.policy")) //nolint:errcheck // an absent policy is an empty one, which is the case being checked.
	if strings.Contains(string(body), "pinned_interfaces=1") {
		t.Fatalf("a pin was written without one being configured: %q", body)
	}
	if smbagent.ReadPolicy(filepath.Join(dir, "network.policy")).PinnedInterfaces {
		t.Fatal("the sidecar would skip detection, so SMB would answer on loopback only")
	}
}

// Turning SMB off removes the files, because the sidecar reads absence as the
// off switch and tears down the accounts with it. An emptied file left behind
// would keep a revoked credential working.
func TestTurningItOffRemovesEverythingTheSidecarReads(t *testing.T) {
	dir := t.TempDir()
	if _, err := Publish(context.Background(), Deps{Auth: &fakeAccounts{}, ConfigDir: dir}, enabledConfig()); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	// The credential file is written by the auth service in production; here
	// it stands for one that exists.
	if err := os.WriteFile(filepath.Join(dir, "smbpasswd"), []byte("alice:2001:...\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	off := enabledConfig()
	off.Enabled = false
	if _, err := Publish(context.Background(), Deps{Auth: &fakeAccounts{}, ConfigDir: dir}, off); err != nil {
		t.Fatalf("turning it off: %v", err)
	}

	for _, name := range []string{"smb.conf", "smbpasswd", "passwd", "network.policy"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived being turned off, so the sidecar keeps serving it", name)
		}
	}
}

// A configuration the renderer refuses never reaches the directory the sidecar
// reads: it would validate and promote whatever is there.
func TestARefusedRenderWritesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := enabledConfig()
	// Whitespace in a workgroup splits it into two values in the rendered
	// file, which the renderer refuses rather than escaping.
	cfg.Workgroup = "BAD NAME"

	if _, err := Publish(context.Background(), Deps{Auth: &fakeAccounts{}, ConfigDir: dir}, cfg); err == nil {
		t.Fatal("a configuration the renderer refuses was published")
	}
	if _, err := os.Stat(filepath.Join(dir, "smb.conf")); err == nil {
		t.Error("a refused configuration reached the directory the sidecar reads")
	}
}

// A credential publish that fails is a failed publish. Writing the
// configuration and not the credentials leaves shares nobody can authenticate
// to.
func TestAFailedCredentialPublishFailsTheWholePublish(t *testing.T) {
	dir := t.TempDir()
	accounts := &fakeAccounts{fail: os.ErrPermission}

	if _, err := Publish(context.Background(), Deps{Auth: accounts, ConfigDir: dir}, enabledConfig()); err == nil {
		t.Fatal("a failed credential publish reported success")
	}
}
