package dav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every document this writer produces has to parse. The writer is hand-rolled
// precisely because the encoder cannot be used, so "it parses" is the property
// that would otherwise go unchecked.
func mustParse(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("the document does not parse: %v\n%s", err, doc)
		}
	}
}

func write(t *testing.T, extra map[string]string, responses ...Response) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m := NewMultistatus(rec, extra)
	for _, r := range responses {
		if err := m.Write(r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	doc := rec.Body.String()
	mustParse(t, doc)
	return doc
}

func TestTheRootBindsDavAndTheResponseIs207(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultistatus(rec, nil)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/xml") {
		t.Fatalf("Content-Type = %q, want application/xml", ct)
	}
	doc := rec.Body.String()
	mustParse(t, doc)
	if !strings.Contains(doc, `xmlns:D="DAV:"`) {
		t.Fatalf("the root does not bind D: to DAV:\n%s", doc)
	}
}

func TestAFoundPropertyIsEmittedUnderA200Propstat(t *testing.T) {
	doc := write(t, nil, Response{
		Href:  "/dav/docs/a.txt",
		Found: []Prop{{Name: DavName("getcontentlength"), Value: "12"}},
	})
	for _, want := range []string{
		"<D:href>/dav/docs/a.txt</D:href>",
		"<D:getcontentlength>12</D:getcontentlength>",
		"HTTP/1.1 200 OK",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the document is missing %q\n%s", want, doc)
		}
	}
}

func TestANotFoundNameIsEmittedEmptyUnderA404Propstat(t *testing.T) {
	doc := write(t, nil, Response{
		Href:     "/dav/docs/a.txt",
		Found:    []Prop{{Name: DavName("getetag"), Value: `"abc"`}},
		NotFound: []Name{DavName("getcontentlanguage")},
	})
	if !strings.Contains(doc, "<D:getcontentlanguage/>") {
		t.Fatalf("the missing name is not emitted empty\n%s", doc)
	}
	if !strings.Contains(doc, "HTTP/1.1 404 Not Found") {
		t.Fatalf("the missing name has no 404 propstat\n%s", doc)
	}
	// A value must never ride along with a name that was not found.
	i := strings.Index(doc, "404")
	if strings.Contains(doc[i:], `"abc"`) {
		t.Fatalf("the found value leaked into the 404 propstat\n%s", doc)
	}
}

// A vendor namespace declared on the root uses its prefix; one that was not
// declared carries its own declaration, because a dead property can be in a
// namespace nobody announced.
func TestAVendorNamespaceUsesItsRootPrefix(t *testing.T) {
	const ns = "http://owncloud.org/ns"
	doc := write(t, map[string]string{ns: "oc"}, Response{
		Href:  "/dav/a",
		Found: []Prop{{Name: Name{Space: ns, Local: "favorite"}, Value: "1"}},
	})
	if !strings.Contains(doc, `xmlns:oc="`+ns+`"`) {
		t.Fatalf("the vendor prefix is not declared on the root\n%s", doc)
	}
	if !strings.Contains(doc, "<oc:favorite>1</oc:favorite>") {
		t.Fatalf("the property does not use the root prefix\n%s", doc)
	}
}

func TestAnUndeclaredNamespaceCarriesItsOwnDeclaration(t *testing.T) {
	doc := write(t, nil, Response{
		Href:  "/dav/a",
		Found: []Prop{{Name: Name{Space: "urn:nobody:told:us", Local: "colour"}, Value: "red"}},
	})
	if !strings.Contains(doc, `xmlns:x="urn:nobody:told:us"`) {
		t.Fatalf("the property did not declare its own namespace\n%s", doc)
	}
	mustParse(t, doc)
}

// The injection this design closes: a dead property's value is re-serialised
// from stored text, so markup in it becomes text rather than elements.
func TestADeadPropertyValueCannotInjectMarkup(t *testing.T) {
	doc := write(t, nil, Response{
		Href: "/dav/a",
		Found: []Prop{{
			Name:  Name{Space: "u:", Local: "note"},
			Value: `</x:note></D:prop><D:evil/>`,
		}},
	})
	if strings.Contains(doc, "<D:evil/>") {
		t.Fatalf("a stored value injected an element\n%s", doc)
	}
	mustParse(t, doc)
}

// An href is percent-encoded then escaped, inside a real document.
func TestAnHrefWithAnAmpersandStaysAddressable(t *testing.T) {
	doc := write(t, nil, Response{Href: "/dav/docs/a&b.txt"})
	if !strings.Contains(doc, "<D:href>/dav/docs/a%26b.txt</D:href>") {
		t.Fatalf("the href is not encoded correctly\n%s", doc)
	}
	mustParse(t, doc)
}

func TestAResourceStatusReplacesThePropstatPair(t *testing.T) {
	doc := write(t, nil, Response{Href: "/dav/a", Status: http.StatusLocked})
	if strings.Contains(doc, "propstat") {
		t.Fatalf("a resource status still emitted a propstat\n%s", doc)
	}
	if !strings.Contains(doc, "HTTP/1.1 423 Locked") {
		t.Fatalf("the resource status is missing\n%s", doc)
	}
}

