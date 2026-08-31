//go:build linux && compat_nc

package compat

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

// emit renders an envelope.
func emit(t *testing.T, e Envelope, f Format) string {
	t.Helper()

	var buf bytes.Buffer
	if err := Write(&buf, e, f); err != nil {
		t.Fatalf("writing: %v", err)
	}
	return buf.String()
}

// The one place the two formats genuinely differ. A boolean is a boolean in
// JSON; in XML a true is "1" and a false is nothing at all, because a client
// checking presence reads a literal "false" as a non-empty string.
func TestABooleanRendersDifferentlyInEachFormat(t *testing.T) {
	e := func(b bool) Envelope {
		return Envelope{Version: V2, Status: StatusOKv2, Data: Map(P("flag", Bool(b)))}
	}

	jsonTrue := emit(t, e(true), FormatJSON)
	jsonFalse := emit(t, e(false), FormatJSON)
	if !strings.Contains(jsonTrue, `"flag":true`) {
		t.Errorf("JSON true: %s", jsonTrue)
	}
	if !strings.Contains(jsonFalse, `"flag":false`) {
		t.Errorf("JSON false: %s", jsonFalse)
	}

	xmlTrue := emit(t, e(true), FormatXML)
	xmlFalse := emit(t, e(false), FormatXML)
	if !strings.Contains(xmlTrue, "<flag>1</flag>") {
		t.Errorf("XML true: %s", xmlTrue)
	}
	if !strings.Contains(xmlFalse, "<flag/>") {
		t.Errorf("XML false: %s", xmlFalse)
	}
	if strings.Contains(xmlFalse, "false") {
		t.Errorf("a literal false reached the XML: %s", xmlFalse)
	}
}

// Map order is preserved, so a document reads the way it was built. Go's own
// map iteration is randomized, which is exactly why the tree is ordered.
func TestMapOrderIsPreserved(t *testing.T) {
	data := Map(
		P("zebra", Int(1)),
		P("alpha", Int(2)),
		P("middle", Int(3)),
	)
	e := Envelope{Version: V2, Status: StatusOKv2, Data: data}

	// Repeated because a randomized order would only sometimes disagree.
	first := emit(t, e, FormatJSON)
	for i := 0; i < 50; i++ {
		if got := emit(t, e, FormatJSON); got != first {
			t.Fatalf("the same tree produced two documents:\n%s\n%s", first, got)
		}
	}

	zebra := strings.Index(first, "zebra")
	alpha := strings.Index(first, "alpha")
	middle := strings.Index(first, "middle")
	if zebra >= alpha || alpha >= middle {
		t.Errorf("keys came out in the wrong order: %s", first)
	}
}

// A list's XML items are always named element, whatever they hold.
func TestListItemsAreNamedElement(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Map(P("items", List(Str("a"), Int(2), Bool(true)))),
	}, FormatXML)

	if strings.Count(body, "<element>") != 3 {
		t.Errorf("want three element tags: %s", body)
	}
}

// An empty value self-closes rather than writing an open and close pair.
func TestAnEmptyValueSelfCloses(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Map(
			P("nothing", Empty()),
			P("blank", Str("")),
			P("emptylist", List()),
			P("emptymap", Map()),
		),
	}, FormatXML)

	for _, name := range []string{"nothing", "blank", "emptylist", "emptymap"} {
		if !strings.Contains(body, "<"+name+"/>") {
			t.Errorf("%s did not self-close: %s", name, body)
		}
	}
}

// A key no XML parser would accept as a tag becomes an element instead, so a
// numeric map key does not produce a document nothing can read.
func TestANumericKeyDoesNotBecomeATag(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Map(P("123", Str("v")), P("ok", Str("w"))),
	}, FormatXML)

	if strings.Contains(body, "<123>") {
		t.Errorf("a numeric key became a tag: %s", body)
	}
	if !strings.Contains(body, "<ok>w</ok>") {
		t.Errorf("a usable key was rewritten: %s", body)
	}

	// And the result parses, which is the point.
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Errorf("the body does not parse: %v\n%s", err, body)
	}
}

// Every key an XML parser refuses becomes an element, not only the obviously
// numeric ones. Fuzzing found two a hand-written range check let through: an
// invalid UTF-8 byte, and U+3FFFF, which is a valid rune the decoder still
// rejects in a name. Both produced a whole response no client could read.
func TestAKeyTheParserRefusesBecomesAnElement(t *testing.T) {
	keys := []string{
		"123",
		"",
		"a b",
		"a<b",
		"a&b",
		"\xe5",       // invalid UTF-8
		"\U0003FFFF", // a valid rune the XML decoder refuses in a name
		"\uFFFF",     // a noncharacter
		"-leading",
		".leading",
	}

	for _, key := range keys {
		t.Run(strconv.Quote(key), func(t *testing.T) {
			body := emit(t, Envelope{
				Version: V2, Status: StatusOKv2,
				Data: Map(P(key, Str("v"))),
			}, FormatXML)

			dec := xml.NewDecoder(strings.NewReader(body))
			for {
				_, err := dec.Token()
				if errors.Is(err, io.EOF) {
					return
				}
				if err != nil {
					t.Fatalf("key %q produced a body that does not parse: %v\n%s", key, err, body)
				}
			}
		})
	}
}

