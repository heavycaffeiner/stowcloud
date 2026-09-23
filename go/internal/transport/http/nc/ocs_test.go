//go:build linux && compat_nc

package nc

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

// The envelope, in both encodings.
//
// What matters is not that the writer produces some document but that each
// encoding produces the one its own client parses: the same tree renders a
// boolean as a digit in one and as a literal in the other, and a client that
// reads the wrong spelling reads false for true.

func TestTheJSONEnvelopeNestsUnderItsOwnKey(t *testing.T) {
	t.Parallel()

	body, err := render(Obj(
		P("meta", Obj(P("statuscode", Int(200)))),
		P("data", Obj(P("flag", Bool(true)), P("count", Int(3)))),
	), FormatJSON)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	var into struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
			Data struct {
				Flag  bool `json:"flag"`
				Count int  `json:"count"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if uerr := json.Unmarshal(body, &into); uerr != nil {
		t.Fatalf("the document does not parse: %v\n%s", uerr, body)
	}
	if into.OCS.Meta.StatusCode != 200 || !into.OCS.Data.Flag || into.OCS.Data.Count != 3 {
		t.Errorf("the values did not survive: %#v", into)
	}
}

// A number has to stay a number. One client decodes the capabilities document
// with a strict typed decoder and throws the whole thing away when a field it
// declared as a number arrives quoted, which disables every feature at once.
func TestNumbersAndBooleansAreNotQuotedInJSON(t *testing.T) {
	t.Parallel()

	body, err := render(Obj(P("major", Int(31)), P("enabled", Bool(true)), P("rel", Float(12.5))), FormatJSON)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	doc := string(body)
	for _, want := range []string{`"major":31`, `"enabled":true`, `"rel":12.5`} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s is not in %s", want, doc)
		}
	}
}

// The XML form spells a boolean as a digit, because a client reads the text as
// a number and treats anything else as false.
func TestTheXMLEnvelopeSpellsBooleansAsDigits(t *testing.T) {
	t.Parallel()

	body, err := render(Obj(P("on", Bool(true)), P("off", Bool(false))), FormatXML)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	doc := string(body)
	if !strings.Contains(doc, "<on>1</on>") || !strings.Contains(doc, "<off>0</off>") {
		t.Errorf("the booleans render as %s", doc)
	}
	if !strings.HasPrefix(doc, `<?xml version="1.0"?><ocs>`) {
		t.Errorf("the root is wrong: %s", doc)
	}
}

// A list renders as the repeated element every client's parser looks for.
func TestAListRendersAsRepeatedElements(t *testing.T) {
	t.Parallel()

	body, err := render(Obj(P("data", List(Str("a"), Str("b")))), FormatXML)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if got := string(body); !strings.Contains(got, "<element>a</element><element>b</element>") {
		t.Errorf("the list renders as %s", got)
	}
}

// An absent member is skipped rather than rendered empty: a client reading an
// empty string where it expected a number reads zero, which is a different
// fact from "not reported".
func TestAnAbsentMemberIsSkipped(t *testing.T) {
	t.Parallel()

	for _, f := range []Format{FormatXML, FormatJSON} {
		body, err := render(Obj(P("here", Str("x")), P("gone", Absent())), f)
		if err != nil {
			t.Fatalf("rendering: %v", err)
		}
		if strings.Contains(string(body), "gone") {
			t.Errorf("the absent member was rendered: %s", body)
		}
	}
}

// Text that came from a client comes back escaped. A file name holding an
// angle bracket would otherwise put an element into another client's parse.
func TestClientTextIsEscaped(t *testing.T) {
	t.Parallel()

	body, err := render(Obj(P("name", Str(`a<b>&"c'`))), FormatXML)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	var into struct {
		Name string `xml:"name"`
	}
	if uerr := xml.Unmarshal(body, &into); uerr != nil {
		t.Fatalf("the document is not well formed: %v\n%s", uerr, body)
	}
	if into.Name != `a<b>&"c'` {
		t.Errorf("the text round-tripped as %q", into.Name)
	}
}

// The two versions map an OCS code onto HTTP differently, and each row is a
// real client's branch: one reads the envelope and needs 200, the other reads
// the status line.
func TestTheStatusTable(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		code   int
		wantV1 int
		wantV2 int
	}{
		{StatusOKv1, 200, 100},
		{StatusOKv2, 200, 200},
		{StatusBadRequest, 200, 400},
		{StatusForbidden, 200, 403},
		{StatusNotFound, 200, 404},
		{StatusUnauthorized, 401, 401},
		{StatusFailure, 200, 500},
	} {
		if got := V1.HTTPStatus(c.code); got != c.wantV1 {
			t.Errorf("v1 %d answered %d, want %d", c.code, got, c.wantV1)
		}
		if got := V2.HTTPStatus(c.code); got != c.wantV2 {
			t.Errorf("v2 %d answered %d, want %d", c.code, got, c.wantV2)
		}
	}
}

// Format negotiation: the query parameter is a client saying what it wants,
// the header is another client saying it, and the default is the encoding the
// share family's own parser expects.
func TestFormatNegotiation(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		query, accept string
		want          Format
	}{
		{"json", "", FormatJSON},
		{"xml", "application/json", FormatXML},
		{"", "application/json", FormatJSON},
		{"", "application/xml", FormatXML},
		{"", "", FormatXML},
		{"", "*/*", FormatXML},
	} {
		if got := NegotiateFormat(c.query, c.accept); got != c.want {
			t.Errorf("query %q accept %q answered %v, want %v", c.query, c.accept, got, c.want)
		}
	}
}

// Whatever a client stored, both encodings stay parseable. The corpus is
// arbitrary text because that is what a file name, a note and a label are.
func FuzzEnvelopeValues(f *testing.F) {
	f.Add("plain")
	f.Add("<tag>")
	f.Add("a&b")
	f.Add("\x00\x01")
	// A file name on a POSIX filesystem is bytes, not text.
	f.Add("\x92")
	f.Add("\uD55C\uAE00")

	f.Fuzz(func(t *testing.T, text string) {
		tree := Obj(P("data", Obj(P("name", Str(text)), P("nested", List(Str(text))))))

		xmlBody, err := render(tree, FormatXML)
		if err != nil {
			t.Fatalf("rendering XML: %v", err)
		}
		var anyXML struct{}
		if uerr := xml.Unmarshal(xmlBody, &anyXML); uerr != nil {
			t.Fatalf("the XML is not well formed: %v\n%q", uerr, xmlBody)
		}

		jsonBody, err := render(tree, FormatJSON)
		if err != nil {
			t.Fatalf("rendering JSON: %v", err)
		}
		var anyJSON map[string]any
		if uerr := json.Unmarshal(jsonBody, &anyJSON); uerr != nil {
			t.Fatalf("the JSON does not parse: %v\n%q", uerr, jsonBody)
		}
	})
}
