// Linux only, because what it tests is.
//go:build linux

package smbpublish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
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

// The grant-to-share-list translation, which decides who can reach what over
// SMB. Nothing above exercises it: those tests pass no grants, so the
// rendering falls out early and the share blocks are never built.

// A share nobody has a grant on is left out entirely.
//
// Not rendered with an empty account list: an empty list in that format means
// every account, so rendering one publishes a share this server considers
// private to everybody who can reach the port.
func TestAShareWithNoGrantIsNotRendered(t *testing.T) {
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return nil, nil },
		Names:  func(context.Context, int64) (string, error) { return "alice", nil },
		Core:   nil,
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if len(defs) != 0 {
		t.Fatalf("a share with no grant was rendered: %+v", defs)
	}
}

// A grant on a subtree is not a share-level grant. That format has no notion
// of a permission beginning partway down a tree, so rendering one would grant
// the whole share.
func TestASubtreeGrantDoesNotBecomeAShareGrant(t *testing.T) {
	grants := []acl.Grant{{
		ID: 1, User: 7, Share: 1,
		Subpath: acl.NewPath("private"),
		Allow:   acl.Read,
	}}
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
		Names:  func(context.Context, int64) (string, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatalf("a grant on a subtree became a grant on the whole share: %+v", defs)
	}
}

// An account with any denial is left off rather than subtracted. That format
// has no denial that survives the other lists, so a partial translation would
// grant what this server denies.
func TestAnAccountWithADenialIsLeftOff(t *testing.T) {
	grants := []acl.Grant{{
		ID: 1, User: 7, Share: 1, Subpath: acl.NewPath(),
		Allow: acl.Read, Deny: acl.Write,
	}}
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
		Names:  func(context.Context, int64) (string, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatalf("an account carrying a denial was rendered: %+v", defs)
	}
}

// A grant without read is not access over this protocol, which has no notion
// of a write-only share.
func TestAGrantWithoutReadIsNotRendered(t *testing.T) {
	grants := []acl.Grant{{
		ID: 1, User: 7, Share: 1, Subpath: acl.NewPath(), Allow: acl.Create,
	}}
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
		Names:  func(context.Context, int64) (string, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatalf("a grant with no read became a share: %+v", defs)
	}
}

// An account whose name cannot be resolved is skipped rather than rendered
// with an empty one, which would be a name the daemon cannot look up.
func TestAnUnresolvableAccountIsSkipped(t *testing.T) {
	grants := []acl.Grant{{
		ID: 1, User: 7, Share: 1, Subpath: acl.NewPath(), Allow: acl.Read,
	}}
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
		Names:  func(context.Context, int64) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatalf("an account with no name was rendered: %+v", defs)
	}
}

// With no way to resolve a name, nothing is rendered: every grant would be
// attributed to nobody, and a share with no account list is a share open to
// everyone.
func TestWithNoNameResolverNothingIsRendered(t *testing.T) {
	grants := []acl.Grant{{
		ID: 1, User: 7, Share: 1, Subpath: acl.NewPath(), Allow: acl.Read,
	}}
	defs, err := shareDefs(context.Background(), Deps{
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatalf("shares were rendered with no way to name an account: %+v", defs)
	}
}

// A failure reading the grants fails the publish rather than rendering a
// configuration with no account lists in it.
func TestAGrantReadFailureFailsTheRender(t *testing.T) {
	// A Core is needed to get past the guard above, which is what makes this
	// exercise the read rather than the missing-dependency case.
	c, cerr := core.New(emptyStore(t), core.Options{ACL: acl.NewEvaluator()})
	if cerr != nil {
		t.Fatal(cerr)
	}

	_, err := shareDefs(context.Background(), Deps{
		Core: c,
		Grants: func(context.Context) ([]acl.Grant, error) {
			return nil, errors.New("the database is unavailable")
		},
		Names: func(context.Context, int64) (string, error) { return "alice", nil },
	})
	if err == nil {
		t.Fatal("a failed grant read rendered a configuration anyway")
	}
}

// emptyStore is a store with no shares in it, which is all these need: what is
// being checked is the translation, not what a share resolves to.
func emptyStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})
	return s
}

// The case every test above is the negative of: a whole-share read grant
// becomes a share with that account on its list.
func TestAWholeShareGrantBecomesAnAccountList(t *testing.T) {
	c, share := shareFixture(t)

	grants := []acl.Grant{
		{ID: 1, User: 7, Share: int64(share), Subpath: acl.NewPath(), Allow: acl.Read},
		{ID: 2, User: 8, Share: int64(share), Subpath: acl.NewPath(), Allow: acl.Read | acl.Write},
	}
	names := map[int64]string{7: "reader", 8: "writer"}

	defs, err := shareDefs(context.Background(), Deps{
		Core:   c,
		Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
		Names:  func(_ context.Context, id int64) (string, error) { return names[id], nil },
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("got %d shares, want the one with grants on it: %+v", len(defs), defs)
	}

	def := defs[0]
	if len(def.ValidUsers) != 2 {
		t.Errorf("the share admits %v, want both accounts", def.ValidUsers)
	}
	// Read and write are separate lists, because the difference is the whole
	// point of having two grants.
	if len(def.ReadList) != 1 || def.ReadList[0] != "reader" {
		t.Errorf("the read list is %v", def.ReadList)
	}
	if len(def.WriteList) != 1 || def.WriteList[0] != "writer" {
		t.Errorf("the write list is %v", def.WriteList)
	}
	if def.Path == "" {
		t.Error("the share names no path, so the daemon has nothing to serve")
	}

	// The same state renders the same file, so a republish that changed
	// nothing is byte identical and the sidecar's unchanged case happens.
	for range 4 {
		again, aerr := shareDefs(context.Background(), Deps{
			Core:   c,
			Grants: func(context.Context) ([]acl.Grant, error) { return grants, nil },
			Names:  func(_ context.Context, id int64) (string, error) { return names[id], nil },
		})
		if aerr != nil {
			t.Fatal(aerr)
		}
		if len(again) != 1 ||
			strings.Join(again[0].ValidUsers, ",") != strings.Join(def.ValidUsers, ",") {
			t.Fatal("the same grants rendered differently")
		}
	}
}

// shareFixture registers one share, so there is something for a grant to name.
func shareFixture(t *testing.T) (*core.Core, core.ShareID) {
	t.Helper()

	c, err := core.New(emptyStore(t), core.Options{ACL: acl.NewEvaluator()})
	if err != nil {
		t.Fatal(err)
	}
	host := t.TempDir()
	if rerr := c.RegisterShare(context.Background(), core.ShareDef{
		ID: 1, Name: "docs", Host: host, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering a share: %v", rerr)
	}
	return c, 1
}
