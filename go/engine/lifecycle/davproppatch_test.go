//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// proppatch runs a PROPPATCH and returns the recorder.
func (f *fixture) proppatch(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.h.Proppatch(w, request("PROPPATCH", "/files/"+path, body, nil), f.resolve(t, path))
	return w
}

// storedProps reads the property rows back, which is what tells a write that
// reported success from one that happened.
func (f *fixture) storedProps(t *testing.T, path string) map[string]string {
	t.Helper()
	res := f.resolve(t, path)
	st, err := res.Root().Stat(res.Path())
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	rows, perr := f.props.Props(context.Background(), lifecycle.DavKeyOf(f.core.EntryAt(res, st)))
	if perr != nil {
		t.Fatalf("reading the properties of %q: %v", path, perr)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.NS+" "+r.Name] = r.Value
	}
	return out
}

// outcomes maps each property in a multistatus to the status it reported.
func outcomes(t *testing.T, body string) map[string]int {
	t.Helper()
	var doc struct {
		Responses []struct {
			Propstat []struct {
				Status string `xml:"status"`
				Prop   struct {
					Any []struct {
						XMLName xml.Name
					} `xml:",any"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the multistatus does not parse: %v\n%s", err, body)
	}

	out := map[string]int{}
	for _, r := range doc.Responses {
		for _, ps := range r.Propstat {
			code := 0
			switch {
			case strings.Contains(ps.Status, "200"):
				code = http.StatusOK
			case strings.Contains(ps.Status, "403"):
				code = http.StatusForbidden
			case strings.Contains(ps.Status, "424"):
				code = http.StatusFailedDependency
			}
			for _, p := range ps.Prop.Any {
				out[p.XMLName.Local] = code
			}
		}
	}
	return out
}

const setOne = `<?xml version="1.0"?>` +
	`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:vendor">` +
	`<D:set><D:prop><V:colour>blue</V:colour></D:prop></D:set>` +
	`</D:propertyupdate>`

// A dead property is stored and comes back on the next read.
func TestProppatchStoresADeadProperty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.proppatch(t, "a.txt", setOne)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if got := outcomes(t, w.Body.String())["colour"]; got != http.StatusOK {
		t.Errorf("the property reported %d, want 200", got)
	}
	if got := f.storedProps(t, "a.txt")["urn:vendor colour"]; got != "blue" {
		t.Errorf("the stored value is %q, want blue", got)
	}
}

// A removal takes it away again.
func TestProppatchRemovesADeadProperty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	if w := f.proppatch(t, "a.txt", setOne); w.Code != http.StatusMultiStatus {
		t.Fatalf("the set answered %d", w.Code)
	}

	const remove = `<?xml version="1.0"?>` +
		`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:vendor">` +
		`<D:remove><D:prop><V:colour/></D:prop></D:remove>` +
		`</D:propertyupdate>`

	w := f.proppatch(t, "a.txt", remove)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("the removal answered %d, want 207", w.Code)
	}
	if _, still := f.storedProps(t, "a.txt")["urn:vendor colour"]; still {
		t.Error("the property survived its own removal")
	}
}

// A live property cannot be written.
//
// Allowing it would let a client believe it changed a file's length by writing
// the number, which the next PROPFIND would contradict.
func TestALivePropertyCannotBeWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const body = `<?xml version="1.0"?>` +
		`<D:propertyupdate xmlns:D="DAV:">` +
		`<D:set><D:prop><D:getcontentlength>99</D:getcontentlength></D:prop></D:set>` +
		`</D:propertyupdate>`

	w := f.proppatch(t, "a.txt", body)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if got := outcomes(t, w.Body.String())["getcontentlength"]; got != http.StatusForbidden {
		t.Errorf("a live property reported %d, want 403", got)
	}
}

// One refused instruction costs the whole request.
//
// RFC 4918 makes PROPPATCH atomic. A partial commit leaves the resource in a
// state the client did not ask for and cannot work out from the response,
// which says what each property got and not what the resource now holds.
func TestOneRefusalStopsEveryWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const body = `<?xml version="1.0"?>` +
		`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:vendor">` +
		`<D:set><D:prop>` +
		`<V:colour>blue</V:colour>` +
		`<D:getcontentlength>99</D:getcontentlength>` +
		`</D:prop></D:set>` +
		`</D:propertyupdate>`

	w := f.proppatch(t, "a.txt", body)

	got := outcomes(t, w.Body.String())
	if got["getcontentlength"] != http.StatusForbidden {
		t.Errorf("the live property reported %d, want 403", got["getcontentlength"])
	}
	// The one that could have been written reports the dependency failure
	// rather than success, because it was not written.
	if got["colour"] != http.StatusFailedDependency {
		t.Errorf("the dead property reported %d, want 424", got["colour"])
	}
	if _, stored := f.storedProps(t, "a.txt")["urn:vendor colour"]; stored {
		t.Error("a refused request wrote one of its properties anyway")
	}
}

