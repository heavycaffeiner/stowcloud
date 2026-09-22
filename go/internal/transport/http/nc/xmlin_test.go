//go:build linux && compat_nc

package nc

import (
	"strings"
	"testing"
)

// The request parsers, over what a client sends and over what an attacker
// might.
//
// Each one reads a body from the network, so the property under test is that
// nothing gets past the guards and nothing panics: the parsers answer a value
// or a refusal, never a document with an external reference resolved.

func TestAPropfindBodyIsRead(t *testing.T) {
	t.Parallel()

	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:prop><d:getetag/><oc:fileid/></d:prop></d:propfind>`
	q, err := ParsePropfind(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if q.All || q.NamesOnly {
		t.Errorf("an explicit set parsed as %#v", q)
	}
	if !q.Asked(PropETag()) || !q.Asked(PropFileID()) {
		t.Errorf("the requested names are %#v", q.Names)
	}
	if q.Asked(PropSize()) {
		t.Error("a name that was not asked for reads as asked")
	}
}

// An empty body means every property, which is what one client sends when it
// asks the upload collection what has arrived.
func TestAnEmptyPropfindBodyMeansEverything(t *testing.T) {
	t.Parallel()

	q, err := ParsePropfind(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if !q.All {
		t.Errorf("an empty body parsed as %#v", q)
	}
}

// The namespace decides which property a name means, not the prefix: the
// three clients bind the same namespaces to different prefixes.
func TestPropertiesAreMatchedByNamespaceNotPrefix(t *testing.T) {
	t.Parallel()

	body := `<?xml version="1.0"?><x:propfind xmlns:x="DAV:" xmlns:y="http://owncloud.org/ns">` +
		`<x:prop><x:getetag/><y:fileid/></x:prop></x:propfind>`
	q, err := ParsePropfind(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if !q.Asked(PropETag()) || !q.Asked(PropFileID()) {
		t.Errorf("unusual prefixes parsed as %#v", q.Names)
	}
}

// A doctype is refused outright. It is the entry point for an external
// entity, and this body arrives from the network.
func TestADoctypeIsRefused(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`<!DOCTYPE d [<!ENTITY x SYSTEM "file:///etc/passwd">]><d:propfind xmlns:d="DAV:"><d:prop>&x;</d:prop></d:propfind>`,
		`<!DOCTYPE d SYSTEM "http://elsewhere.example/d.dtd"><d:propfind xmlns:d="DAV:"/>`,
	} {
		if _, err := ParsePropfind(strings.NewReader(body)); err == nil {
			t.Errorf("this was accepted: %s", body)
		}
	}
}

// Unstarring removes the property rather than setting it to zero, so the
// remove half of a property update is load-bearing.
func TestAProppatchReadsBothHalves(t *testing.T) {
	t.Parallel()

	set := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set></d:propertyupdate>`
	ops, err := ParseProppatch(strings.NewReader(set))
	if err != nil {
		t.Fatalf("parsing a set: %v", err)
	}
	if len(ops) != 1 || ops[0].Remove || ops[0].Value != "1" || !ops[0].Name.Equal(PropFavorite()) {
		t.Fatalf("a set parsed as %#v", ops)
	}

	remove := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:remove><d:prop><oc:favorite/></d:prop></d:remove></d:propertyupdate>`
	ops, err = ParseProppatch(strings.NewReader(remove))
	if err != nil {
		t.Fatalf("parsing a remove: %v", err)
	}
	if len(ops) != 1 || !ops[0].Remove || !ops[0].Name.Equal(PropFavorite()) {
		t.Fatalf("a remove parsed as %#v", ops)
	}
}

// The filter report asks for the starred set with the value a client sends,
// and with nothing else.
func TestTheFavouriteFilterIsStrict(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"0", false},
		{"false", false},
		{"", false},
	} {
		body := `<?xml version="1.0"?><oc:filter-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
			`<d:prop><d:getetag/></d:prop><oc:filter-rules><oc:favorite>` + c.value +
			`</oc:favorite></oc:filter-rules></oc:filter-files>`
		fq, err := ParseFilterFiles(strings.NewReader(body))
		if err != nil {
			t.Fatalf("parsing %q: %v", c.value, err)
		}
		if fq.Favorite != c.want {
			t.Errorf("%q parsed as %v, want %v", c.value, fq.Favorite, c.want)
		}
	}
}