// A key that is a usable tag stays one, so the check does not rewrite every
// key into element and lose the names.
func TestAUsableKeyStaysATag(t *testing.T) {
	keys := []string{"ok", "_private", "a-b", "a.b", "a1", "\u00e9t\u00e9", "\uD55C\uAE00"}

	for _, key := range keys {
		body := emit(t, Envelope{
			Version: V2, Status: StatusOKv2,
			Data: Map(P(key, Str("v"))),
		}, FormatXML)

		if !strings.Contains(body, "<"+key+">v</"+key+">") {
			t.Errorf("key %q was rewritten: %s", key, body)
		}
	}
}

// Both encodings parse with the standard library, an independent
// implementation. A response only this package can read is one no client can.
func TestBothFormatsParseWithTheStandardLibrary(t *testing.T) {
	e := Envelope{
		Version: V2,
		Status:  StatusOKv2,
		Message: "OK",
		Data: Map(
			P("text", Str(`a & b <c> "d"`)),
			P("number", Int(-42)),
			P("flag", Bool(true)),
			P("list", List(Str("x"), Str("y"))),
			P("nested", Map(P("inner", Str("v")))),
		),
	}

	body := emit(t, e, FormatXML)
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the XML does not parse: %v\n%s", err, body)
		}
	}

	body = emit(t, e, FormatJSON)
	var into map[string]any
	if err := json.Unmarshal([]byte(body), &into); err != nil {
		t.Fatalf("the JSON does not parse: %v\n%s", err, body)
	}

	// And the JSON's values keep their types through the standard decoder.
	ocs, ok := into["ocs"].(map[string]any)
	if !ok {
		t.Fatalf("the JSON has no ocs object: %#v", into)
	}
	data, ok := ocs["data"].(map[string]any)
	if !ok {
		t.Fatalf("the JSON has no data object: %#v", ocs)
	}
	if _, ok := data["flag"].(bool); !ok {
		t.Errorf("the boolean did not survive as a boolean: %#v", data["flag"])
	}
	if _, ok := data["number"].(float64); !ok {
		t.Errorf("the number did not survive as a number: %#v", data["number"])
	}
}

// Text goes through escaping, so a value cannot open an element.
func TestTextCannotBecomeMarkup(t *testing.T) {
	const attack = `</message></meta><data>injected</data>`

	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2, Message: attack,
	}, FormatXML)

	if strings.Contains(body, "<data>injected") {
		t.Errorf("an injected element reached the body: %s", body)
	}
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Errorf("the body does not parse: %v", err)
	}
}

// The two versions map an OCS code to HTTP differently, and each row matters
// to a real client.
func TestTheStatusTable(t *testing.T) {
	cases := []struct {
		ocs    int
		wantV1 int
		wantV2 int
	}{
		{StatusOKv1, 200, 100},
		{StatusOKv2, 200, 200},
		{StatusUnauthorized, 401, 401},
		{StatusNotFound, 200, 404},
		{StatusInvalid, 200, 500},
		{StatusFailure, 200, 500},
		{403, 200, 403},
		{404, 200, 404},
		{42, 200, 400},
		{700, 200, 400},
	}

	for _, c := range cases {
		if got := V1.HTTPStatus(c.ocs); got != c.wantV1 {
			t.Errorf("v1 %d: got %d, want %d", c.ocs, got, c.wantV1)
		}
		if got := V2.HTTPStatus(c.ocs); got != c.wantV2 {
			t.Errorf("v2 %d: got %d, want %d", c.ocs, got, c.wantV2)
		}
	}
}

// v1 answers 200 for everything except an authentication failure, because a
// v1 client reads the envelope rather than the status line. Only 997 is the
// exception, and treating others as failures breaks those clients.
func TestV1AnswersOkExceptForAuthentication(t *testing.T) {
	for ocs := 100; ocs < 1000; ocs++ {
		want := 200
		if ocs == StatusUnauthorized {
			want = 401
		}
		if got := V1.HTTPStatus(ocs); got != want {
			t.Fatalf("v1 %d: got %d, want %d", ocs, got, want)
		}
	}
}

