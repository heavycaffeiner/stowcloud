//go:build linux

package agent

import (
	"strings"
	"testing"
)

// A configuration shaped as the server renders it: the global header first,
// both scope lines closed, then the shares.
const rendered = `[global]
  workgroup = OFFICE
  interfaces = lo
  hosts allow = 127.0.0.0/8 ::1/128
  disable netbios = yes
  server min protocol = SMB3_11

[docs]
  path = /srv/docs
  valid users = alice

[photos]
  path = /srv/photos
  valid users = alice bob
`

func widened() *Scope {
	return &Scope{Interfaces: "lo eth0", HostsAllow: "127.0.0.0/8 ::1/128 192.168.0.0/16", Detected: true}
}

// The scope lines are substituted in place, never appended. The server always
// renders both, so a file missing them is one the agent should not widen, and
// appending would leave the closed line still in the file ahead of the open one.
func TestTheScopeLinesAreSubstitutedInPlace(t *testing.T) {
	out := Candidate(rendered, widened())

	if strings.Count(out, "interfaces =") != 1 {
		t.Errorf("the bind line was appended rather than substituted:\n%s", out)
	}
	if strings.Count(out, "hosts allow =") != 1 {
		t.Errorf("the admission line was appended rather than substituted:\n%s", out)
	}
	if !strings.Contains(out, "interfaces = lo eth0") {
		t.Errorf("the widened bind line is missing:\n%s", out)
	}
	if !strings.Contains(out, "hosts allow = 127.0.0.0/8 ::1/128 192.168.0.0/16") {
		t.Errorf("the widened admission line is missing:\n%s", out)
	}
	// The closed values are gone, not merely outranked.
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "interfaces = lo" {
			t.Errorf("the closed bind line survived:\n%s", out)
		}
	}
}

// The substituted lines keep their position, so a directive belonging to the
// global section does not end up under a share.
func TestTheSubstitutedLinesStayInTheGlobalSection(t *testing.T) {
	out := Candidate(rendered, widened())

	bind := strings.Index(out, "interfaces = lo eth0")
	firstShare := strings.Index(out, "[docs]")
	if bind < 0 || firstShare < 0 {
		t.Fatalf("the candidate is not shaped as expected:\n%s", out)
	}
	if bind > firstShare {
		t.Errorf("the bind line landed under a share rather than in the global section:\n%s", out)
	}
}

// A file without the scope lines is one the agent should not be widening, so
// nothing is inserted.
func TestAFileWithoutScopeLinesIsNotWidened(t *testing.T) {
	src := "[global]\n  workgroup = OFFICE\n\n[docs]\n  path = /srv/docs\n"
	out := Candidate(src, widened())

	if strings.Contains(out, "interfaces =") {
		t.Errorf("a bind line was inserted into a file that had none:\n%s", out)
	}
	if strings.Contains(out, "hosts allow =") {
		t.Errorf("an admission line was inserted into a file that had none:\n%s", out)
	}
}

// The log directives land inside the global section, which is what makes the
// ban filter able to see a failed authentication at all.
func TestTheLogDirectivesLandInsideTheGlobalSection(t *testing.T) {
	out := Candidate(rendered, widened())

	level := strings.Index(out, "log level =")
	firstShare := strings.Index(out, "[docs]")
	if level < 0 {
		t.Fatalf("the log directives are missing:\n%s", out)
	}
	if level > firstShare {
		t.Errorf("the log directives landed under a share:\n%s", out)
	}
	// auth_audit at three is the one level at which a failed authentication is
	// logged at all.
	if !strings.Contains(out, "auth_audit:3") {
		t.Errorf("the audit level is not set:\n%s", out)
	}
}

