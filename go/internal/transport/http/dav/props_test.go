//go:build linux

package dav

import (
	"encoding/xml"
	"strings"
	"testing"
)

func file() Resource {
	return Resource{
		Name:    "report.txt",
		Size:    1234,
		MTimeNs: 1700000000000000000,
		ETag:    "abc",
	}
}

func dir() Resource {
	r := file()
	r.Name = "Docs"
	r.IsDir = true
	return r
}

// A collection says so, and a file says it is not one. Sync clients decide
// whether to descend from this single property.
func TestResourcetypeSeparatesFilesFromCollections(t *testing.T) {
	t.Parallel()

	got, ok := LiveProp(davName("resourcetype"), dir())
	if !ok {
		t.Fatal("a directory has no resourcetype")
	}
	if len(got.Children) != 1 || got.Children[0].Name.Local != "collection" {
		t.Errorf("a directory rendered as %+v", got.Children)
	}

	got, ok = LiveProp(davName("resourcetype"), file())
	if !ok {
		t.Fatal("a file has no resourcetype")
	}
	if len(got.Children) != 0 {
		t.Errorf("a file claimed to be a collection: %+v", got.Children)
	}
}

// A collection has no length. Reporting zero is a lie a sync client acts on,
// so the property is absent instead.
func TestACollectionHasNoContentLength(t *testing.T) {
	t.Parallel()

	if _, ok := LiveProp(davName("getcontentlength"), dir()); ok {
		t.Error("a directory reported a content length")
	}
	got, ok := LiveProp(davName("getcontentlength"), file())
	if !ok {
		t.Fatal("a file reported no content length")
	}
	if got.Value != "1234" {
		t.Errorf("the length is %q, want 1234", got.Value)
	}
}

// propname lists a collection without getcontentlength, matching what allprop
// would actually produce for it.
func TestPropnameOmitsWhatAllpropWouldNotProduce(t *testing.T) {
	t.Parallel()

	for _, n := range LiveNames(dir()) {
		if n.Local == "getcontentlength" {
			t.Error("propname offered a directory a content length")
		}
	}
	var sawLength bool
	for _, n := range LiveNames(file()) {
		if n.Local == "getcontentlength" {
			sawLength = true
		}
	}
	if !sawLength {
		t.Error("propname omitted a file's content length")
	}
}

// An absent birth time omits creationdate rather than reporting the epoch,
// which a client would display as 1970.
func TestAnAbsentBirthTimeOmitsCreationDate(t *testing.T) {
	t.Parallel()

	if _, ok := LiveProp(davName("creationdate"), file()); ok {
		t.Error("a file with no birth time reported a creation date")
	}

	r := file()
	epoch := int64(0)
	r.BTimeNs = &epoch
	got, ok := LiveProp(davName("creationdate"), r)
	if !ok {
		t.Fatal("a real birth time was dropped")
	}
	// Zero is a real timestamp and has to render as one.
	if got.Value != "1970-01-01T00:00:00Z" {
		t.Errorf("the epoch rendered as %q", got.Value)
	}
}

// getetag has to match what GET returns byte for byte, weak marker included,
// or a client revalidates forever.
func TestTheETagMatchesTheHeaderForm(t *testing.T) {
	t.Parallel()

	got, _ := LiveProp(davName("getetag"), file())
	if got.Value != `"abc"` {
		t.Errorf("a strong tag rendered as %q", got.Value)
	}

	weak := file()
	weak.ETagWeak = true
	got, _ = LiveProp(davName("getetag"), weak)
	if got.Value != `W/"abc"` {
		t.Errorf("a weak tag rendered as %q", got.Value)
	}
}

// The last-modified date is RFC 1123 with a literal GMT. Go's time.RFC1123
// prints the zone name and would emit "UTC", which is not a valid HTTP date.
func TestTheModifiedDateEndsInGMT(t *testing.T) {
	t.Parallel()

	got, _ := LiveProp(davName("getlastmodified"), file())
	if !strings.HasSuffix(got.Value, " GMT") {
		t.Errorf("the date is %q, which is not an HTTP date", got.Value)
	}
}

// A directory reports the one content type every client recognises for one.
func TestContentTypeIsGuessedFromTheNameOnly(t *testing.T) {
	t.Parallel()

	got, _ := LiveProp(davName("getcontenttype"), dir())
	if got.Value != CollectionContentType {
		t.Errorf("a directory reported %q", got.Value)
	}

	for _, c := range []struct{ name, want string }{
		{"a.txt", "text/plain"},
		{"a.PNG", "image/png"},
		{"a.tar.gz", "application/octet-stream"},
		{"noext", "application/octet-stream"},
		{"trailing.", "application/octet-stream"},
	} {
		if got := ContentTypeOf(c.name); got != c.want {
			t.Errorf("%s guessed %q, want %q", c.name, got, c.want)
		}
	}
}

// A lock renders with the scope it was actually taken with. A client told
// "exclusive" for a shared lock believes it holds something nobody else can.
func TestLockdiscoveryReportsTheScopeAsTaken(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name      string
		exclusive bool
		want      string
	}{
		{"exclusive", true, "exclusive"},
		{"shared", false, "shared"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := file()
			r.Locks = []Lock{{Token: "urn:uuid:t", Path: "/f", Exclusive: c.exclusive}}

			got, ok := LiveProp(davName("lockdiscovery"), r)
			if !ok || len(got.Children) != 1 {
				t.Fatalf("lockdiscovery produced %+v", got)
			}
			body := renderProp(t, got)
			if !strings.Contains(body, "<D:"+c.want+"/>") {
				t.Errorf("a %s lock rendered as: %s", c.name, body)
			}
		})
	}
}

