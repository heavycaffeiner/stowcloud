//go:build linux

package publish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
)

// stubAccounts records what the credential half was asked for.
type stubAccounts struct {
	passwdPath string
	passwdGID  uint32
	passwdErr  error
	passdbErr  error
	passdbRan  bool
}

func (s *stubAccounts) PublishPasswdEntries(_ context.Context, path string, gid uint32) error {
	s.passwdPath, s.passwdGID = path, gid
	if s.passwdErr != nil {
		return s.passwdErr
	}
	return os.WriteFile(path, []byte("svc:x:1000:1000:::\n"), 0o600)
}

func (s *stubAccounts) PublishPassdb(context.Context) error {
	s.passdbRan = true
	return s.passdbErr
}

// deps builds a publish over a temp directory with one share and one grant.
func deps(t *testing.T, shares []Share, grants []Grant) (Deps, *stubAccounts) {
	t.Helper()
	acc := &stubAccounts{}
	return Deps{
		Shares:   func() []Share { return shares },
		Accounts: acc,
		Grants: func(context.Context) ([]Grant, error) {
			return grants, nil
		},
		Names: func(_ context.Context, id int64) (string, error) {
			switch id {
			case 1:
				return "alice", nil
			case 2:
				return "bob", nil
			}
			return "", errors.New("no such account")
		},
		ConfigDir:  t.TempDir(),
		ServiceGID: 2000,
	}, acc
}

func oneShare() []Share {
	return []Share{{ID: 7, Name: "docs", Path: "/srv/docs", ModeFile: 0o664, ModeDir: 0o775}}
}

func enabled() smb.Config {
	return smb.Config{Enabled: true, Workgroup: "OFFICE", ServiceUser: "scsvc"}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The whole round trip: files land, the credential half runs, a report returns.
func TestPublishWritesTheSetAndReports(t *testing.T) {
	d, acc := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true, AllowWrite: true},
	})

	report, err := Publish(t.Context(), d, enabled())
	if err != nil {
		t.Fatal(err)
	}
	// No socket configured is a bare-metal deployment, which is legitimate.
	if !report.OK || report.Smbd != agent.ActionUnchanged {
		t.Errorf("the report is %+v", report)
	}

	conf := read(t, d.ConfigDir, fileConf)
	if !strings.Contains(conf, "[docs]") {
		t.Errorf("the share is missing from the configuration:\n%s", conf)
	}
	if !strings.Contains(conf, "alice") {
		t.Errorf("the account list is missing:\n%s", conf)
	}
	if _, serr := os.Stat(filepath.Join(d.ConfigDir, filePolicy)); serr != nil {
		t.Errorf("the policy file is missing: %v", serr)
	}
	if acc.passwdGID != 2000 {
		t.Errorf("the account file was published with gid %d, want the configured one", acc.passwdGID)
	}
	if !acc.passdbRan {
		t.Error("the credentials were never published")
	}
}

// The deny rule, which is the requirement this package states rather than
// approximates. A user holding any deny grant on a share is absent from that
// share's lists entirely, even where the deny covers nothing they could
// otherwise reach.
func TestADenyGrantRemovesTheUserFromTheShare(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true, AllowWrite: true},
		{User: 1, Share: 7, WholeShare: true, Denies: true},
		{User: 2, Share: 7, WholeShare: true, AllowRead: true},
	})

	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	conf := read(t, d.ConfigDir, fileConf)

	if strings.Contains(conf, "alice") {
		t.Errorf("a denied account survived into the rendered share:\n%s", conf)
	}
	if !strings.Contains(conf, "bob") {
		t.Errorf("an undenied account was dropped:\n%s", conf)
	}
}