// Each version's success code drives the meta block's word.
func TestTheMetaStatusWord(t *testing.T) {
	cases := []struct {
		version Version
		status  int
		want    string
	}{
		{V1, StatusOKv1, "ok"},
		{V1, StatusOKv2, "failure"},
		{V2, StatusOKv2, "ok"},
		{V2, StatusOKv1, "failure"},
		{V2, StatusNotFound, "failure"},
	}

	for _, c := range cases {
		body := emit(t, Envelope{Version: c.version, Status: c.status}, FormatJSON)
		if !strings.Contains(body, `"status":"`+c.want+`"`) {
			t.Errorf("v%d code %d: want %q in %s", c.version, c.status, c.want, body)
		}
	}
}

// The format query wins over Accept, since a client that spelled it out in the
// URL said so more deliberately than one sending a header its library filled
// in. An unknown format means XML rather than an error.
func TestFormatNegotiation(t *testing.T) {
	cases := []struct {
		query  string
		accept string
		want   Format
	}{
		{"json", "", FormatJSON},
		{"json", "text/xml", FormatJSON},
		{"xml", "application/json", FormatXML},
		{"", "application/json", FormatJSON},
		{"", "application/json, text/plain", FormatJSON},
		{"", "text/xml", FormatXML},
		{"", "", FormatXML},
		{"yaml", "application/json", FormatXML},
		{"JSON", "", FormatXML},
		{" json ", "", FormatJSON},
	}

	for _, c := range cases {
		if got := NegotiateFormat(c.query, c.accept); got != c.want {
			t.Errorf("query %q accept %q: got %v, want %v", c.query, c.accept, got, c.want)
		}
	}
}

// The declared content type matches what was written.
func TestTheContentTypeMatchesTheBody(t *testing.T) {
	if !strings.Contains(FormatJSON.ContentType(), "application/json") {
		t.Errorf("JSON declares %q", FormatJSON.ContentType())
	}
	if !strings.Contains(FormatXML.ContentType(), "xml") {
		t.Errorf("XML declares %q", FormatXML.ContentType())
	}
}

// The JSON envelope wraps in an ocs object; the XML root is the ocs element.
// A client reads one shape or the other and not both.
func TestTheEnvelopeShape(t *testing.T) {
	e := Envelope{Version: V2, Status: StatusOKv2, Message: "OK", Data: Str("d")}

	jsonBody := emit(t, e, FormatJSON)
	var into struct {
		OCS struct {
			Meta struct {
				Status     string `json:"status"`
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
			Data string `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &into); err != nil {
		t.Fatalf("the JSON does not parse: %v\n%s", err, jsonBody)
	}
	if into.OCS.Meta.StatusCode != StatusOKv2 || into.OCS.Data != "d" {
		t.Errorf("the JSON envelope is %#v", into)
	}

	xmlBody := emit(t, e, FormatXML)
	if !strings.HasPrefix(xmlBody, `<?xml version="1.0"?><ocs>`) {
		t.Errorf("the XML root is wrong: %s", xmlBody)
	}
	if !strings.HasSuffix(xmlBody, "</ocs>") {
		t.Errorf("the XML is not closed: %s", xmlBody)
	}
}

// Whatever a value holds, both encodings stay parseable by the standard
// library. This is the injection surface, so it is fuzzed.
func FuzzEnvelopeValues(f *testing.F) {
	for _, seed := range []string{
		"plain", "a & b", "</ocs>", `"quoted"`, "\x00", "]]>", "<!--", "\uFFFF",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		e := Envelope{
			Version: V2,
			Status:  StatusOKv2,
			Message: value,
			Data:    Map(P("v", Str(value)), P(value, Str("k"))),
		}

		jsonBody := emit(t, e, FormatJSON)
		var into map[string]any
		if err := json.Unmarshal([]byte(jsonBody), &into); err != nil {
			t.Fatalf("%q produced JSON that does not parse: %v", value, err)
		}

		xmlBody := emit(t, e, FormatXML)
		dec := xml.NewDecoder(strings.NewReader(xmlBody))
		depth, roots := 0, 0
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if hasInvalidXMLRune(value) {
					return
				}
				t.Fatalf("%q produced XML that does not parse: %v\n%s", value, err, xmlBody)
			}
			switch el := tok.(type) {
			case xml.StartElement:
				// Only the outermost element. A map key spelled "ocs" is a
				// legitimate nested element, not a second root.
				if depth == 0 {
					roots++
					if el.Name.Local != "ocs" {
						t.Errorf("%q produced root %q, want ocs", value, el.Name.Local)
					}
				}
				depth++
			case xml.EndElement:
				depth--
			}
		}
		if roots != 1 {
			t.Errorf("%q produced %d roots, want 1", value, roots)
		}
	})
}

// hasInvalidXMLRune reports a rune no XML document may carry.
func hasInvalidXMLRune(s string) bool {
	for _, r := range s {
		switch {
		case r == 0x09 || r == 0x0A || r == 0x0D:
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= 0x10FFFF:
		default:
			return true
		}
	}
	return false
}