// An unlocked resource reports lockdiscovery as present and empty, not absent:
// "nothing holds this" is an answer, and its absence is not.
func TestAnUnlockedResourceStillReportsLockdiscovery(t *testing.T) {
	t.Parallel()

	got, ok := LiveProp(davName("lockdiscovery"), file())
	if !ok {
		t.Fatal("an unlocked resource omitted lockdiscovery")
	}
	if len(got.Children) != 0 {
		t.Errorf("an unlocked resource reported %d locks", len(got.Children))
	}
}

// The owner a client supplied comes back escaped. It is arbitrary text that
// this server stored and hands to every other client on a PROPFIND.
func TestALockOwnerIsEscapedOnTheWayOut(t *testing.T) {
	t.Parallel()

	r := file()
	r.Locks = []Lock{{
		Token: "urn:uuid:t", Path: "/f", Exclusive: true,
		Owner: `<D:href>http://evil/</D:href>`,
	}}
	got, _ := LiveProp(davName("lockdiscovery"), r)
	body := renderProp(t, got)

	if strings.Contains(body, "<D:href>http://evil/") {
		t.Errorf("a client's markup reached the body: %s", body)
	}
	if !strings.Contains(body, "&lt;D:href&gt;") {
		t.Errorf("the owner is missing or unescaped: %s", body)
	}
}

// Both scopes are advertised, because both are granted. Advertising only
// exclusive would make a client stop asking for the shared locks it can have.
func TestSupportedlockAdvertisesBothScopes(t *testing.T) {
	t.Parallel()

	got, _ := LiveProp(davName("supportedlock"), file())
	body := renderProp(t, got)
	for _, scope := range []string{"exclusive", "shared"} {
		if !strings.Contains(body, "<D:"+scope+"/>") {
			t.Errorf("%s is not advertised: %s", scope, body)
		}
	}
}

// Quota is omitted rather than reported as zero. A client reads zero available
// as "full" and stops uploading.
func TestQuotaIsAbsentRatherThanZero(t *testing.T) {
	t.Parallel()

	if _, ok := LiveProp(davName("quota-available-bytes"), file()); ok {
		t.Error("a resource with no quota reported one available")
	}
	if _, ok := LiveProp(davName("quota-used-bytes"), file()); ok {
		t.Error("a resource with no quota reported usage")
	}

	// Used without a limit is a real answer: the caller knows what is stored
	// and not what the ceiling is.
	r := file()
	r.Quota = &Quota{Used: 99}
	if got, ok := LiveProp(davName("quota-used-bytes"), r); !ok || got.Value != "99" {
		t.Errorf("used quota rendered as %+v", got)
	}
	if _, ok := LiveProp(davName("quota-available-bytes"), r); ok {
		t.Error("an unlimited quota reported an available figure")
	}
}

// A property this server does not know is refused rather than invented, which
// is what lets the caller report it as a 404 inside the multistatus.
func TestAnUnknownPropertyIsNotInvented(t *testing.T) {
	t.Parallel()

	for _, n := range []xml.Name{
		davName("getcontentlanguage"),
		davName("nonsense"),
		{Space: "urn:vendor", Local: "resourcetype"},
	} {
		if _, ok := LiveProp(n, file()); ok {
			t.Errorf("%v was answered", n)
		}
	}
}

// Every name propname offers is one allprop can actually produce. A name in
// one list and not the other is a client asking for a property that comes
// back 404 from the server that just advertised it.
func TestPropnameAndAllpropAgree(t *testing.T) {
	t.Parallel()

	epoch := int64(0)
	withBirth := file()
	withBirth.BTimeNs = &epoch
	dirWithBirth := dir()
	dirWithBirth.BTimeNs = &epoch

	// Both filesystems, since the birth time is what makes the two lists
	// differ: one that reports it must advertise creationdate, and one that
	// does not must not.
	for _, r := range []Resource{file(), dir(), withBirth, dirWithBirth} {
		produced := make(map[string]bool)
		for _, p := range LiveProps(r) {
			produced[p.Name.Local] = true
		}
		for _, n := range LiveNames(r) {
			if !produced[n.Local] {
				t.Errorf("propname offers %s and allprop does not produce it", n.Local)
			}
		}
		// And the other direction: allprop must not produce a name propname
		// never offered, which would be a property a client cannot discover.
		offered := make(map[string]bool)
		for _, n := range LiveNames(r) {
			offered[n.Local] = true
		}
		for _, p := range LiveProps(r) {
			if !offered[p.Name.Local] {
				t.Errorf("allprop produces %s and propname never offered it", p.Name.Local)
			}
		}
	}
}

// renderProp writes one property through the real writer, so a test sees what
// a client would receive rather than the struct behind it.
func renderProp(t *testing.T, p Prop) string {
	t.Helper()
	var sb strings.Builder
	m := NewMultistatus(&sb, nil)
	m.Response("/f", []PropStat{{Status: 200, Props: []Prop{p}}})
	if err := m.Close(); err != nil {
		t.Fatalf("writing the body: %v", err)
	}
	if err := xml.Unmarshal([]byte(sb.String()), new(struct{})); err != nil {
		t.Fatalf("the body does not parse: %v", err)
	}
	return sb.String()
}
