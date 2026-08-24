package smbagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rendered = `[global]
  workgroup = WORKGROUP
  disable netbios = yes
  bind interfaces only = yes
  interfaces = lo
  hosts allow = 127.0.0.0/8 ::1/128
  hosts deny = 0.0.0.0/0

[Share]
  path = /Tokyo/Share
  valid users = heavycaffeiner
`

func testScope() *Scope {
	return &Scope{
		Interfaces: "lo eth0",
		HostsAllow: "127.0.0.0/8 ::1/128 192.168.0.0/16",
		Detected:   true,
	}
}

func TestLoggingLandsInsideTheGlobalSection(t *testing.T) {
	lines := strings.Split(Candidate(rendered, nil), "\n")
	want := []string{"[global]", "  log file = /var/log/samba/log.smbd", "  log level = 1 auth_audit:3"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

func TestTheScopeLinesAreReplacedAndNothingElseIs(t *testing.T) {
	out := Candidate(rendered, testScope())
	for _, want := range []string{
		"  interfaces = lo eth0\n",
		"  hosts allow = 127.0.0.0/8 ::1/128 192.168.0.0/16\n",
		"  hosts deny = 0.0.0.0/0\n",
		"  workgroup = WORKGROUP\n",
		// The directive whose name contains another directive's name. It is
		// not the one being substituted, and rewriting it would turn a
		// "bind interfaces only" line into a second bind line.
		"  bind interfaces only = yes\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the candidate is missing %q", want)
		}
	}
	if n := strings.Count(out, "\n  interfaces ="); n != 1 {
		t.Errorf("the candidate carries %d bind lines, want exactly one", n)
	}
}

// A pin means the server already wrote the final answer.
func TestNoScopeLeavesTheRenderedLinesAlone(t *testing.T) {
	out := Candidate(rendered, nil)
	if !strings.Contains(out, "  interfaces = lo\n") {
		t.Error("the pinned bind line was rewritten")
	}
	if !strings.Contains(out, "  hosts allow = 127.0.0.0/8 ::1/128\n") {
		t.Error("the pinned admission line was rewritten")
	}
}

func TestSectionsCarryTheirPathsAndSkipTheGlobalOne(t *testing.T) {
	s := Sections(rendered)
	if len(s) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(s), s)
	}
	if s[0].Name != "Share" || s[0].Path != "/Tokyo/Share" {
		t.Errorf("section = %+v", s[0])
	}
}

// A comment that happens to contain a directive's name is not that directive.
func TestACommentedDirectiveIsNotOne(t *testing.T) {
	conf := "[global]\n  # interfaces = lo eth9\n  interfaces = lo\n"
	if got := BoundInterfaces(conf); got != "lo" {
		t.Fatalf("bound interfaces = %q, want the real line", got)
	}
}

func TestNetbiosFollowsThePromotedFile(t *testing.T) {
	if NetbiosWanted(rendered) {
		t.Error("a configuration that disables the name service wants it running")
	}
	if !NetbiosWanted("[global]\n  disable netbios = no\n") {
		t.Error("a configuration that enables the name service does not want it")
	}
	// Absent means the server rendered neither, which only happens for a
	// configuration this agent did not produce.
	if NetbiosWanted("[global]\n") {
		t.Error("an unrelated configuration wants the name service running")
	}
}

func TestPolicyDefaultsClosedWhenTheFileIsMissing(t *testing.T) {
	p := ReadPolicy(filepath.Join(t.TempDir(), "network.policy"))
	if p.AllowPublicBind || p.PinnedInterfaces {
		t.Fatalf("a missing policy read as %+v, want both closed", p)
	}
}

func TestPolicyReadsBothFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.policy")
	body := "# comment\nallow_public_bind=1\npinned_interfaces=0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p := ReadPolicy(path)
	if !p.AllowPublicBind {
		t.Error("the set flag did not read as set")
	}
	if p.PinnedInterfaces {
		t.Error("a flag set to zero read as set")
	}
}

// The candidate is built from the rendered file and nothing else, so building
// it twice gives the same bytes and a promotion that changed nothing is
// visibly a no-op.
func TestTheCandidateIsDeterministic(t *testing.T) {
	first := Candidate(rendered, testScope())
	for range 8 {
		if Candidate(rendered, testScope()) != first {
			t.Fatal("the same input rendered differently")
		}
	}
}
