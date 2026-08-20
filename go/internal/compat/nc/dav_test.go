//go:build compat_nc

package nc

import (
	"strings"
	"testing"
)

// The path parser reads a URL a stranger sent, so what matters is what it
// refuses rather than what it accepts.

func TestTheLayoutParses(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want DavTarget
	}{
		{"the root", "/remote.php/dav/", DavTarget{Kind: TargetRoot}},
		{"the root without a slash", "/remote.php/dav", DavTarget{Kind: TargetRoot}},
		{
			"a file", "/remote.php/dav/files/alice/docs/a.txt",
			DavTarget{Kind: TargetFiles, User: "alice", Path: "docs/a.txt"},
		},
		{
			"the legacy alias", "/remote.php/webdav/docs/a.txt",
			DavTarget{Kind: TargetFiles, Path: "docs/a.txt"},
		},
		{
			"the index prefix", "/index.php/remote.php/dav/files/alice/a.txt",
			DavTarget{Kind: TargetFiles, User: "alice", Path: "a.txt"},
		},
		{
			"an upload home", "/remote.php/dav/uploads/alice",
			DavTarget{Kind: TargetUploadHome, User: "alice"},
		},
		{
			"an upload collection", "/remote.php/dav/uploads/alice/tid123",
			DavTarget{Kind: TargetUploadFolder, User: "alice", TID: "tid123"},
		},
		{
			"a member", "/remote.php/dav/uploads/alice/tid123/00001",
			DavTarget{Kind: TargetUploadChunk, User: "alice", TID: "tid123", Name: "00001"},
		},
		{
			"the assembly target", "/remote.php/dav/uploads/alice/tid123/.file",
			DavTarget{Kind: TargetUploadChunk, User: "alice", TID: "tid123", Name: FutureFileName},
		},
		{
			"the trash", "/remote.php/dav/trashbin/alice/trash",
			DavTarget{Kind: TargetTrashRoot, User: "alice"},
		},
		{
			"the trash with a trailing slash", "/remote.php/dav/trashbin/alice/trash/",
			DavTarget{Kind: TargetTrashRoot, User: "alice"},
		},
		{
			"a trash entry", "/remote.php/dav/trashbin/alice/trash/item-7",
			DavTarget{Kind: TargetTrashEntry, User: "alice", Entry: "item-7"},
		},
		{
			"a restore", "/remote.php/dav/trashbin/alice/restore/anything",
			DavTarget{Kind: TargetTrashRestore, User: "alice"},
		},
		{
			"a principal", "/remote.php/dav/principals/users/alice",
			DavTarget{Kind: TargetPrincipal, User: "alice"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseDavPath(tc.path); got != tc.want {
				t.Fatalf("ParseDavPath(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		})
	}
}

// A percent-encoded separator must never introduce a segment boundary. Doing
// the decode before the split is the classic way a path-mapping layer is
// walked out of its root.
func TestAnEncodedSeparatorCannotIntroduceABoundary(t *testing.T) {
	// The account segment carrying an encoded separator would otherwise become
	// two segments and name a different collection.
	if got := ParseDavPath("/remote.php/dav/files/alice%2F..%2Fbob/a.txt"); got.Kind != TargetNone {
		t.Fatalf("an encoded separator in the account segment parsed as %+v", got)
	}
	// And in a member name, which would nest below one.
	if got := ParseDavPath("/remote.php/dav/uploads/alice/tid/00001%2Fdeeper"); got.Kind != TargetNone {
		t.Fatalf("an encoded separator in a member name parsed as %+v", got)
	}
}

// A traversal is refused here rather than left to the path layer. The layer
// would refuse it too, but this is a malformed request and saying so beats a
// permission error.
func TestATraversalIsRefused(t *testing.T) {
	for _, p := range []string{
		"/remote.php/dav/files/alice/../../etc/passwd",
		"/remote.php/dav/files/alice/docs/../../..",
		"/remote.php/webdav/../secret",
		"/remote.php/dav/files/alice/%2e%2e/secret",
	} {
		if got := ParseDavPath(p); got.Kind != TargetNone {
			t.Fatalf("ParseDavPath(%q) = %+v, want nothing", p, got)
		}
	}
}

// A path this layout does not name is nothing, rather than something close by.
func TestAnUnknownLayoutIsNothing(t *testing.T) {
	for _, p := range []string{
		"", "/", "/remote.php/", "/remote.php/nope/x",
		"/dav/files/alice/a.txt",
		"/remote.php/dav/files/",
		"/remote.php/dav/files",
		"/remote.php/dav/uploads/",
		"/remote.php/dav/trashbin/alice",
		"/remote.php/dav/trashbin/alice/nope",
		// No nesting below a member name.
		"/remote.php/dav/uploads/alice/tid/00001/deeper",
	} {
		if got := ParseDavPath(p); got.Kind != TargetNone {
			t.Fatalf("ParseDavPath(%q) = %+v, want nothing", p, got)
		}
	}
}

// A name that legitimately needs encoding survives it.
func TestAnEncodedNameIsDecodedOnce(t *testing.T) {
	got := ParseDavPath("/remote.php/dav/files/alice/my%20docs/a%26b.txt")
	if got.Kind != TargetFiles || got.Path != "my docs/a&b.txt" {
		t.Fatalf("got %+v, want the decoded path", got)
	}
	// Decoded once, not twice: a name containing a literal percent sequence
	// stays what the user typed.
	got = ParseDavPath("/remote.php/dav/files/alice/100%2525.txt")
	if got.Path != "100%25.txt" {
		t.Fatalf("path = %q, want a single decode", got.Path)
	}
}

// The parser reads a URL from a stranger, so it is fuzzed. Nothing may panic,
// and nothing that parses may carry a separator or a traversal into a field
// the caller will treat as one segment.
func FuzzParseDavPath(f *testing.F) {
	for _, seed := range []string{
		"/remote.php/dav/files/alice/a.txt",
		"/remote.php/webdav/a.txt",
		"/remote.php/dav/uploads/alice/tid/00001",
		"/remote.php/dav/trashbin/alice/trash/item",
		"/remote.php/dav/principals/users/alice",
		"/remote.php/dav/files/alice%2F..%2Fbob/a",
		"/remote.php/", "", "/",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		got := ParseDavPath(path)
		if got.Kind == TargetNone {
			return
		}
		// Every single-segment field is exactly one segment.
		for name, v := range map[string]string{
			"user": got.User, "tid": got.TID, "name": got.Name, "entry": got.Entry,
		} {
			if strings.ContainsRune(v, '/') {
				t.Fatalf("ParseDavPath(%q) put a separator in %s: %q", path, name, v)
			}
		}
		// And the joined path carries no traversal.
		for _, seg := range strings.Split(got.Path, "/") {
			if seg == ".." || seg == "." {
				t.Fatalf("ParseDavPath(%q) produced the traversal %q in %q", path, seg, got.Path)
			}
		}
	})
}
