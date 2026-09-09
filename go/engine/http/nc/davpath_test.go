//go:build linux && compat_nc

package nc

import (
	"strings"
	"testing"
)

// The URL layouts, and the hrefs that go back out.
//
// A client builds these two ways in one session and compares the href it gets
// back against the path it asked for, so both spellings have to parse and the
// answer has to carry the spelling that arrived.

func TestEveryLayoutParses(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		path    string
		kind    TargetKind
		user    string
		want    []string
		session string
		member  string
		trash   TrashOp
	}{
		{path: "/remote.php/dav", kind: KindRoot},
		{path: "/remote.php/dav/", kind: KindRoot},
		{path: "/index.php/remote.php/dav", kind: KindRoot},
		{
			path: "/remote.php/dav/files/alice/docs/a.txt",
			kind: KindFiles, user: "alice", want: []string{"docs", "a.txt"},
		},
		{
			path: "/index.php/remote.php/dav/files/alice/docs/a.txt",
			kind: KindFiles, user: "alice", want: []string{"docs", "a.txt"},
		},
		{path: "/remote.php/dav/files/alice", kind: KindFiles, user: "alice"},
		{path: "/remote.php/webdav/docs/a.txt", kind: KindFiles, want: []string{"docs", "a.txt"}},
		{
			path: "/remote.php/dav/uploads/alice/session-1/000003",
			kind: KindUploads, user: "alice", session: "session-1", member: "000003",
		},
		{
			path: "/remote.php/dav/uploads/alice/session-1/.file",
			kind: KindUploads, user: "alice", session: "session-1", member: AssemblyMember,
		},
		{
			path: "/remote.php/dav/trashbin/alice/trash",
			kind: KindTrash, user: "alice", trash: TrashList,
		},
		{
			path: "/remote.php/dav/trashbin/alice/restore/2-ab",
			kind: KindTrash, user: "alice", trash: TrashRestore, want: []string{"2-ab"},
		},
		{path: "/remote.php/dav/principals/users/alice", kind: KindPrincipals},
	} {
		got, ok := ParseTarget(c.path)
		if !ok {
			t.Errorf("%s did not parse", c.path)
			continue
		}
		if got.Kind != c.kind {
			t.Errorf("%s is kind %v, want %v", c.path, got.Kind, c.kind)
		}
		if got.User != c.user {
			t.Errorf("%s names account %q, want %q", c.path, got.User, c.user)
		}
		if strings.Join(got.Path, "/") != strings.Join(c.want, "/") {
			t.Errorf("%s split to %q, want %q", c.path, got.Path, c.want)
		}
		if got.Session != c.session || got.Member != c.member {
			t.Errorf("%s gave session %q member %q", c.path, got.Session, got.Member)
		}
		if got.Trash != c.trash {
			t.Errorf("%s gave trash half %v", c.path, got.Trash)
		}
	}
}

// A path outside every layout is refused rather than guessed at.
func TestAnUnknownLayoutIsRefused(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/remote.php/dav/whatever/alice",
		"/dav/files/alice",
		"/status.php",
		"/remote.php/dav/files/alice/../../etc/passwd",
		"/remote.php/dav/files/alice/a%2Fb",
	} {
		if _, ok := ParseTarget(path); ok {
			t.Errorf("%s parsed and should not have", path)
		}
	}
}

// The prefix an href is built from is the one the request used, because a
// client abandons a whole response whose hrefs do not start with the path it
// asked for.
func TestAnHrefKeepsTheRequestsOwnPrefix(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"/remote.php/dav/files/alice", "/index.php/remote.php/dav/files/alice"} {
		target, ok := ParseTarget(prefix + "/docs")
		if !ok {
			t.Fatalf("%s did not parse", prefix)
		}
		href := target.Href([]string{"docs", "a b&c.txt"}, false)
		if !strings.HasPrefix(href, prefix+"/") {
			t.Errorf("href %q does not start with %q", href, prefix)
		}
		if strings.ContainsAny(strings.TrimPrefix(href, prefix), " &") {
			t.Errorf("href %q is not escaped", href)
		}
		if !strings.HasSuffix(target.Href([]string{"docs"}, true), "/") {
			t.Error("a collection href carries no trailing slash")
		}
	}
}

// A chunk name is a number, spelled two ways by two clients, and the listing
// answers the padded form one of them recognises.
func TestChunkNames(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		member string
		want   uint32
		ok     bool
	}{
		{"1", 1, true},
		{"000001", 1, true},
		{"000042", 42, true},
		{".file", 0, false},
		{"", 0, false},
		{"1a", 0, false},
		{"99999999999", 0, false},
	} {
		got, ok := ChunkName(c.member)
		if ok != c.ok || got != c.want {
			t.Errorf("%q parsed to (%d, %v), want (%d, %v)", c.member, got, ok, c.want, c.ok)
		}
	}
	if got := FormatChunkName(42); got != "000042" {
		t.Errorf("42 formats as %q", got)
	}
}
