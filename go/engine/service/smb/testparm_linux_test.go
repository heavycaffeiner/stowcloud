//go:build linux

package smb_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
)

// The acceptance path for a configuration renderer is the parser that will read
// it. Everything else in this package's tests asserts on strings this package
// produced, which cannot tell a directive the daemon honours from one it drops
// on the floor.
//
// testparm is Samba's own parser and the same binary the agent validates with
// before promoting. These tests hand it the renderer's real output and ask what
// the daemon would actually do with it.

// testparmPath finds the real tool, or reports that this machine has none.

// testparmRun invokes the real parser against a configuration this test wrote.
//
// The gosec exception sits here rather than at each call site: the binary came
// from LookPath and the configuration is this test's own temporary file.
func testparmRun(tool string, args ...string) *exec.Cmd {
	return exec.Command(tool, args...) //nolint:gosec // the tool comes from LookPath and the conf is this test's own temp file.
}

func testparmPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("testparm")
	if err != nil {
		t.Skip("testparm is not installed, so the daemon's own parser cannot be asked")
	}
	return path
}

// render writes a rendered configuration and returns its path.
func render(t *testing.T, cfg smb.Config, shares []smb.ShareDef) string {
	t.Helper()
	body, _, err := smb.Render(cfg, shares)
	if err != nil {
		t.Fatalf("the renderer refused its own configuration: %v", err)
	}
	path := filepath.Join(t.TempDir(), "smb.conf")
	if werr := os.WriteFile(path, body, 0o600); werr != nil {
		t.Fatal(werr)
	}
	return path
}

// effective asks the parser what a directive resolves to once defaults are
// applied, which is what the daemon runs with.
func effective(t *testing.T, tool, conf, section, name string) string {
	t.Helper()
	args := []string{"-s", "--parameter-name", name}
	if section != "global" {
		args = append(args, "--section-name", section)
	}
	args = append(args, conf)

	out, err := testparmRun(tool, args...).Output()
	if err != nil {
		t.Fatalf("testparm could not read %s/%s: %v", section, name, err)
	}
	return strings.TrimSpace(string(out))
}

// dump returns the whole configuration as the daemon resolves it.
//
// Whole rather than one directive, because the question here is whether a
// directive survives at all, and asking for one the parser discarded is an
// error rather than an empty answer.
func dump(t *testing.T, tool, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hand.conf")
	if werr := os.WriteFile(path, []byte(body), 0o600); werr != nil {
		t.Fatal(werr)
	}
	out, err := testparmRun(tool, "-s", path).Output()
	if err != nil {
		t.Fatalf("testparm could not read the configuration: %v", err)
	}
	return string(out)
}

func sampleShares() []smb.ShareDef {
	return []smb.ShareDef{{
		Name: "docs", Path: "/srv/docs",
		ValidUsers: []string{"alice", "bob"},
		ReadList:   []string{"bob"},
		WriteList:  []string{"alice"},
		ModeFile:   0o664, ModeDir: 0o775,
	}}
}

func sampleConfig() smb.Config {
	return smb.Config{Enabled: true, Workgroup: "OFFICE", ServerName: "storage", ServiceUser: "scsvc"}
}