// The search body one client sends for its own search box, reduced to what
// the handler branches on.
func TestASearchRequestIsRead(t *testing.T) {
	t.Parallel()

	body := `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
		`<d:basicsearch><d:select><d:prop><d:getetag/></d:prop></d:select>` +
		`<d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%needle%</d:literal></d:like></d:where>` +
		`<d:orderby><d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order></d:orderby>` +
		`<d:limit><d:nresults>25</d:nresults></d:limit></d:basicsearch></d:searchrequest>`

	sq, err := ParseSearchRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if sq.ScopeHref != "/files/alice" {
		t.Errorf("the scope is %q", sq.ScopeHref)
	}
	if sq.Limit != 25 || !sq.Descending {
		t.Errorf("limit %d descending %v", sq.Limit, sq.Descending)
	}
	if sq.Where.Op != "like" || !sq.Where.Prop.Equal(PropDisplayName()) || sq.Where.Literal != "%needle%" {
		t.Errorf("the where clause is %#v", sq.Where)
	}
	if len(sq.Select) != 1 || !sq.Select[0].Equal(PropETag()) {
		t.Errorf("the selected set is %#v", sq.Select)
	}
}

// One client binds the vendor property to a second spelling of the vendor
// namespace, so the same query has to be recognised under either.
func TestAFavouriteSearchIsRecognisedUnderEitherNamespace(t *testing.T) {
	t.Parallel()

	for _, ns := range []string{nsOwnCloud, nsNextcloud, nsNextcloudAlt} {
		body := `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:" xmlns:oc="` + ns + `">` +
			`<d:basicsearch><d:select><d:prop><d:getetag/></d:prop></d:select>` +
			`<d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
			`<d:where><d:eq><d:prop><oc:favorite/></d:prop><d:literal>yes</d:literal></d:eq></d:where>` +
			`</d:basicsearch></d:searchrequest>`
		sq, err := ParseSearchRequest(strings.NewReader(body))
		if err != nil {
			t.Fatalf("parsing under %s: %v", ns, err)
		}
		if !hasFavoriteEq(sq.Where) {
			t.Errorf("a starred-set query under %s was not recognised: %#v", ns, sq.Where)
		}
	}
}

// Whatever arrives, the parsers answer or refuse. The corpus is the shapes a
// client sends plus the shapes an attacker would try.
func FuzzRequestBodies(f *testing.F) {
	f.Add(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>`)
	f.Add(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:propname/></d:propfind>`)
	f.Add(`<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><x/></d:prop></d:set></d:propertyupdate>`)
	f.Add(`<oc:filter-files xmlns:oc="http://owncloud.org/ns"><oc:filter-rules/></oc:filter-files>`)
	f.Add(`<d:searchrequest xmlns:d="DAV:"><d:basicsearch><d:where><d:and/></d:where></d:basicsearch></d:searchrequest>`)
	f.Add(`<!DOCTYPE x><d:propfind xmlns:d="DAV:"/>`)
	f.Add(`<d:propfind`)
	f.Add("")
	f.Add(strings.Repeat("<a>", 500))

	f.Fuzz(func(t *testing.T, body string) {
		// Each parser either answers or refuses; neither may panic, and a
		// refusal is what a malformed or hostile body is supposed to get. The
		// answers are read so the compiler cannot elide the work.
		if q, err := ParsePropfind(strings.NewReader(body)); err == nil {
			_ = q.Asked(PropETag())
		}
		if ops, err := ParseProppatch(strings.NewReader(body)); err == nil {
			_ = len(ops)
		}
		if fq, err := ParseFilterFiles(strings.NewReader(body)); err == nil {
			_ = fq.Favorite
		}
		if sq, err := ParseSearchRequest(strings.NewReader(body)); err == nil {
			_ = hasFavoriteEq(sq.Where)
		}
	})
}

// A request path either parses into a target inside the mount or is refused.
// Nothing may panic, and nothing may parse into a component that could name
// something other than a file below the tree.
func FuzzParseTarget(f *testing.F) {
	f.Add("/remote.php/dav/files/alice/docs/a.txt")
	f.Add("/index.php/remote.php/dav/uploads/alice/session/000001")
	f.Add("/remote.php/dav/trashbin/alice/trash/1-ab")
	f.Add("/remote.php/webdav/")
	f.Add("/remote.php/dav//files//alice//a")
	f.Add("/remote.php/dav/files/alice/%2e%2e/%2e%2e/etc")
	f.Add("/remote.php/dav/files/alice/%00")
	f.Add("")

	f.Fuzz(func(t *testing.T, path string) {
		target, ok := ParseTarget(path)
		if !ok {
			return
		}
		for _, comp := range target.Path {
			if comp == "" || comp == "." || comp == ".." ||
				strings.ContainsAny(comp, "/\x00") {
				t.Fatalf("%q parsed to component %q", path, comp)
			}
		}
		// An href built from what was parsed stays inside the prefix the
		// request arrived under, which is what a client compares against.
		href := target.Href(target.Path, false)
		if !strings.HasPrefix(href, target.Prefix) {
			t.Fatalf("%q rendered href %q outside prefix %q", path, href, target.Prefix)
		}
	})
}
