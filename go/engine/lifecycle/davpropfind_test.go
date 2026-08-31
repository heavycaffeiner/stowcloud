//go:build linux

package lifecycle_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// propfind runs a PROPFIND and returns the recorder.
func (f *fixture) propfind(t *testing.T, path, depth, body string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if depth != "" {
		headers["Depth"] = depth
	}
	w := httptest.NewRecorder()
	f.h.Propfind(w, request("PROPFIND", "/files/"+path, body, headers), f.resolve(t, path))
	return w
}

// hrefs returns the href of every response in a multistatus body, in order.
func hrefs(t *testing.T, body string) []string {
	t.Helper()
	var doc struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the multistatus does not parse: %v\n%s", err, body)
	}
	out := make([]string, 0, len(doc.Responses))
	for _, r := range doc.Responses {
		out = append(out, r.Href)
	}
	return out
}

const allprop = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

// Depth: 0 answers about the target and nothing else.
func TestPropfindDepthZeroCoversTheTargetAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")
	f.write(t, "Docs/a.txt", "a")
	f.write(t, "Docs/b.txt", "b")

	w := f.propfind(t, "Docs", "0", allprop)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if got := hrefs(t, w.Body.String()); len(got) != 1 {
		t.Errorf("depth zero returned %d responses: %v", len(got), got)
	}
}

// Depth: 1 answers about the target and its immediate members, and no further.
func TestPropfindDepthOneCoversTheMembers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs/nested")
	f.write(t, "Docs/a.txt", "a")
	f.write(t, "Docs/nested/deep.txt", "deep")

	w := f.propfind(t, "Docs", "1", allprop)

	got := hrefs(t, w.Body.String())
	// The collection, a.txt and nested. Not deep.txt.
	if len(got) != 3 {
		t.Fatalf("depth one returned %d responses: %v", len(got), got)
	}
	for _, h := range got {
		if strings.Contains(h, "deep.txt") {
			t.Errorf("depth one descended two levels: %v", got)
		}
	}
}

// Depth: infinity walks the whole subtree, which is what a sync client asks
// for on its first pass.
func TestPropfindInfinityWalksTheSubtree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs/nested")
	f.write(t, "Docs/a.txt", "a")
	f.write(t, "Docs/nested/deep.txt", "deep")

	w := f.propfind(t, "Docs", "infinity", allprop)

	got := hrefs(t, w.Body.String())
	var sawDeep bool
	for _, h := range got {
		if strings.Contains(h, "deep.txt") {
			sawDeep = true
		}
	}
	if !sawDeep {
		t.Errorf("infinity did not reach the nested file: %v", got)
	}
}

// An absent Depth header is infinity, which is what RFC 4918 specifies.
func TestAnAbsentDepthIsInfinity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs/nested")
	f.write(t, "Docs/nested/deep.txt", "deep")

	w := f.propfind(t, "Docs", "", allprop)

	var sawDeep bool
	for _, h := range hrefs(t, w.Body.String()) {
		if strings.Contains(h, "deep.txt") {
			sawDeep = true
		}
	}
	if !sawDeep {
		t.Error("an absent Depth did not default to infinity")
	}
}

// An unbounded walk of a large collection is refused rather than attempted.
// The honest failure beats a response that arrives minutes later.
func TestInfinityOverALargeCollectionIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixtureBounded(t, 3)
	f.mkdir(t, "Docs")
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		f.write(t, "Docs/"+n+".txt", n)
	}

	w := f.propfind(t, "Docs", "infinity", allprop)

	if w.Code != http.StatusInsufficientStorage {
		t.Errorf("answered %d, want 507", w.Code)
	}
	// Depth one over the same collection is fine: the bound is on the
	// unbounded walk, not on the directory's size.
	if got := f.propfind(t, "Docs", "1", allprop); got.Code != http.StatusMultiStatus {
		t.Errorf("depth one answered %d, want 207", got.Code)
	}
}

// A directory renders as a collection and a file does not. Sync clients decide
// whether to descend from exactly this.
func TestPropfindMarksCollections(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")
	f.write(t, "Docs/a.txt", "a")

	body := f.propfind(t, "Docs", "1", allprop).Body.String()

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
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the multistatus does not parse: %v", err)
	}

	for _, r := range doc.Responses {
		isCollection := r.Propstat[0].Prop.ResourceType.Collection != nil
		wantCollection := !strings.HasSuffix(r.Href, ".txt")
		if isCollection != wantCollection {
			t.Errorf("%s reported collection=%v", r.Href, isCollection)
		}
	}
}