// The whole point of validating before promoting is that the daemon will accept
// the file. If the renderer can produce one testparm rejects, the agent refuses
// it and SMB keeps serving the previous configuration with no obvious cause.
func TestTheRenderedConfigurationParses(t *testing.T) {
	tool := testparmPath(t)
	conf := render(t, sampleConfig(), sampleShares())

	out, err := testparmRun(tool, "-s", conf).CombinedOutput()
	if err != nil {
		t.Fatalf("the daemon's parser rejected the rendered configuration: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Loaded services file OK") {
		t.Errorf("the parser did not report the file loaded:\n%s", out)
	}
}

// A configuration that parses is not the same as one that means what it says.
// Samba silently ignores an unknown directive, so a misspelled security setting
// produces a file that loads cleanly and protects nothing. This asks the parser
// for each directive's effective value.
func TestTheSecurityPostureIsWhatTheDaemonResolves(t *testing.T) {
	tool := testparmPath(t)
	conf := render(t, sampleConfig(), sampleShares())

	for _, c := range []struct{ section, name, want string }{
		// The transport. SMB3 is the parser's canonical spelling of the 3.1.1
		// dialect, which SMB3_00 remaining distinct confirms.
		{"global", "server min protocol", "SMB3"},
		{"global", "server signing", "required"},
		{"global", "smb encrypt", "required"},

		// Anonymous and guest access, which is what a share reachable without
		// a credential would come through.
		{"global", "restrict anonymous", "2"},
		{"global", "null passwords", "No"},
		{"global", "map to guest", "Never"},

		// The obsolete authentication protocols.
		{"global", "ntlm auth", "ntlmv2-only"},
		{"global", "lanman auth", "No"},
		{"global", "raw NTLMv2 auth", "No"},
		{"global", "unix extensions", "No"},

		// The network scope the agent later widens.
		{"global", "bind interfaces only", "Yes"},
		{"global", "interfaces", "lo"},
		{"global", "hosts allow", "127.0.0.0/8 ::1/128"},
		{"global", "hosts deny", "0.0.0.0/0"},
		{"global", "server multi channel support", "No"},

		// Coexistence with other programs writing the same trees.
		{"global", "store dos attributes", "No"},
		{"global", "disable spoolss", "Yes"},
		{"global", "force user", "scsvc"},

		// The share's own access lists and modes.
		{"docs", "path", "/srv/docs"},
		{"docs", "valid users", "alice bob"},
		{"docs", "read list", "bob"},
		{"docs", "write list", "alice"},
		{"docs", "create mask", "0664"},
		{"docs", "directory mask", "0775"},
		{"docs", "delete veto files", "No"},
	} {
		t.Run(c.section+"/"+c.name, func(t *testing.T) {
			if got := effective(t, tool, conf, c.section, c.name); !strings.EqualFold(got, c.want) {
				t.Errorf("the renderer asked for %q and the daemon resolves %q", c.want, got)
			}
		})
	}
}

// SMB3 is an alias rather than a downgrade. Without this, the test above would
// be asserting that a weaker dialect is acceptable.
func TestTheDialectAliasIsNotADowngrade(t *testing.T) {
	tool := testparmPath(t)
	dir := t.TempDir()

	resolve := func(value string) string {
		path := filepath.Join(dir, "probe.conf")
		body := "[global]\n  server min protocol = " + value + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return effective(t, tool, path, "global", "server min protocol")
	}

	// The renderer's value and the parser's canonical spelling agree.
	if resolve("SMB3_11") != resolve("SMB3") {
		t.Error("SMB3 is not the parser's spelling of SMB3_11")
	}
	// And an older 3.x dialect stays distinct, so the alias is not collapsing
	// the whole family onto one value.
	if got := resolve("SMB3_00"); strings.EqualFold(got, "SMB3") {
		t.Errorf("SMB3_00 resolved to %q, so the alias is hiding a downgrade", got)
	}
}

// The account lists are what stand between one account and another's files, so
// a share whose lists the daemon does not resolve is a share with no access
// control at all.
//
// Two halves. The daemon reads an empty list as no restriction, which is the
// fact that makes this dangerous; and the renderer never writes one, which is
// what keeps the fact harmless.
func TestAShareWithNoAccountListIsNotServed(t *testing.T) {
	tool := testparmPath(t)

	// Written by hand, because the renderer will not produce it. This is the
	// file that would open the share to every account that can authenticate:
	// the daemon does not read "valid users =" as an empty list, it discards
	// the line, and a share with no such line restricts nobody.
	handWritten := "[global]\n" +
		"  workgroup = WORKGROUP\n" +
		"  security = user\n" +
		"[open]\n" +
		"  path = /srv/open\n" +
		"  valid users =\n"
	dumped := dump(t, tool, handWritten)
	if strings.Contains(dumped, "valid users") {
		t.Fatalf("the daemon kept the empty account list, so the danger this guards is not what it was thought to be:\n%s", dumped)
	}

	// The renderer's answer to the same share: no block at all.
	out, res, err := smb.Render(sampleConfig(), []smb.ShareDef{{
		Name: "open", Path: "/srv/open", ModeFile: 0o664, ModeDir: 0o775,
	}})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if strings.Contains(string(out), "[open]") {
		t.Error("a share naming no account was published, which admits every account that can authenticate")
	}
	if len(res.Withheld) != 1 || res.Withheld[0].Name != "open" {
		t.Errorf("the withheld share was not reported: %+v", res.Withheld)
	}
}

// The renderer's refusals are the other half. A workgroup carrying a newline
// would open a section of the attacker's choosing, and the fuzzers cover the
// property; this confirms the one case end to end against the real parser, so
// the refusal is not merely this package disagreeing with itself.
func TestARefusedValueNeverReachesTheParser(t *testing.T) {
	testparmPath(t)

	_, _, err := smb.Render(smb.Config{
		Enabled:   true,
		Workgroup: "OFFICE\n[global]\n  guest ok = yes",
	}, sampleShares())

	if err == nil {
		t.Fatal("a workgroup that would open its own section was rendered")
	}
}