// The stored property survives a rename, because it is keyed on the file's
// identity rather than on the name it happened to have.
func TestAPropertySurvivesARename(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "before.txt", "contents")

	if w := f.proppatch(t, "before.txt", setOne); w.Code != http.StatusMultiStatus {
		t.Fatalf("the set answered %d", w.Code)
	}

	w := httptest.NewRecorder()
	f.h.Move(w, request("MOVE", "/files/before.txt", "", nil),
		f.resolve(t, "before.txt"), f.target(t, "after.txt", true))
	if w.Code != http.StatusCreated {
		t.Fatalf("the move answered %d, want 201", w.Code)
	}

	if got := f.storedProps(t, "after.txt")["urn:vendor colour"]; got != "blue" {
		t.Errorf("the property did not follow the rename: %q", got)
	}
}

// Deleting a resource takes its properties with it. Left behind they would
// attach to whatever next occupies the inode, so a new file would be born
// carrying a deleted one's properties.
func TestDeletingAResourceDiscardsItsProperties(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	if w := f.proppatch(t, "a.txt", setOne); w.Code != http.StatusMultiStatus {
		t.Fatalf("the set answered %d", w.Code)
	}
	before := f.storedProps(t, "a.txt")
	if before["urn:vendor colour"] != "blue" {
		t.Fatalf("the property was not stored: %v", before)
	}
	res := f.resolve(t, "a.txt")
	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		t.Fatalf("stat: %v", serr)
	}
	key := lifecycle.DavKeyOf(f.core.EntryAt(res, st))

	w := httptest.NewRecorder()
	f.h.Delete(w, request("DELETE", "/files/a.txt", "", nil), res)
	if w.Code != http.StatusNoContent {
		t.Fatalf("the delete answered %d, want 204", w.Code)
	}

	rows, err := f.props.Props(context.Background(), key)
	if err != nil {
		t.Fatalf("reading the properties: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d properties outlived the resource: %v", len(rows), rows)
	}
}

// A stored property is returned by a named PROPFIND beside the live ones.
func TestPropfindReturnsAStoredProperty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	if w := f.proppatch(t, "a.txt", setOne); w.Code != http.StatusMultiStatus {
		t.Fatalf("the set answered %d", w.Code)
	}

	const query = `<?xml version="1.0"?>` +
		`<D:propfind xmlns:D="DAV:" xmlns:V="urn:vendor"><D:prop>` +
		`<D:displayname/><V:colour/>` +
		`</D:prop></D:propfind>`

	body := f.propfind(t, "a.txt", "0", query).Body.String()

	if !strings.Contains(body, ">blue<") {
		t.Errorf("the stored property is missing from the listing: %s", body)
	}
	if !strings.Contains(body, "a.txt") {
		t.Errorf("the live property is missing: %s", body)
	}
}

// A property name that cannot be written back as a tag is refused rather than
// stored. Stored, it would break every later PROPFIND on the resource, and the
// client that caused it would never see the failure.
//
// The XML parser is the first gate and refuses most of these, so this asserts
// the outcome a client sees rather than which layer produced it: nothing is
// stored and the request does not report success.
func TestAnUnwritableNameIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		// A digit cannot start an XML name.
		"1bad",
		// Nor can a hyphen.
		"-bad",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.write(t, "a.txt", "contents")

			body := `<?xml version="1.0"?>` +
				`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:vendor">` +
				`<D:set><D:prop><V:` + name + `>x</V:` + name + `></D:prop></D:set>` +
				`</D:propertyupdate>`

			w := f.proppatch(t, "a.txt", body)

			if w.Code == http.StatusMultiStatus {
				if got := outcomes(t, w.Body.String())[name]; got == http.StatusOK {
					t.Error("an unwritable name reported success")
				}
			}
			if stored := f.storedProps(t, "a.txt"); len(stored) != 0 {
				t.Errorf("an unwritable name was stored: %v", stored)
			}
		})
	}
}

// An underscore does start a legal XML name, so a property called _1 is
// stored rather than refused.
//
// Recorded because the first version of the test above assumed the opposite
// and passed for the wrong reason: it read a successful store as a refusal
// that had not happened.
func TestAnUnderscoreNameIsLegal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const body = `<?xml version="1.0"?>` +
		`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:vendor">` +
		`<D:set><D:prop><V:_1>x</V:_1></D:prop></D:set>` +
		`</D:propertyupdate>`

	w := f.proppatch(t, "a.txt", body)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if got := f.storedProps(t, "a.txt")["urn:vendor _1"]; got != "x" {
		t.Errorf("a legal name was not stored: %q", got)
	}
}

// The lock guard covers PROPPATCH, since it writes.
func TestProppatchIsGuardedByTheLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	f.locks.refuse = errors.New("held")

	w := f.proppatch(t, "a.txt", setOne)

	if w.Code != http.StatusLocked {
		t.Errorf("answered %d, want 423", w.Code)
	}
	if len(f.storedProps(t, "a.txt")) != 0 {
		t.Error("a locked resource had a property written")
	}
}

// Every response above is well formed XML.
func TestEveryProppatchResponseParses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	for _, body := range []string{
		setOne,
		`<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:">` +
			`<D:set><D:prop><D:getetag>x</D:getetag></D:prop></D:set></D:propertyupdate>`,
	} {
		raw := f.proppatch(t, "a.txt", body).Body.String()
		if err := xml.Unmarshal([]byte(raw), new(struct{})); err != nil {
			t.Errorf("a response does not parse: %v\n%s", err, raw)
		}
	}
}