// The order the grants arrive in must not decide the answer. A deny read after
// an allow has to remove the name that allow already added.
func TestTheDenyRuleIsOrderIndependent(t *testing.T) {
	forward := []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
		{User: 1, Share: 7, WholeShare: true, Denies: true},
	}
	reverse := []Grant{forward[1], forward[0]}

	for _, c := range []struct {
		name   string
		grants []Grant
	}{{"allow then deny", forward}, {"deny then allow", reverse}} {
		t.Run(c.name, func(t *testing.T) {
			d, _ := deps(t, oneShare(), c.grants)
			if _, err := Publish(t.Context(), d, enabled()); err != nil {
				t.Fatal(err)
			}
			// The share has no admitted account left, so it is omitted
			// entirely rather than rendered with an empty list, which in this
			// format means everyone.
			conf := read(t, d.ConfigDir, fileConf)
			if strings.Contains(conf, "alice") {
				t.Errorf("the denied account survived:\n%s", conf)
			}
			if strings.Contains(conf, "[docs]") {
				t.Errorf("a share with no admitted account was rendered:\n%s", conf)
			}
		})
	}
}

// A name must not appear on both the read and the write list. Two grants, one
// read-only and one writable, describe a writer.
func TestAWriterIsNotAlsoOnTheReadList(t *testing.T) {
	for _, c := range []struct {
		name   string
		grants []Grant
	}{
		{"read then write", []Grant{
			{User: 1, Share: 7, WholeShare: true, AllowRead: true},
			{User: 1, Share: 7, WholeShare: true, AllowRead: true, AllowWrite: true},
		}},
		{"write then read", []Grant{
			{User: 1, Share: 7, WholeShare: true, AllowRead: true, AllowWrite: true},
			{User: 1, Share: 7, WholeShare: true, AllowRead: true},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, _ := deps(t, oneShare(), c.grants)
			if _, err := Publish(t.Context(), d, enabled()); err != nil {
				t.Fatal(err)
			}
			conf := read(t, d.ConfigDir, fileConf)

			var readLine, writeLine string
			for _, line := range strings.Split(conf, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "read list") {
					readLine = trimmed
				}
				if strings.HasPrefix(trimmed, "write list") {
					writeLine = trimmed
				}
			}
			if !strings.Contains(writeLine, "alice") {
				t.Errorf("the writer is not on the write list: %q", writeLine)
			}
			if strings.Contains(readLine, "alice") {
				t.Errorf("the writer is also on the read list, which contradicts it: %q", readLine)
			}
		})
	}
}

// A subpath grant cannot be expressed in this format, so it grants nothing
// here rather than the whole share.
func TestASubpathGrantDoesNotGrantTheShare(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: false, AllowRead: true, AllowWrite: true},
	})
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	conf := read(t, d.ConfigDir, fileConf)
	if strings.Contains(conf, "[docs]") {
		t.Errorf("a subpath grant rendered a whole share:\n%s", conf)
	}
}

// A share nobody holds a grant on is omitted. An empty account list in this
// format means every account, so rendering one would publish a private share.
func TestAShareWithNoGrantIsOmitted(t *testing.T) {
	d, _ := deps(t, oneShare(), nil)
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	conf := read(t, d.ConfigDir, fileConf)
	if strings.Contains(conf, "[docs]") {
		t.Errorf("a share with no grant was rendered:\n%s", conf)
	}
}

// Missing dependencies render nothing rather than everything.
func TestMissingDepsRenderNoShares(t *testing.T) {
	base, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})

	for _, c := range []struct {
		name string
		mut  func(*Deps)
	}{
		{"no grants reader", func(d *Deps) { d.Grants = nil }},
		{"no name resolver", func(d *Deps) { d.Names = nil }},
		{"no share list", func(d *Deps) { d.Shares = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := base
			d.ConfigDir = t.TempDir()
			c.mut(&d)
			if _, err := Publish(t.Context(), d, enabled()); err != nil {
				t.Fatal(err)
			}
			if conf := read(t, d.ConfigDir, fileConf); strings.Contains(conf, "[docs]") {
				t.Errorf("a share was rendered with a dependency missing:\n%s", conf)
			}
		})
	}
}

