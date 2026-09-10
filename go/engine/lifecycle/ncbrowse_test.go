//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
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

// searchResponses decodes a multistatus into the hrefs it named and how many
// of them are collections.
func searchResponses(t *testing.T, body []byte) (hrefs []string, collections int) {
	t.Helper()
	var doc struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				Prop struct {
					ResourceType struct {
						Collection *struct{} `xml:"collection"`
					} `xml:"resourcetype"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the search is not well formed: %v\n%s", err, body)
	}
	for _, r := range doc.Responses {
		hrefs = append(hrefs, r.Href)
		for _, ps := range r.Propstat {
			if ps.Prop.ResourceType.Collection != nil {
				collections++
				break
			}
		}
	}
	return hrefs, collections
}

// searchBody spells the request the phone's search screen sends: a name
// LIKE over a scope, and a row count only when the client states one.
func searchBody(login, scope, literal string, rows int) string {
	limit := ""
	if rows > 0 {
		limit = `<d:limit><d:nresults>` + strconv.Itoa(rows) + `</d:nresults></d:limit>`
	}
	return `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:basicsearch><d:select><d:prop><d:displayname/><d:resourcetype/></d:prop></d:select>` +
		`<d:from><d:scope><d:href>/files/` + login + scope + `</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%` + literal + `%</d:literal></d:like></d:where>` +
		`<d:orderby/>` + limit + `</d:basicsearch></d:searchrequest>`
}

// seedSearchCorpus writes folders and files whose names all match "target",
// more of them than the old hundred-row page.
func seedSearchCorpus(t *testing.T, host string) (files, dirs int) {
	t.Helper()
	for i := range 12 {
		dir := filepath.Join(host, "target-dir-"+strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("seeding a folder: %v", err)
		}
		dirs++
		for j := range 12 {
			name := filepath.Join(dir, "target-"+strconv.Itoa(i)+"-"+strconv.Itoa(j)+".txt")
			if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
				t.Fatalf("seeding a file: %v", err)
			}
			files++
		}
	}
	return files, dirs
}

// The phone's search screen states no row count, and it means it: every
// match belongs in the answer. A hundred rows of a hundred and fifty reads
// as the whole answer, and the app has no way to ask for the rest.
func TestASearchWithNoStatedLimitAnswersEveryMatch(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	files, dirs := seedSearchCorpus(t, f.host)

	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav",
		strings.NewReader(searchBody(f.login, "", "target", 0)),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207", resp.StatusCode)
	}

	hrefs, collections := searchResponses(t, body)
	if len(hrefs) != files+dirs {
		t.Errorf("the search reported %d of %d matches", len(hrefs), files+dirs)
	}
	// Folders match a name search too: someone typing a folder's name is
	// looking for the folder.
	if collections != dirs {
		t.Errorf("the search reported %d folders, want %d", collections, dirs)
	}
}

// A stated row count is still honoured exactly: a client that pages asked
// for a page.
func TestASearchHonoursAStatedLimit(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	seedSearchCorpus(t, f.host)

	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav",
		strings.NewReader(searchBody(f.login, "", "target", 7)),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207", resp.StatusCode)
	}
	if hrefs, _ := searchResponses(t, body); len(hrefs) != 7 {
		t.Errorf("a search limited to 7 answered %d rows", len(hrefs))
	}
}

// The scope in the body is a confinement, not a hint. A folder picker asks
// about one subtree, and offering it destinations from a sibling is an
// answer to a question nobody asked.
func TestASearchStaysInsideItsScope(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	seedSearchCorpus(t, f.host)

	scope := "/" + f.share + "/target-dir-3"
	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav",
		strings.NewReader(searchBody(f.login, scope, "target", 0)),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207", resp.StatusCode)
	}

	hrefs, _ := searchResponses(t, body)
	if len(hrefs) != 12 {
		t.Errorf("the scoped search reported %d rows, want the 12 files in that folder", len(hrefs))
	}
	for _, href := range hrefs {
		if !strings.Contains(href, "target-dir-3/") {
			t.Errorf("a hit outside the scope: %s", href)
		}
	}
}

// A search the engine turned away is a refusal, not an empty answer.
//
// The engine runs a bounded number of searches at once. Answering the one it
// declined with an empty multistatus tells a sync client the files are not
// there, and a client that acts on that deletes its local copies.
func TestASearchRefusedForBeingBusySaysSo(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	writeHostFile(t, f.host, "busy-target.txt", []byte("x"))

	// One slot, held by a search that blocks in its own callback until this
	// test lets go.
	f.e.Search.SetBounds(1, 0)
	sources := search.SourcesOf(f.e.Core.UserScanSources(core.UserID(f.user)))
	release := make(chan struct{})
	held := make(chan struct{})
	// The outcome travels on a channel rather than through t: the search
	// unblocks as this test body ends, and reporting from there panics the
	// run with "Log in goroutine after test has completed".
	done := make(chan error, 1)
	var once sync.Once
	task.Go(context.Background(), "test: hold the search slot", func() {
		_, qerr := f.e.Search.Query(context.Background(), sources, svc.QueryOptions{
			Query: "busy-target",
			Stream: func([]search.Hit) {
				once.Do(func() { close(held) })
				<-release
			},
		})
		done <- qerr
	})
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the occupying search never reached its callback")
	}

	resp, body := f.request(t, "SEARCH", f.base+"/remote.php/dav",
		strings.NewReader(searchBody(f.login, "", "busy-target", 0)),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a refused search answered %d, want 503\n%s", resp.StatusCode, body)
	}

	resp, body = f.request(t, "GET",
		f.base+"/ocs/v2.php/search/providers/files/search?term=busy-target&limit=25&format=json",
		nil, map[string]string{"Accept": "application/json"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the unified search answered %d, want 503\n%s", resp.StatusCode, body)
	}

	// Let the held search finish and read its outcome here, inside the test,
	// so nothing touches t after this returns.
	close(release)
	select {
	case qerr := <-done:
		if qerr != nil {
			t.Errorf("the occupying search failed: %v", qerr)
		}
	case <-time.After(10 * time.Second):
		t.Error("the occupying search never finished")
	}
}

// The folder picker asks for folders only, with d:is-collection beside the
// name comparison. A list of files is a list of destinations that cannot
// hold anything.
func TestAFolderOnlySearchAnswersFoldersOnly(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	_, dirs := seedSearchCorpus(t, f.host)

	body := `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:basicsearch><d:select><d:prop><d:displayname/><d:resourcetype/></d:prop></d:select>` +
		`<d:from><d:scope><d:href>/files/` + f.login + `</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where><d:and><d:is-collection/>` +
		`<d:like><d:prop><d:displayname/></d:prop><d:literal>%target%</d:literal></d:like>` +
		`</d:and></d:where><d:orderby/></d:basicsearch></d:searchrequest>`

	resp, out := f.request(t, "SEARCH", f.base+"/remote.php/dav", strings.NewReader(body),
		map[string]string{"Content-Type": "text/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("the search answered %d, want 207\n%s", resp.StatusCode, out)
	}

	hrefs, collections := searchResponses(t, out)
	if len(hrefs) != dirs || collections != dirs {
		t.Errorf("a folders-only search answered %d rows of which %d are folders, want %d of each",
			len(hrefs), collections, dirs)
	}
}

// The unified search panel reads a title, a path and a folder to open. A
// folder is a result like any other there, and the path has to be the one
// the account navigates: the share-relative one opens a folder that does
// not exist.
func TestTheUnifiedSearchNamesFoldersAndNavigablePaths(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	if err := os.MkdirAll(filepath.Join(f.host, "target-dir"), 0o700); err != nil {
		t.Fatalf("seeding a folder: %v", err)
	}
	writeHostFile(t, f.host, "target-file.txt", []byte("x"))

	resp, body := f.request(t, "GET",
		f.base+"/ocs/v2.php/search/providers/files/search?term=target&limit=50&format=json",
		nil, map[string]string{"Accept": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("the unified search answered %d\n%s", resp.StatusCode, body)
	}

	var doc struct {
		OCS struct {
			Data struct {
				Entries []struct {
					Title      string `json:"title"`
					Subline    string `json:"subline"`
					Attributes struct {
						Path string `json:"path"`
					} `json:"attributes"`
				} `json:"entries"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the answer does not parse: %v\n%s", err, body)
	}

	byTitle := map[string]string{}
	for _, e := range doc.OCS.Data.Entries {
		byTitle[e.Title] = e.Attributes.Path
	}
	folder, hasFolder := byTitle["target-dir"]
	if !hasFolder {
		t.Errorf("the folder is missing from the results: %v", byTitle)
	}
	if want := "/" + f.share + "/target-dir"; hasFolder && folder != want {
		t.Errorf("the folder is at %q, want %q", folder, want)
	}
	if file, ok := byTitle["target-file.txt"]; !ok || file != "/"+f.share+"/target-file.txt" {
		t.Errorf("the file is at %q, want %q", file, "/"+f.share+"/target-file.txt")
	}
}
