//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The byte-exact format smbd's libc reads. A field out of place breaks the name
// lookup for every account after it in the file.
const renderedFixture = "alice:x:3001:2000::/nonexistent:/usr/sbin/nologin\n" +
	"bob:x:3002:2000::/nonexistent:/usr/sbin/nologin\n"

func TestParseRenderedStampsTheMarker(t *testing.T) {
	got := ParseRendered(renderedFixture)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(got))
	}
	if got[0].Name != "alice" || got[0].UID != "3001" || got[0].GID != "2000" {
		t.Errorf("the first entry is %+v", got[0])
	}
	// The marker is what makes an account removable later. Without it this
	// agent cannot tell its own accounts from the system's.
	for _, e := range got {
		if !managed(e.Line) {
			t.Errorf("the marker is missing from %q", e.Line)
		}
	}
}

// A malformed line reaches the system account file and breaks the lookup for
// everything after it, so it is dropped rather than passed through.
func TestParseRenderedDropsMalformedLines(t *testing.T) {
	body := "alice:x:3001:2000::/nonexistent:/usr/sbin/nologin\n" +
		"this is not a passwd line\n" +
		":x:3003:2000::/nonexistent:/usr/sbin/nologin\n" +
		"short:x:3004\n" +
		"\n" +
		"bob:x:3002:2000::/nonexistent:/usr/sbin/nologin\n"

	got := ParseRendered(body)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want the two well-formed ones: %+v", len(got), got)
	}
	if got[0].Name != "alice" || got[1].Name != "bob" {
		t.Errorf("the wrong entries survived: %+v", got)
	}
}

// The rebuilt file keeps every account that is not this agent's and replaces
// every account that is, which is what makes a removed account disappear
// rather than persist.
func TestRebuildKeepsTheSystemAndReplacesOurs(t *testing.T) {
	current := "root:x:0:0:root:/root:/bin/bash\n" +
		"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n" +
		"gone:x:3009:2000:" + managedMarker + ":/nonexistent:/usr/sbin/nologin\n"

	out := Rebuild(current, ParseRendered(renderedFixture))

	for _, keep := range []string{"root:x:0:0", "daemon:x:1:1"} {
		if !strings.Contains(out, keep) {
			t.Errorf("a system account was dropped: %q missing from\n%s", keep, out)
		}
	}
	if strings.Contains(out, "gone") {
		t.Errorf("an account no longer rendered survived the rebuild:\n%s", out)
	}
	for _, want := range []string{"alice", "bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("a rendered account is missing: %q\n%s", want, out)
		}
	}
}

// A collision is refused for the whole batch. A passwd write applied partway
// against one is exactly how a uid ends up owned by the wrong account.
func TestCollisionsRefuseOnNameAndOnUID(t *testing.T) {
	system := "root:x:0:0:root:/root:/bin/bash\n" +
		"alice:x:1500:1500:a real person:/home/alice:/bin/bash\n" +
		"carol:x:3002:2000:another real person:/home/carol:/bin/bash\n"

	got := Collisions(ParseRendered(renderedFixture), system)
	if len(got) == 0 {
		t.Fatal("a colliding batch was accepted")
	}

	joined := strings.Join(got, "\n")
	// alice collides by name.
	if !strings.Contains(joined, `"alice"`) {
		t.Errorf("the name collision is not reported:\n%s", joined)
	}
	// bob's uid 3002 is already carol's, which is the collision that binds a
	// credential to the wrong name.
	if !strings.Contains(joined, "3002") || !strings.Contains(joined, "carol") {
		t.Errorf("the uid collision is not reported:\n%s", joined)
	}
}

// An account this agent created is not a collision with itself, or every pass
// after the first would refuse.
func TestOurOwnAccountsAreNotCollisions(t *testing.T) {
	desired := ParseRendered(renderedFixture)
	// The file as it stands after a previous pass.
	current := Rebuild("root:x:0:0:root:/root:/bin/bash\n", desired)

	if got := Collisions(desired, current); len(got) != 0 {
		t.Errorf("a second pass refused over its own accounts: %v", got)
	}
}

// A name that fails the rule is refused before it can reach the account file
// or the credential tool's argument list.
func TestCollisionsRefuseAnInvalidName(t *testing.T) {
	body := "-oProxyCommand:x:3001:2000::/nonexistent:/usr/sbin/nologin\n"
	got := Collisions(ParseRendered(body), "")
	if len(got) == 0 {
		t.Fatal("a name that is not a valid account name was accepted")
	}
	if !strings.Contains(strings.Join(got, "\n"), "not a valid") {
		t.Errorf("the refusal does not say why: %v", got)
	}
}

// This agent invents no groups, and an account whose group resolves to nothing
// breaks the lookup as surely as a missing account.
func TestMissingGroupsReportsTheAbsentOnes(t *testing.T) {
	groups := "root:x:0:\nusers:x:100:\n"
	got := MissingGroups(ParseRendered(renderedFixture), groups)
	if len(got) != 1 || got[0] != "2000" {
		t.Errorf("MissingGroups reported %v, want the one absent gid", got)
	}

	present := groups + "scsvc:x:2000:\n"
	if got := MissingGroups(ParseRendered(renderedFixture), present); len(got) != 0 {
		t.Errorf("a present group was reported missing: %v", got)
	}
}

// The account file has to be readable by every name lookup on the machine, and
// has to survive power loss as one file or the other.
func TestWritePasswdIsDurableAndReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")

	const first = "root:x:0:0:root:/root:/bin/bash\n"
	if err := WritePasswd(path, first); err != nil {
		t.Fatal(err)
	}

	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(reader)

	second := first + strings.Repeat("filler:x:9:9:::\n", 20000)
	if werr := WritePasswd(path, second); werr != nil {
		t.Fatal(werr)
	}

	// A reader open across the replace still sees the whole previous file,
	// which is what an in-place write would have truncated underneath it.
	during := make([]byte, len(second))
	n, rerr := reader.Read(during)
	if rerr != nil && n == 0 {
		t.Fatal(rerr)
	}
	if string(during[:n]) != first {
		t.Errorf("a reader open across the write saw %d bytes, want the previous file whole", n)
	}

	st, serr := os.Stat(path)
	if serr != nil {
		t.Fatal(serr)
	}
	if st.Mode().Perm() != passwdMode {
		t.Errorf("the account file is mode %v, want %v", st.Mode().Perm(), os.FileMode(passwdMode))
	}
}

// The rule that decides what may become a system account name.
func TestValidName(t *testing.T) {
	for _, good := range []string{"alice", "a", "_svc", "user-1", "a_b-c9", strings.Repeat("a", MaxAccountName)} {
		if !ValidName(good) {
			t.Errorf("%q was refused", good)
		}
	}
	for _, bad := range []string{
		"",
		strings.Repeat("a", MaxAccountName+1),
		"Alice",            // upper case: the lookup is case-sensitive here
		"1alice",           // a digit first
		"-alice",           // reads as an option to the credential tool
		"al ice",           // a space splits an account list entry in two
		"al:ice",           // the field separator itself
		"al\nice",          // a second line in the account file
		"aliceÿ",           // outside the portable set
		"-oProxyCommand=x", // argument injection
	} {
		if ValidName(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