// A collection's href ends in a slash and a file's does not. Clients build
// member URLs by appending, so a missing slash produces a sibling.
func TestACollectionHrefEndsInASlash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// A nested collection, because the target's own href and a member's are
	// built by different code. Without a member collection the walk's own
	// encoding is never exercised.
	f.mkdir(t, "Docs/nested")
	f.write(t, "Docs/a.txt", "a")

	got := hrefs(t, f.propfind(t, "Docs", "1", allprop).Body.String())

	// The fixture addresses the mount at its own "/files" prefix, so the
	// hrefs echo that prefix back: the client can only request what it
	// addressed.
	want := map[string]bool{
		"/files/Docs/":        true,
		"/files/Docs/nested/": true,
		"/files/Docs/a.txt":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d hrefs, want %d: %v", len(got), len(want), got)
	}
	for _, h := range got {
		if !want[h] {
			t.Errorf("unexpected href %q; the set is %v", h, got)
		}
		delete(want, h)
	}
	for missing := range want {
		t.Errorf("href %q was not produced; got %v", missing, got)
	}
}

// A named request that asks for something nobody has gets it back as a 404
// group, which is a different answer from an empty value.
func TestANamedPropertyNobodyHasIsReportedMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "a")

	const body = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop>` +
		`<D:displayname/><D:nonsense/>` +
		`</D:prop></D:propfind>`

	var doc struct {
		Responses []struct {
			Propstat []struct {
				Status string `xml:"status"`
				Prop   struct {
					Inner string `xml:",innerxml"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	raw := f.propfind(t, "a.txt", "0", body).Body.String()
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("the multistatus does not parse: %v\n%s", err, raw)
	}
	if len(doc.Responses) != 1 {
		t.Fatalf("got %d responses", len(doc.Responses))
	}
	if len(doc.Responses[0].Propstat) != 2 {
		t.Fatalf("got %d propstat groups, want a found group and a missing one",
			len(doc.Responses[0].Propstat))
	}

	var sawOK, sawMissing bool
	for _, ps := range doc.Responses[0].Propstat {
		switch {
		case strings.Contains(ps.Status, "200"):
			sawOK = true
			if !strings.Contains(ps.Prop.Inner, "displayname") {
				t.Errorf("the found group does not hold displayname: %s", ps.Prop.Inner)
			}
		case strings.Contains(ps.Status, "404"):
			sawMissing = true
			if !strings.Contains(ps.Prop.Inner, "nonsense") {
				t.Errorf("the missing group does not name the property: %s", ps.Prop.Inner)
			}
		}
	}
	if !sawOK || !sawMissing {
		t.Errorf("the two groups are not both present: ok=%v missing=%v", sawOK, sawMissing)
	}
}

// propname lists names with no values, so a client can discover what exists
// without paying for the values.
func TestPropnameReturnsNamesWithoutValues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const body = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`
	raw := f.propfind(t, "a.txt", "0", body).Body.String()

	if !strings.Contains(raw, "<D:displayname/>") {
		t.Errorf("propname did not return an empty displayname: %s", raw)
	}
	// The name is there and the value is not.
	if strings.Contains(raw, "<D:displayname>a.txt</D:displayname>") {
		t.Errorf("propname returned a value: %s", raw)
	}
}

// PROPFIND of a file works, not only of a collection.
func TestPropfindOfAFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.propfind(t, "a.txt", "0", allprop)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<D:getcontentlength>8</D:getcontentlength>") {
		t.Errorf("the length is missing or wrong: %s", body)
	}
}

// A body that will not parse is refused before the 207 is committed, so the
// client gets an error rather than a truncated document.
func TestAMalformedBodyIsRefusedBeforeTheStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "a")

	w := f.propfind(t, "a.txt", "0", `<D:propfind xmlns:D="DAV:"><D:allprop>`)

	if w.Code == http.StatusMultiStatus {
		t.Fatal("a malformed body was answered 207")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", w.Code)
	}
}

// An unusable Depth is refused rather than silently treated as something else.
func TestAnUnusableDepthIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")

	w := f.propfind(t, "Docs", "2", allprop)

	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", w.Code)
	}
}

// PROPFIND of something absent is 404.
func TestPropfindOfWhatIsNotThere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := f.propfind(t, "absent.txt", "0", allprop)

	if w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
}

// The document a client receives is well formed XML in every case above, which
// a hand-built writer is exactly where it stops being.
func TestEveryMultistatusParses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs/nested")
	f.write(t, "Docs/a.txt", "a")
	f.write(t, "Docs/nested/deep.txt", "deep")

	for _, depth := range []string{"0", "1", "infinity"} {
		raw := f.propfind(t, "Docs", depth, allprop).Body.String()
		if err := xml.Unmarshal([]byte(raw), new(struct{})); err != nil {
			t.Errorf("depth %s produced a body that does not parse: %v\n%s", depth, err, raw)
		}
	}
}