// Accounts whose names do not resolve are skipped rather than written with an
// empty name the daemon could not look up.
func TestAnUnresolvableAccountIsSkipped(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 99, Share: 7, WholeShare: true, AllowRead: true},
	})
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	conf := read(t, d.ConfigDir, fileConf)
	if strings.Contains(conf, "[docs]") {
		t.Errorf("an unresolvable account produced a share:\n%s", conf)
	}
}

// The policy file carries the two flags the agent reads, and carries neither
// when neither applies.
func TestThePolicyFileCarriesTheFlags(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  smb.Config
		want []string
		none bool
	}{
		{name: "neither", cfg: enabled(), none: true},
		{
			name: "public bind",
			cfg:  smb.Config{Enabled: true, Workgroup: "OFFICE", AllowPublicBind: true},
			want: []string{"allow_public_bind=1"},
		},
		{
			name: "pinned",
			cfg:  smb.Config{Enabled: true, Workgroup: "OFFICE", Interfaces: []string{"192.168.1.10"}},
			want: []string{"pinned_interfaces=1"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, _ := deps(t, nil, nil)
			if _, err := Publish(t.Context(), d, c.cfg); err != nil {
				t.Fatal(err)
			}
			body := read(t, d.ConfigDir, filePolicy)
			if c.none && strings.TrimSpace(body) != "" {
				t.Errorf("the policy file is not empty: %q", body)
			}
			for _, want := range c.want {
				if !strings.Contains(body, want) {
					t.Errorf("the policy file lacks %q: %q", want, body)
				}
			}
		})
	}
}

// Disabling removes the whole set, which is how the agent learns SMB is off.
// A file left behind would keep a revoked credential working.
func TestDisableRemovesEveryFile(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	// The passdb file is the auth half's; create it so the removal has
	// something to take.
	if err := os.WriteFile(filepath.Join(d.ConfigDir, filePassdb), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Publish(t.Context(), d, smb.Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileConf, filePolicy, filePasswd, filePassdb} {
		if _, err := os.Stat(filepath.Join(d.ConfigDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the disable", name)
		}
	}
}

// Disabling a deployment that never published is not an error: the files are
// already absent, which is the state being asked for.
func TestDisableToleratesAbsentFiles(t *testing.T) {
	d, _ := deps(t, nil, nil)
	if _, err := Disable(t.Context(), d); err != nil {
		t.Errorf("disabling an unpublished deployment failed: %v", err)
	}
}

// Every removal is attempted before a failure is reported, so one unremovable
// file does not leave the rest of the set in place.
func TestDisableReportsPartialFailureAfterTryingEverything(t *testing.T) {
	d, _ := deps(t, nil, nil)

	// A directory where a file belongs cannot be removed by os.Remove once it
	// has an entry inside it, which is the unremovable case without needing a
	// permission trick that root would defeat.
	stuck := filepath.Join(d.ConfigDir, fileConf)
	if err := os.MkdirAll(filepath.Join(stuck, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filePolicy, filePasswd, filePassdb} {
		if err := os.WriteFile(filepath.Join(d.ConfigDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := Disable(t.Context(), d)
	if err == nil {
		t.Fatal("an unremovable file was not reported")
	}
	if !strings.Contains(err.Error(), fileConf) {
		t.Errorf("the failure does not name the file: %v", err)
	}
	// The others still went, which is the point of joining rather than
	// returning at the first failure.
	for _, name := range []string{filePolicy, filePasswd, filePassdb} {
		if _, serr := os.Stat(filepath.Join(d.ConfigDir, name)); !os.IsNotExist(serr) {
			t.Errorf("%s survived a disable that failed on another file", name)
		}
	}
}

// A render the configuration cannot pass writes nothing, so the agent never
// sees half a configuration.
func TestARefusedRenderWritesNothing(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})
	bad := smb.Config{Enabled: true, Workgroup: "BAD\nGROUP\n[global]"}

	if _, err := Publish(t.Context(), d, bad); err == nil {
		t.Fatal("an unrenderable configuration was published")
	}
	if _, err := os.Stat(filepath.Join(d.ConfigDir, fileConf)); !os.IsNotExist(err) {
		t.Error("a refused render still wrote the configuration file")
	}
}

// A credential publisher that fails stops the publish, because a configuration
// naming accounts with no credentials is a login that fails as an unknown user.
func TestACredentialFailureStopsThePublish(t *testing.T) {
	d, acc := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})
	acc.passdbErr = errors.New("the key is not available")

	if _, err := Publish(t.Context(), d, enabled()); err == nil {
		t.Fatal("a failed credential publish was reported as success")
	}
}

// The agent's failure is reported as what it is: the files are written and the
// answer is missing, not the configuration lost.
func TestAnUnreachableAgentSaysTheFilesAreWritten(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})
	d.Socket = filepath.Join(t.TempDir(), "absent.sock")

	_, err := Publish(t.Context(), d, enabled())
	if err == nil {
		t.Fatal("an unreachable agent was reported as success")
	}
	if !strings.Contains(err.Error(), "written") {
		t.Errorf("the failure does not say the files landed: %v", err)
	}
	// And they did.
	if _, serr := os.Stat(filepath.Join(d.ConfigDir, fileConf)); serr != nil {
		t.Errorf("the configuration was not written: %v", serr)
	}
}