// A name that cannot be an element is dropped rather than emitted, because one
// malformed name would break the document for every other property in it.
func TestAnUnwritableNameIsDroppedNotEmitted(t *testing.T) {
	doc := write(t, nil, Response{
		Href: "/dav/a",
		Found: []Prop{
			{Name: Name{Space: "u:", Local: "<evil>"}, Value: "x"},
			{Name: Name{Space: "u:", Local: "good"}, Value: "y"},
		},
	})
	if strings.Contains(doc, "evil") {
		t.Fatalf("a malformed name reached the document\n%s", doc)
	}
	if !strings.Contains(doc, "y") {
		t.Fatalf("the valid property beside it was lost\n%s", doc)
	}
}

func TestIsValidXMLNameRefusesWhatWouldBreakADocument(t *testing.T) {
	for _, bad := range []string{
		"", "1abc", "-abc", "a b", "a<b", "a>b", "a&b", "a/b",
		"x:y",             // a colon would carry its own prefix binding
		"xml", "XmlThing", // reserved by the spec in any case
		strings.Repeat("a", 257),
		"λε", // outside ASCII: no property here needs it
	} {
		if isValidXMLName(bad) {
			t.Errorf("isValidXMLName(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"a", "getetag", "favorite", "a-b", "a.b", "a_b", "a1", "_x"} {
		if !isValidXMLName(good) {
			t.Errorf("isValidXMLName(%q) = false, want true", good)
		}
	}
}

// Raw is for the properties whose value is structured, and it is never built
// from anything a client sent.
func TestResourcetypeIsEmittedAsStructure(t *testing.T) {
	doc := write(t, nil, Response{
		Href:  "/dav/docs/",
		Found: []Prop{{Name: DavName("resourcetype"), Raw: "<D:collection/>"}},
	})
	if !strings.Contains(doc, "<D:resourcetype><D:collection/></D:resourcetype>") {
		t.Fatalf("resourcetype is not structured\n%s", doc)
	}
	mustParse(t, doc)
}

func TestAnEmptyValueBecomesAnEmptyElement(t *testing.T) {
	doc := write(t, nil, Response{
		Href:  "/dav/a",
		Found: []Prop{{Name: DavName("getcontentlanguage")}},
	})
	if !strings.Contains(doc, "<D:getcontentlanguage/>") {
		t.Fatalf("an empty value did not become an empty element\n%s", doc)
	}
}

// The document must stay well formed however many responses go into it, which
// is the streaming claim: nothing is buffered and rewritten at the end.
func TestManyResponsesStreamIntoOneWellFormedDocument(t *testing.T) {
	const n = 500
	rec := httptest.NewRecorder()
	m := NewMultistatus(rec, nil)
	for i := 0; i < n; i++ {
		if err := m.Write(Response{
			Href:  "/dav/docs/file",
			Found: []Prop{{Name: DavName("getcontentlength"), Value: "1"}},
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.Count() != n {
		t.Fatalf("Count = %d, want %d", m.Count(), n)
	}
	doc := rec.Body.String()
	mustParse(t, doc)
	if got := strings.Count(doc, "<D:response>"); got != n {
		t.Fatalf("got %d response elements, want %d", got, n)
	}
}

func TestWritingAfterCloseIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultistatus(rec, nil)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Write(Response{Href: "/dav/a"}); err == nil {
		t.Fatal("a write after close was accepted, so the document would be malformed")
	}
}

func TestStatusLineNamesUnknownCodesWithoutTruncating(t *testing.T) {
	if got := statusLine(200); got != "HTTP/1.1 200 OK" {
		t.Fatalf("statusLine(200) = %q", got)
	}
	if got := statusLine(299); !strings.HasPrefix(got, "HTTP/1.1 299 ") || strings.HasSuffix(got, " ") {
		t.Fatalf("statusLine(299) = %q, want a complete line", got)
	}
}

func TestWriteErrorEmitsAPreconditionElement(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteError(rec, http.StatusLocked, DavName("lock-token-submitted"), "held by another client"); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	if rec.Code != http.StatusLocked {
		t.Fatalf("status = %d, want 423", rec.Code)
	}
	doc := rec.Body.String()
	mustParse(t, doc)
	if !strings.Contains(doc, "<D:lock-token-submitted/>") {
		t.Fatalf("the precondition element is missing\n%s", doc)
	}
	if !strings.Contains(doc, "held by another client") {
		t.Fatalf("the description is missing\n%s", doc)
	}
}

// The root declaration set is sorted, so two runs produce the same bytes and a
// golden comparison is possible at all.
func TestTheRootDeclarationsAreDeterministic(t *testing.T) {
	extra := map[string]string{
		"http://owncloud.org/ns":  "oc",
		"http://nextcloud.org/ns": "nc",
		"urn:zzz":                 "z",
	}
	first := write(t, extra, Response{Href: "/dav/a"})
	for i := 0; i < 20; i++ {
		if got := write(t, extra, Response{Href: "/dav/a"}); got != first {
			t.Fatalf("two runs differ:\n%s\n%s", first, got)
		}
	}
}
