//go:build linux && compat_nc

package lifecycle_test

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// Browsing, as the three clients do it.
//
// The propfind body below is the one the clients send, reduced to the
// properties whose absence breaks a sync. The assertions are about the shape
// of the answer as much as its content: prefixes, propstat ordering and href
// spelling are all things a client matches literally.

const propfindBody = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
<d:prop>
<d:getlastmodified/><d:getcontentlength/><d:getcontenttype/><d:getetag/><d:resourcetype/>
<oc:id/><oc:fileid/><oc:permissions/><oc:size/><oc:favorite/><oc:owner-id/>
<nc:has-preview/><nc:is-encrypted/><nc:mount-type/>
</d:prop>
</d:propfind>`

// The mount answers discovery before any credential, and the list it reports
// decides what one client will even attempt: it refuses to issue a search
// unless the method appears here.
func TestDiscoveryAdvertisesTheMethodsClientsGateOn(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, _ := ncAnonymous(t, "OPTIONS", f.base+"/remote.php/dav", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("discovery answered %d, want 200", resp.StatusCode)
	}
	allow := resp.Header.Get("Allow")
	for _, method := range []string{"PROPFIND", "SEARCH", "REPORT", "MKCOL", "PUT", "MOVE", "COPY"} {
		if !strings.Contains(allow, method) {
			t.Errorf("Allow is %q, missing %s", allow, method)
		}
	}
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "1") {
		t.Errorf("DAV is %q", dav)
	}
}

// A request with no credential is challenged rather than refused silently: a
// WebDAV client does not send one until it is asked, and without the header it
// reports a broken server instead of prompting.
func TestAnUnauthenticatedListingIsChallenged(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, _ := ncAnonymous(t, "PROPFIND", f.davRoot(), strings.NewReader(propfindBody))
	if resp.StatusCode != 401 {
		t.Fatalf("answered %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("challenge is %q", got)
	}
}

// The account's own root lists the shares it may reach. A client mounts this
// collection, so an empty or malformed answer is an account that appears to
// hold nothing.
func TestTheAccountRootListsItsShares(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "PROPFIND", f.davRoot(), strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("answered %d, want 207\n%s", resp.StatusCode, body)
	}
	doc := string(body)

	// The prefixes are matched literally by one client's parser, so the
	// document has to carry these exact spellings.
	if !strings.HasPrefix(doc, `<?xml version="1.0" encoding="utf-8"?><d:multistatus`) {
		t.Fatalf("the root element is wrong:\n%s", doc[:min(200, len(doc))])
	}
	for _, ns := range []string{`xmlns:d="DAV:"`, `xmlns:oc="http://owncloud.org/ns"`, `xmlns:nc="http://nextcloud.org/ns"`} {
		if !strings.Contains(doc, ns) {
			t.Errorf("%s is not declared", ns)
		}
	}

	self := entryOf(t, doc, "/remote.php/dav/files/"+f.login+"/")
	if !strings.Contains(self, "<d:collection/>") {
		t.Errorf("the root is not a collection:\n%s", self)
	}
	if etag := propValue(t, self, "d:getetag"); etag == `""` || etag == "" {
		t.Errorf("the root carries no etag: %q", etag)
	}

	share := entryOf(t, doc, "/remote.php/dav/files/"+f.login+"/"+f.share+"/")
	if !strings.Contains(share, "<d:collection/>") {
		t.Errorf("the share is not a collection:\n%s", share)
	}
	if got := propValue(t, share, "oc:permissions"); !strings.Contains(got, "G") {
		t.Errorf("permissions are %q, and one client asserts the read letter is present", got)
	}
}

// One client reads only the first propstat of a response and another drops any
// propstat whose status is not 200. The found properties therefore have to
// come first, under a 200, in every response.
func TestTheSuccessfulPropstatComesFirst(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	askUnknown := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">` +
		`<d:prop><d:getetag/><nc:rich-workspace/></d:prop></d:propfind>`
	_, body := f.request(t, "PROPFIND", f.filePath(""), strings.NewReader(askUnknown),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	doc := string(body)

	first := strings.Index(doc, "<d:propstat>")
	if first < 0 {
		t.Fatalf("no propstat at all:\n%s", doc)
	}
	firstBlock := doc[first:]
	if end := strings.Index(firstBlock, "</d:propstat>"); end >= 0 {
		firstBlock = firstBlock[:end]
	}
	if !strings.Contains(firstBlock, "HTTP/1.1 200 OK") {
		t.Errorf("the first propstat is not the successful one:\n%s", firstBlock)
	}
	if !strings.Contains(firstBlock, "<d:getetag>") {
		t.Errorf("the etag is not in the first propstat:\n%s", firstBlock)
	}
	if strings.Contains(firstBlock, "rich-workspace") {
		t.Errorf("a property this server has no store for was reported as present:\n%s", firstBlock)
	}
}

