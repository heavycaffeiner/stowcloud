package smbagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const renderedAccounts = "alice:x:2001:1000::/nonexistent:/sbin/nologin\n" +
	"bob:x:2002:1000::/nonexistent:/sbin/nologin\n"

func TestRenderedEntriesGetTheMarker(t *testing.T) {
	e := ParseRendered(renderedAccounts)
	if len(e) != 2 {
		t.Fatalf("got %d entries, want 2", len(e))
	}
	if e[0].Name != "alice" || e[0].UID != "2001" || e[0].GID != "1000" {
		t.Errorf("entry = %+v", e[0])
	}
	if !strings.Contains(e[0].Line, managedMarker) {
		t.Errorf("the line carries no marker: %q", e[0].Line)
	}
}

// A malformed line never reaches the system account file: one there breaks the
// name lookup for every account after it.
func TestAMalformedLineNeverReachesTheAccountFile(t *testing.T) {
	e := ParseRendered("garbage\n:x:1:1::/:/bin/sh\nbob:x:2002:1000::/nonexistent:/sbin/nologin\n")
	if len(e) != 1 || e[0].Name != "bob" {
		t.Fatalf("got %+v, want only the well-formed line", e)
	}
}

// The property the whole marker scheme exists for: an account dropped from the
// registry disappears rather than accumulating.
func TestRebuildingDropsManagedAccountsThatAreGone(t *testing.T) {
	base := "root:x:0:0:root:/root:/bin/bash\nscsvc:x:1000:1000::/:/sbin/nologin\n"

	first := Rebuild(base, ParseRendered(renderedAccounts))
	if !strings.Contains(first, "alice") || !strings.Contains(first, "bob") {
		t.Fatalf("the first pass is missing an account:\n%s", first)
	}

	// bob leaves the registry.
	second := Rebuild(first, ParseRendered("alice:x:2001:1000::/nonexistent:/sbin/nologin\n"))
	if !strings.Contains(second, "alice") {
		t.Error("the remaining account was dropped")
	}
	if strings.Contains(second, "bob") {
		t.Error("an account removed from the registry survived the rebuild")
	}
	for _, keep := range []string{"root:x:0:0", "scsvc:x:1000"} {
		if !strings.Contains(second, keep) {
			t.Errorf("a system account this agent did not create was dropped: %s", keep)
		}
	}
}

func TestANameCollisionWithARealAccountIsRefused(t *testing.T) {
	desired := ParseRendered("alice:x:2001:1000::/nonexistent:/sbin/nologin\n")
	existing := "alice:x:1001:1001:a real person:/home/alice:/bin/bash\n"
	c := Collisions(desired, existing)
	if !anyContains(c, "did not create") {
		t.Fatalf("collisions = %v, want the existing account refused", c)
	}
}

// The expensive one: the import tool resolves by number, so a shared number
// silently attaches the credential to the wrong name.
func TestAUserIdCollisionWithARealAccountIsRefused(t *testing.T) {
	desired := ParseRendered("alice:x:1000:1000::/nonexistent:/sbin/nologin\n")
	existing := "scsvc:x:1000:1000::/:/sbin/nologin\n"
	c := Collisions(desired, existing)
	if !anyContains(c, "user id 1000") {
		t.Fatalf("collisions = %v, want the shared number refused", c)
	}
}

func TestOurOwnPreviousEntriesDoNotCountAsCollisions(t *testing.T) {
	desired := ParseRendered(renderedAccounts)
	existing := Rebuild("root:x:0:0:root:/root:/bin/bash\n", desired)
	if c := Collisions(desired, existing); len(c) != 0 {
		t.Fatalf("a second pass over this agent's own accounts reported %v", c)
	}
}

func TestNamesAreHeldToThePortableRule(t *testing.T) {
	for _, ok := range []string{"alice", "_svc-1", "a", "user_2"} {
		if !ValidName(ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{
		"",
		"-x",             // would be read as an option by the credential tool
		"Alice",          // not portable
		"alice bob",      // whitespace splits a list
		"alice;rm -rf /", // shell punctuation
		strings.Repeat("a", 33),
	} {
		if ValidName(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestAGroupWithNoEntryIsReported(t *testing.T) {
	groups := "root:x:0:\nscsvc:x:1000:\n"
	if m := MissingGroups(ParseRendered(renderedAccounts), groups); len(m) != 0 {
		t.Errorf("a group that exists was reported missing: %v", m)
	}
	orphan := ParseRendered("carol:x:2003:4242::/nonexistent:/sbin/nologin\n")
	m := MissingGroups(orphan, groups)
	if len(m) != 1 || m[0] != "4242" {
		t.Fatalf("missing groups = %v, want the one with no entry", m)
	}
}

func TestWritingTheAccountFileReplacesItAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	body := Rebuild("root:x:0:0:root:/root:/bin/bash\n", ParseRendered(renderedAccounts))
	if err := WritePasswd(path, body); err != nil {
		t.Fatal(err)
	}

	back, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice", "root"} {
		if !strings.Contains(string(back), want) {
			t.Errorf("the written file is missing %s", want)
		}
	}

	// Readable by everyone, because every name lookup on the machine reads it
	// and an unreadable one breaks all of them.
	info, serr := os.Stat(path)
	if serr != nil {
		t.Fatal(serr)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissions = %o, want the file readable by every lookup", perm)
	}

	// No temporary file survives, so the next pass does not read a partial one.
	if _, err := os.Stat(path + ".sc-smb-agent.tmp"); err == nil {
		t.Error("the temporary file was left behind")
	}
}

func anyContains(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
