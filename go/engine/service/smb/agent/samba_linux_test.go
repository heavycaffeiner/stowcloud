//go:build linux

package agent_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
)

// The agent's acceptance path is the pair of tools it actually drives:
// testparm, which it validates a candidate with before promoting, and pdbedit,
// which owns the credential database.
//
// The rest of this package's tests assert on strings the agent produced. They
// cannot tell a candidate the validator will accept from one that fails, which
// is the failure that keeps the previous configuration serving while the new
// one is silently refused.

func tool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed, so the agent's real validator cannot be asked", name)
	}
	return path
}

// serverRendered is what the server writes into the shared directory: the
// closed network case, which the agent is meant to widen.
func serverRendered(t *testing.T) string {
	t.Helper()
	body, _, err := smb.Render(
		smb.Config{Enabled: true, Workgroup: "OFFICE", ServerName: "storage", ServiceUser: "scsvc"},
		[]smb.ShareDef{{
			Name: "docs", Path: "/srv/docs",
			ValidUsers: []string{"alice"}, WriteList: []string{"alice"},
			ModeFile: 0o664, ModeDir: 0o775,
		}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// validate runs the agent's own validator over a file, exactly as the apply
// pass does before promoting.
func validate(t *testing.T, testparm, path string) (string, error) {
	t.Helper()
	out, err := exec.Command(testparm, "-s", path).CombinedOutput() //nolint:gosec // the tool comes from LookPath and the path is this test's temp file.
	return string(out), err
}

// The transformation the agent applies between reading and promoting has to
// produce something the validator accepts. If it does not, the apply refuses
// and SMB keeps serving whatever it served before, with the cause visible only
// in a log line.
func TestTheWidenedCandidateStillValidates(t *testing.T) {
	testparm := tool(t, "testparm")

	scope := &agent.Scope{
		Interfaces: "lo eth0",
		HostsAllow: "127.0.0.0/8 ::1/128 192.168.0.0/16",
		Detected:   true,
	}
	candidate := agent.Candidate(serverRendered(t), scope)

	path := filepath.Join(t.TempDir(), "smb.conf.candidate")
	if err := agent.WriteCandidate(path, candidate); err != nil {
		t.Fatal(err)
	}

	out, err := validate(t, testparm, path)
	if err != nil {
		t.Fatalf("the validator rejected the agent's own candidate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Loaded services file OK") {
		t.Errorf("the validator did not load the candidate:\n%s", out)
	}
}

// The widened scope has to reach the daemon as the scope, not merely appear in
// the file. A substituted line that landed in the wrong section would parse and
// bind nothing.
func TestTheWidenedScopeIsWhatTheDaemonResolves(t *testing.T) {
	testparm := tool(t, "testparm")

	scope := &agent.Scope{
		Interfaces: "lo eth0",
		HostsAllow: "127.0.0.0/8 ::1/128 192.168.0.0/16",
		Detected:   true,
	}
	path := filepath.Join(t.TempDir(), "smb.conf")
	if err := agent.Promote(path, agent.Candidate(serverRendered(t), scope)); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, want string }{
		{"interfaces", "lo eth0"},
		{"hosts allow", "127.0.0.0/8 ::1/128 192.168.0.0/16"},
		// Widening the scope must not disturb the posture around it.
		{"bind interfaces only", "Yes"},
		{"server min protocol", "SMB3"},
		{"smb encrypt", "required"},
	} {
		out, err := exec.Command(testparm, "-s", "--parameter-name", c.name, path).Output() //nolint:gosec // as above.
		if err != nil {
			t.Fatalf("testparm could not read %q: %v", c.name, err)
		}
		if got := strings.TrimSpace(string(out)); !strings.EqualFold(got, c.want) {
			t.Errorf("%s resolves to %q, want %q", c.name, got, c.want)
		}
	}
}

// The log directives are what make a failed authentication visible to the ban
// filter at all. They are inserted by the agent rather than the renderer.
//
// What the parser can confirm is narrower than it first appears. testparm
// reports only the base log level and echoes no per-class setting, and it
// accepts an invented class name without complaint, so it cannot answer whether
// auth_audit took effect. Both were checked directly: a configuration with the
// class and one without it both report "1", and a bogus class parses cleanly.
//
// So this asserts the two things that are observable: the directives survive
// into the promoted file, and the parser accepts the file carrying them. That
// the class produces the log line is a property of the daemon at runtime, and
// no smbd is installed here to observe it.
func TestTheAuditLogDirectivesSurviveIntoThePromotedFile(t *testing.T) {
	testparm := tool(t, "testparm")

	path := filepath.Join(t.TempDir(), "smb.conf")
	promoted := agent.Candidate(serverRendered(t), nil)
	if err := agent.Promote(path, promoted); err != nil {
		t.Fatal(err)
	}

	// auth_audit at three is the one level at which the daemon logs a failed
	// authentication at all, so its presence in the promoted bytes is the part
	// this test can hold.
	if !strings.Contains(promoted, "auth_audit:3") {
		t.Errorf("the audit class is not in the promoted configuration:\n%s", promoted)
	}
	if !strings.Contains(promoted, "log file =") {
		t.Errorf("the log file directive is not in the promoted configuration:\n%s", promoted)
	}

	// And the parser accepts the file with them in it, which is what the apply
	// pass requires before promoting.
	if out, err := validate(t, testparm, path); err != nil {
		t.Fatalf("the validator rejected a configuration carrying the log directives: %v\n%s", err, out)
	}
}

// The read-backs report what the daemon serves, so they have to agree with what
// the daemon's own parser reports.
func TestTheReadBacksAgreeWithTheParser(t *testing.T) {
	testparm := tool(t, "testparm")

	promoted := agent.Candidate(serverRendered(t), &agent.Scope{
		Interfaces: "lo eth0", HostsAllow: "127.0.0.0/8", Detected: true,
	})
	path := filepath.Join(t.TempDir(), "smb.conf")
	if err := agent.Promote(path, promoted); err != nil {
		t.Fatal(err)
	}

	// The share list the agent reports.
	sections := agent.Sections(promoted)
	if len(sections) != 1 || sections[0].Name != "docs" || sections[0].Path != "/srv/docs" {
		t.Fatalf("the agent reads the shares as %+v", sections)
	}
	// The same question, asked of the parser.
	out, err := exec.Command(testparm, "-s", "--section-name", "docs", "--parameter-name", "path", path).Output() //nolint:gosec // as above.
	if err != nil {
		t.Fatalf("the parser does not know the share the agent reported: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != sections[0].Path {
		t.Errorf("the agent reports path %q and the parser resolves %q", sections[0].Path, got)
	}

	// The bind line, which is the one thing a reload cannot change.
	bound := agent.BoundInterfaces(promoted)
	iout, ierr := exec.Command(testparm, "-s", "--parameter-name", "interfaces", path).Output() //nolint:gosec // as above.
	if ierr != nil {
		t.Fatal(ierr)
	}
	if got := strings.TrimSpace(string(iout)); got != bound {
		t.Errorf("the agent reads the bind line as %q and the parser resolves %q", bound, got)
	}
}

// The account file the server renders has to be one the daemon's own account
// lookup can read. A field out of place breaks every account after it, and only
// a real reader can say whether the format is right.
func TestTheRenderedAccountFileIsWellFormed(t *testing.T) {
	entries, err := smb.PasswdEntries([]smb.User{
		{Name: "alice", Uid: 3001},
		{Name: "bob", Uid: 3002},
	}, 2000)
	if err != nil {
		t.Fatal(err)
	}

	parsed := agent.ParseRendered(string(entries))
	if len(parsed) != 2 {
		t.Fatalf("the agent parsed %d entries out of the server's own render", len(parsed))
	}

	// Seven colon-separated fields is what the libc reader expects, and the
	// numeric fields have to be numbers.
	for _, line := range strings.Split(strings.TrimSpace(string(entries)), "\n") {
		f := strings.Split(line, ":")
		if len(f) != 7 {
			t.Errorf("a rendered line has %d fields, want 7: %q", len(f), line)
			continue
		}
		for _, i := range []int{2, 3} {
			if strings.TrimSpace(f[i]) == "" {
				t.Errorf("field %d is empty, which breaks the lookup: %q", i, line)
			}
		}
		// The shell has to exist as a path, or the account cannot be used even
		// where the lookup succeeds.
		if !strings.HasPrefix(f[6], "/") {
			t.Errorf("the shell field is not a path: %q", line)
		}
	}
}

// pdbedit is what the agent imports credentials with, and it is the one part of
// this path that cannot be exercised here.
//
// Two constraints were measured rather than assumed. Creating a tdbsam database
// fails as an unprivileged user with "failed to grab mutex", and the importer
// refuses any account absent from the real system passwd database, which only
// root can add to. So the import, the prune and the missing-credential check
// reach the real tool but cannot reach a real database on this machine.
//
// What is asserted instead is the parsing this package owns: the listing parser
// is fed the tool's real output shape and must not invent an account. Running
// the tool is attempted first, so this reports the constraint rather than
// quietly asserting less.
func TestThePassdbListingParserHandlesTheRealOutputShape(t *testing.T) {
	tool(t, "pdbedit")

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// The attempt is what establishes the constraint is still real rather than
	// something that was fixed and left asserted.
	if _, err := agent.PassdbNames(context.Background()); err == nil {
		t.Log("pdbedit listed successfully on this machine, so the database path is reachable here")
	} else {
		t.Logf("the credential database is unreachable unprivileged, as measured: %v", err)
	}

	// pdbedit -L prints one account per line as "name:uid:comment". The parser
	// takes the field before the first colon and drops blank lines.
	for _, c := range []struct {
		name string
		out  string
		want []string
	}{
		{"one account", "alice:3001:Alice\n", []string{"alice"}},
		{"several", "alice:3001:\nbob:3002:\n", []string{"alice", "bob"}},
		{"trailing blank line", "alice:3001:\n\n", []string{"alice"}},
		{"empty database", "", nil},
		{"whitespace only", "   \n\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := agent.ParsePassdbListing(c.out)
			if len(got) != len(c.want) {
				t.Fatalf("parsed %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("parsed %v, want %v", got, c.want)
				}
			}
		})
	}
}