// The four properties a sync client refuses an entry without. Missing any one
// of them marks the entry an error and blocks its parent from being synced,
// which is invisible from the status code.
func TestEveryEntryCarriesWhatASyncRefusesToGoWithout(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello there"))
	writeHostFile(t, f.host, "second.txt", []byte("more"))

	_, body := f.request(t, "PROPFIND", f.filePath(""), strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	doc := string(body)

	for _, name := range []string{"doc.bin", "second.txt"} {
		entry := entryOf(t, doc, "/remote.php/dav/files/"+f.login+"/"+f.share+"/"+name)
		if got := propValue(t, entry, "d:getetag"); len(got) < 3 {
			t.Errorf("%s: etag is %q", name, got)
		}
		if got := propValue(t, entry, "oc:fileid"); got == "" || got == "0" {
			t.Errorf("%s: fileid is %q", name, got)
		}
		if got := propValue(t, entry, "oc:id"); got == "" {
			t.Errorf("%s: id is empty", name)
		}
		if got := propValue(t, entry, "oc:permissions"); got == "" {
			t.Errorf("%s: permissions are empty", name)
		}
		if !hasProp(entry, "d:getcontentlength") {
			t.Errorf("%s: no content length", name)
		}
		if !hasProp(entry, "oc:size") {
			t.Errorf("%s: no oc:size, which is the only folder size one client reads", name)
		}
		if !strings.Contains(entry, "<d:getlastmodified>") {
			t.Errorf("%s: no modification time", name)
		}
	}
}

// An etag is quoted and carries no weakness marker. One client compares the
// quoted string against what it stored, so a marker makes every file look
// changed and re-downloads the whole tree.
func TestEtagsAreQuotedAndUnprefixed(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	_, body := f.request(t, "PROPFIND", f.filePath("doc.bin"), strings.NewReader(propfindBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	entry := entryOf(t, string(body), "/remote.php/dav/files/"+f.login+"/"+f.share+"/doc.bin")

	etag := propValue(t, entry, "d:getetag")
	if !strings.HasPrefix(etag, "&quot;") && !strings.HasPrefix(etag, `"`) {
		t.Errorf("etag %q is not quoted", etag)
	}
	if strings.Contains(etag, "W/") {
		t.Errorf("etag %q carries a weakness marker", etag)
	}
}

// A name that needs escaping has to survive the round trip: the href a
// listing reports is the address the client asks for next, and one client
// abandons a whole response whose href does not start with the path it asked
// for.
func TestAnHrefRoundTripsAnAwkwardName(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	writeHostFile(t, f.host, "a b&c#d.txt", []byte("awkward"))

	_, body := f.request(t, "PROPFIND", f.filePath(""), strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	doc := string(body)

	prefix := "/remote.php/dav/files/" + f.login + "/" + f.share + "/"
	var href string
	for _, part := range strings.Split(doc, "<d:href>") {
		if i := strings.Index(part, "</d:href>"); i >= 0 {
			candidate := part[:i]
			if strings.HasPrefix(candidate, prefix) && strings.Contains(candidate, "%20") {
				href = candidate
			}
		}
	}
	if href == "" {
		t.Fatalf("no escaped href in\n%s", doc)
	}
	if strings.ContainsAny(strings.TrimPrefix(href, prefix), " &#") {
		t.Errorf("href %q is not fully escaped", href)
	}

	resp, _ := f.request(t, "PROPFIND", f.base+href, strings.NewReader(propfindBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Errorf("the href a listing reported answered %d when asked for directly", resp.StatusCode)
	}
}

// Starring a file is a PROPPATCH, and one client unstars by removing the
// property rather than by setting it to zero. Both spellings have to work or
// the star sticks.
func TestStarringAndUnstarringBothTakeEffect(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	set := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set></d:propertyupdate>`
	resp, body := f.request(t, "PROPPATCH", f.filePath("doc.bin"), strings.NewReader(set),
		map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("starring answered %d, want 207\n%s", resp.StatusCode, body)
	}

	_, listed := f.request(t, "PROPFIND", f.filePath("doc.bin"), strings.NewReader(propfindBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	entry := entryOf(t, string(listed), "/remote.php/dav/files/"+f.login+"/"+f.share+"/doc.bin")
	if got := propValue(t, entry, "oc:favorite"); got != "1" {
		t.Errorf("after starring, favorite is %q", got)
	}

	remove := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:remove><d:prop><oc:favorite/></d:prop></d:remove></d:propertyupdate>`
	if resp, rbody := f.request(t, "PROPPATCH", f.filePath("doc.bin"), strings.NewReader(remove),
		map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != 207 {
		t.Fatalf("unstarring answered %d, want 207\n%s", resp.StatusCode, rbody)
	}

	_, after := f.request(t, "PROPFIND", f.filePath("doc.bin"), strings.NewReader(propfindBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	entry = entryOf(t, string(after), "/remote.php/dav/files/"+f.login+"/"+f.share+"/doc.bin")
	if got := propValue(t, entry, "oc:favorite"); got != "0" {
		t.Errorf("after removing the property, favorite is %q", got)
	}
}

// The starred set is read back through a REPORT, which is the only way one
// client asks for it.
func TestTheStarredSetAnswersAReport(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	set := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set></d:propertyupdate>`
	if resp, _ := f.request(t, "PROPPATCH", f.filePath("doc.bin"), strings.NewReader(set),
		map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != 207 {
		t.Fatalf("starring answered %d", resp.StatusCode)
	}

	report := `<?xml version="1.0"?><oc:filter-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:prop><d:getetag/><oc:fileid/></d:prop>` +
		`<oc:filter-rules><oc:favorite>1</oc:favorite></oc:filter-rules></oc:filter-files>`
	resp, body := f.request(t, "REPORT", f.davRoot(), strings.NewReader(report),
		map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the report answered %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "doc.bin") {
		t.Errorf("the starred file is not in the report:\n%s", body)
	}
}

// A search by name is what the client's own search box sends, and it reaches
// the whole subtree rather than one level of it.
func TestASearchFindsAFileByName(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	writeHostFile(t, f.host, "needle.txt", []byte("findable"))

	search := `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:basicsearch><d:select><d:prop><d:getetag/><oc:fileid/><d:displayname/></d:prop></d:select>` +
		`<d:from><d:scope><d:href>/files/` + f.login + `</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%needle%</d:literal></d:like></d:where>` +
		`<d:limit><d:nresults>50</d:nresults></d:limit></d:basicsearch></d:searchrequest>`

	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav", strings.NewReader(search),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "needle.txt") {
		t.Errorf("the file was not found:\n%s", body)
	}
}

// One client asks for the starred set with a search rather than with the
// filter report, and it binds the property to the vendor's other namespace
// spelling. Both have to answer the same set, or its favourites screen is
// empty while the report is correct.
func TestTheStarredSetAlsoAnswersASearch(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	set := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set></d:propertyupdate>`
	if resp, _ := f.request(t, "PROPPATCH", f.filePath("doc.bin"), strings.NewReader(set),
		map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != 207 {
		t.Fatalf("starring answered %d", resp.StatusCode)
	}

	starred := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns"><d:basicsearch>` +
		`<d:select><d:prop><d:getetag/><oc:id/></d:prop></d:select>` +
		`<d:from><d:scope><d:href>/files/` + f.login + `</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where><d:eq><d:prop><oc:favorite/></d:prop><d:literal>yes</d:literal></d:eq></d:where>` +
		`</d:basicsearch></d:searchrequest>`

	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav", strings.NewReader(starred),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "doc.bin") {
		t.Errorf("the starred file is not in the search result:\n%s", body)
	}
}

// A share the account holds but that cannot be read right now stays in the
// root listing.
//
// This is the one answer that loses data if it is wrong: a folder missing
// from an otherwise successful parent listing means "deleted on the server"
// to a sync client, so dropping an unavailable share would have it delete the
// person's local copy of every file in it.
func TestAnUnreadableShareStaysInTheListing(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	// The backing directory goes away underneath the server, which is what a
	// locked container or an unmounted disk looks like from here.
	if err := os.Chmod(f.host, 0o000); err != nil {
		t.Fatalf("sealing the share: %v", err)
	}
	t.Cleanup(func() {
		if cerr := os.Chmod(f.host, 0o700); cerr != nil {
			t.Errorf("unsealing the share: %v", cerr)
		}
	})

	resp, body := f.request(t, "PROPFIND", f.davRoot(), strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the root listing answered %d, want 207\n%s", resp.StatusCode, body)
	}

	href := "/remote.php/dav/files/" + f.login + "/" + f.share + "/"
	doc := string(body)
	if !strings.Contains(doc, "<d:href>"+href+"</d:href>") {
		t.Fatalf("the share is absent from the listing, which reads as deleted:\n%s", doc)
	}
	entry := entryOf(t, doc, href)
	if !strings.Contains(entry, "<d:collection/>") {
		t.Errorf("the share is no longer reported as a collection:\n%s", entry)
	}
	// And it is reported as one that cannot be described rather than as a
	// healthy folder. Both of these are absent only in the unreachable
	// rendering, so this is what keeps the test from passing through the
	// ordinary path: an identity or a validator here would claim the server
	// can enumerate something it currently cannot.
	if strings.Contains(entry, "<oc:fileid>") {
		t.Errorf("an unreadable share was described as readable:\n%s", entry)
	}
	if strings.Contains(entry, "<d:getetag>") {
		t.Errorf("an unreadable share carried a validator:\n%s", entry)
	}
	// Nothing about the host filesystem reaches the client: the reason a
	// share is unreadable is a log line, not a property.
	for _, leak := range []string{f.host, "permission denied", "vfs:"} {
		if strings.Contains(doc, leak) {
			t.Errorf("the response leaked %q:\n%s", leak, doc)
		}
	}
}

// A file whose name is not valid UTF-8 must not take the listing down with
// it. A name on a POSIX filesystem is a byte string, and a document holding a
// raw invalid sequence is unparseable, so one such file would empty the
// folder for every client that opens it.
func TestABadlyNamedFileDoesNotBreakTheListing(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	writeHostFile(t, f.host, "bad-\x92-name.txt", []byte("still readable"))

	resp, body := f.request(t, "PROPFIND", f.filePath(""), strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the listing answered %d, want 207", resp.StatusCode)
	}

	var doc struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the listing is not well formed: %v\n%s", err, body)
	}

	// The well-named file is still there, and so is the awkward one: a client
	// addresses it by the href, which is escaped byte for byte.
	names := 0
	for _, r := range doc.Responses {
		if strings.Contains(r.Href, "doc.bin") || strings.Contains(r.Href, "%92") {
			names++
		}
	}
	if names != 2 {
		t.Errorf("the listing named %d of the two files:\n%s", names, body)
	}
}