// The same state renders the same bytes, which is what makes the agent's
// unchanged case reachable rather than a restart on every publish.
func TestPublishingTwiceRendersIdenticalBytes(t *testing.T) {
	shares := []Share{
		{ID: 9, Name: "zeta", Path: "/srv/z", ModeFile: 0o664, ModeDir: 0o775},
		{ID: 7, Name: "alpha", Path: "/srv/a", ModeFile: 0o664, ModeDir: 0o775},
	}
	grants := []Grant{
		{User: 2, Share: 9, WholeShare: true, AllowRead: true},
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
		{User: 2, Share: 7, WholeShare: true, AllowRead: true, AllowWrite: true},
	}

	d, _ := deps(t, shares, grants)
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	first := read(t, d.ConfigDir, fileConf)

	// Same state, grants in a different order.
	slices.Reverse(grants)
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	if second := read(t, d.ConfigDir, fileConf); second != first {
		t.Errorf("the same state rendered different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// A zero service gid must not reach a rendered file as root's group.
func TestAnUnsetServiceGIDTakesTheDefault(t *testing.T) {
	d, acc := deps(t, nil, nil)
	d.ServiceGID = 0

	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	if acc.passwdGID == 0 {
		t.Error("the account file was published with root's group")
	}
	if acc.passwdGID != defaultServiceGID {
		t.Errorf("the gid is %d, want the compiled-in default", acc.passwdGID)
	}
}

// A publish over an existing set replaces it rather than appending, and the
// sidecar polling the directory never reads a partial file.
func TestRepublishReplacesTheConfiguration(t *testing.T) {
	d, _ := deps(t, oneShare(), []Grant{
		{User: 1, Share: 7, WholeShare: true, AllowRead: true},
	})
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	first := read(t, d.ConfigDir, fileConf)
	if !strings.Contains(first, "alice") {
		t.Fatalf("the first publish is wrong:\n%s", first)
	}

	// The share loses its only grant, so it disappears.
	d.Grants = func(context.Context) ([]Grant, error) { return nil, nil }
	if _, err := Publish(t.Context(), d, enabled()); err != nil {
		t.Fatal(err)
	}
	second := read(t, d.ConfigDir, fileConf)
	if strings.Contains(second, "alice") {
		t.Errorf("the republish appended to the old file rather than replacing it:\n%s", second)
	}
}
