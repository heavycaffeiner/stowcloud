//go:build linux && compat_nc

package nc

import (
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// The property vocabulary, and the multistatus shape around it.
//
// Each of these is a place a client stops working rather than complaining: a
// permission string without the read letter, an etag carrying a weakness
// marker, a document whose prefixes are not the ones its parser matches on.

func TestThePermissionStringAlwaysGrantsReading(t *testing.T) {
	t.Parallel()

	// A sync client asserts the read letter is present on every entry it
	// accepts, and refuses an entry whose permission string is empty.
	for _, p := range []acl.Perms{
		acl.Read,
		acl.Read | acl.Download,
		acl.Download,
		acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share,
	} {
		if got := PermString(p, false, false, false); !strings.Contains(got, "G") {
			t.Errorf("%v rendered as %q, with no read letter", p, got)
		}
	}
}

func TestThePermissionLetters(t *testing.T) {
	t.Parallel()

	every := acl.Read | acl.Download | acl.Write | acl.Create |
		acl.Delete | acl.Rename | acl.Move | acl.Share

	file := PermString(every, false, false, false)
	for _, letter := range []string{"G", "W", "D", "N", "V", "R"} {
		if !strings.Contains(file, letter) {
			t.Errorf("a file with every permission is %q, missing %s", file, letter)
		}
	}
	// The two create letters describe what may be made inside a collection,
	// so a file carries neither: a client reads them there as noise.
	if strings.ContainsAny(file, "CK") {
		t.Errorf("a file carries a create letter: %q", file)
	}

	dir := PermString(every, true, false, false)
	if !strings.Contains(dir, "C") || !strings.Contains(dir, "K") {
		t.Errorf("a writable collection is %q, missing a create letter", dir)
	}

	readOnly := PermString(acl.Read, true, false, false)
	if strings.ContainsAny(readOnly, "WDNVCKR") {
		t.Errorf("a read-only collection is %q", readOnly)
	}
}

func TestTheSharePermissionMask(t *testing.T) {
	t.Parallel()

	if got := SharePermissionMask(acl.Read | acl.Download); got != SharePermRead {
		t.Errorf("read rendered as %d", got)
	}
	full := SharePermissionMask(acl.Read | acl.Write | acl.Create | acl.Delete | acl.Share)
	if full != SharePermAll {
		t.Errorf("every permission rendered as %d, want %d", full, SharePermAll)
	}
}

// An etag is quoted and carries no marker: one client compares the quoted
// string against what it stored, so a marker makes every file look changed.
func TestEtagRendering(t *testing.T) {
	t.Parallel()

	if got := ETagValue("abc"); got != `"abc"` {
		t.Errorf("rendered as %q", got)
	}
	if got := ETagValue(""); got != "" {
		t.Errorf("an absent token rendered as %q", got)
	}
	// Coming back in, the shapes a proxy or a client adds are tolerated.
	for _, in := range []string{`"abc"`, `W/"abc"`, `"abc-gzip"`, "abc", ` "abc" `} {
		if got := ParseETag(in); got != "abc" {
			t.Errorf("%q parsed to %q", in, got)
		}
	}
}

// A client only ever sends a positive timestamp, and a fractional one when its
// source has sub-second stamps. Anything else is treated as absent rather than
// stamped onto a file.
func TestTheUploadTimestampHeader(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1700000000", 1700000000 * int64(time.Second), true},
		{"1700000000.500", 1700000000 * int64(time.Second), true},
		{" 1700000000 ", 1700000000 * int64(time.Second), true},
		{"0", 0, false},
		{"-5", 0, false},
		{"", 0, false},
		{"soon", 0, false},
	} {
		got, ok := ParseUnixHeader(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%q parsed to (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The identity a sync journal is keyed on: a fixed-width number and the
// deployment's own tag, stable for a file that did not move.
func TestTheFileIdentityShape(t *testing.T) {
	t.Parallel()

	got := DavID(42, "instance000000000000000")
	if !strings.HasPrefix(got, "00000042") {
		t.Errorf("the identity is %q", got)
	}
	if DavID(42, "x") == DavID(43, "x") {
		t.Error("two files share one identity")
	}
}

// The multistatus document, whose prefixes and propstat ordering are what one
// client's parser matches literally.
func TestTheMultistatusShape(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	m := NewMulti(&b)
	m.Open()
	m.Response("/remote.php/dav/files/alice/a.txt",
		[]Prop{
			TextProp(PropETag(), `"abc"`),
			{Name: PropResourceType()},
			{Name: PropShareTypes(), Children: []Node{{Name: oc("share-type"), Text: "3"}}},
		},
		[]PropName{PropRichWorkspace()},
	)
	if err := m.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	doc := b.String()

	for _, want := range []string{
		`<d:multistatus`,
		`xmlns:d="DAV:"`,
		`xmlns:oc="http://owncloud.org/ns"`,
		`xmlns:nc="http://nextcloud.org/ns"`,
		`<d:href>/remote.php/dav/files/alice/a.txt</d:href>`,
		`<d:getetag>`,
		`<oc:share-type>3</oc:share-type>`,
		`<nc:rich-workspace/>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s is missing from\n%s", want, doc)
		}
	}

	// The found properties come first, under a 200: one client reads only the
	// first propstat and another drops any block that is not 200.
	first := strings.Index(doc, "<d:propstat>")
	firstEnd := strings.Index(doc, "</d:propstat>")
	block := doc[first:firstEnd]
	if !strings.Contains(block, "HTTP/1.1 200 OK") {
		t.Errorf("the first propstat is not the successful one:\n%s", block)
	}
	if !strings.Contains(block, "<d:getetag>") {
		t.Errorf("the etag is not in the first propstat:\n%s", block)
	}
	if strings.Contains(block, "rich-workspace") {
		t.Errorf("a missing property was reported as found:\n%s", block)
	}
	if !strings.Contains(doc[firstEnd:], "HTTP/1.1 404 Not Found") {
		t.Errorf("the missing set carries no 404:\n%s", doc)
	}
}

// A resource type element, not text: a client keys folder detection on the
// nested element being there.
func TestACollectionCarriesTheResourceTypeElement(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	m := NewMulti(&b)
	m.Open()
	m.Response("/x/", []Prop{
		{Name: PropResourceType(), Children: []Node{{Name: dav("collection")}}},
	}, nil)
	if err := m.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if got := b.String(); !strings.Contains(got, "<d:resourcetype><d:collection/></d:resourcetype>") {
		t.Errorf("a collection renders as %s", got)
	}
}