// A pinned policy means the operator named the addresses, so detection must
// change nothing but the log directives.
func TestANilScopeChangesNothingButTheLogDirectives(t *testing.T) {
	out := Candidate(rendered, nil)

	if !strings.Contains(out, "interfaces = lo\n") {
		t.Errorf("a pinned configuration's bind line was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "hosts allow = 127.0.0.0/8 ::1/128\n") {
		t.Errorf("a pinned configuration's admission line was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "log level =") {
		t.Errorf("the log directives are missing:\n%s", out)
	}

	// Everything else is byte identical, which is what "changes nothing" means.
	withoutLogs := strings.ReplaceAll(out, logDirectives, "")
	if withoutLogs != rendered {
		t.Errorf("a pinned candidate differs beyond the log directives:\n%s", withoutLogs)
	}
}

// A comment mentioning a directive is not a directive.
func TestACommentedDirectiveIsNotADirective(t *testing.T) {
	src := "[global]\n" +
		"  # interfaces = everything\n" +
		"  ; hosts allow = ALL\n" +
		"  interfaces = lo\n" +
		"  hosts allow = 127.0.0.0/8\n"

	out := Candidate(src, widened())

	if strings.Contains(out, "# interfaces = lo eth0") {
		t.Errorf("a comment was rewritten as a directive:\n%s", out)
	}
	if !strings.Contains(out, "# interfaces = everything") {
		t.Errorf("the comment was lost:\n%s", out)
	}
	if strings.Count(out, "interfaces = lo eth0") != 1 {
		t.Errorf("the real directive was not substituted exactly once:\n%s", out)
	}
}

// Sections reports what the daemon serves, which is read back from the promoted
// file rather than tracked beside it.
func TestSectionsPairsNamesWithPathsAndSkipsGlobal(t *testing.T) {
	got := Sections(rendered)

	if len(got) != 2 {
		t.Fatalf("read %d sections, want the two shares: %+v", len(got), got)
	}
	if got[0].Name != "docs" || got[0].Path != "/srv/docs" {
		t.Errorf("the first section is %+v", got[0])
	}
	if got[1].Name != "photos" || got[1].Path != "/srv/photos" {
		t.Errorf("the second section is %+v", got[1])
	}
	for _, s := range got {
		if strings.EqualFold(s.Name, "global") {
			t.Error("the global section was reported as a share")
		}
	}
}

// A share with no path is still a share the daemon serves, and reporting it
// with an empty path is what makes the missing-path check able to see it.
func TestASectionWithoutAPathIsStillReported(t *testing.T) {
	got := Sections("[global]\n\n[broken]\n  valid users = alice\n")
	if len(got) != 1 || got[0].Name != "broken" || got[0].Path != "" {
		t.Errorf("a pathless share read as %+v", got)
	}
}

// The name service runs only when the promoted configuration wants a name.
func TestNetbiosWantedRequiresThePresentAndEnabledDirective(t *testing.T) {
	for _, c := range []struct {
		name string
		conf string
		want bool
	}{
		{"absent", "[global]\n  workgroup = OFFICE\n", false},
		{"disabled", "[global]\n  disable netbios = yes\n", false},
		{"disabled true", "[global]\n  disable netbios = true\n", false},
		{"disabled one", "[global]\n  disable netbios = 1\n", false},
		{"enabled", "[global]\n  disable netbios = no\n", true},
		{"commented", "[global]\n  # disable netbios = no\n", false},
		{"last wins", "[global]\n  disable netbios = no\n  disable netbios = yes\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := NetbiosWanted(c.conf); got != c.want {
				t.Errorf("NetbiosWanted = %v, want %v for:\n%s", got, c.want, c.conf)
			}
		})
	}
}

// The bind line of the promoted file is the one thing a reload cannot change,
// so it is read back rather than remembered.
func TestBoundInterfacesReadsThePromotedLine(t *testing.T) {
	promoted := Candidate(rendered, widened())

	if got := BoundInterfaces(promoted); got != "lo eth0" {
		t.Errorf("BoundInterfaces = %q", got)
	}
	if got := HostsAllowOf(promoted); got != "127.0.0.0/8 ::1/128 192.168.0.0/16" {
		t.Errorf("HostsAllowOf = %q", got)
	}
	// A configuration with no bind line reads as empty rather than as
	// something that happened to be nearby.
	if got := BoundInterfaces("[global]\n  workgroup = OFFICE\n"); got != "" {
		t.Errorf("a configuration with no bind line read as %q", got)
	}
}

// The transformation is stable: running it twice over its own output changes
// only what it is supposed to change.
func TestTheCandidateIsDeterministic(t *testing.T) {
	first := Candidate(rendered, widened())
	second := Candidate(rendered, widened())
	if first != second {
		t.Error("the same input produced two different candidates")
	}
}
