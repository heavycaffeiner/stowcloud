//go:build linux

package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// render runs a body through the writer.
func render(t *testing.T, namespaces []string, write func(*Multistatus)) string {
	t.Helper()

	var buf bytes.Buffer
	m := NewMultistatus(&buf, namespaces)
	write(m)
	if err := m.Close(); err != nil {
		t.Fatalf("writing: %v", err)
	}
	return buf.String()
}

// Everything the writer produces parses, checked by the standard library
// rather than by this package's own scanner. A response only this codebase can
// read is a response no client can.
func TestTheOutputParsesWithTheStandardLibrary(t *testing.T) {
	body := render(t, []string{"urn:vendor"}, func(m *Multistatus) {
		m.Response("/a%20b", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: davNS, Local: "displayname"}, Value: "a & b"},
				{Name: xml.Name{Space: "urn:vendor", Local: "author"}, Value: "<script>"},
			}},
			{Status: 404, Props: []Prop{
				{Name: xml.Name{Space: davNS, Local: "getetag"}},
			}},
		})
	})

	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("the standard decoder refused this body: %v\n%s", err, body)
		}
	}
}

// A value carrying markup comes back as text, not as elements. Otherwise a
// stored property name puts elements into another client's parse.
func TestAValueCannotBecomeMarkup(t *testing.T) {
	const attack = `</D:prop></D:propstat><D:propstat><D:status>HTTP/1.1 200 OK</D:status>`

	body := render(t, nil, func(m *Multistatus) {
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: davNS, Local: "displayname"}, Value: attack},
			}},
		})
	})

	// The parsed document must hold exactly one propstat, whatever the value
	// tried to say.
	dec := xml.NewDecoder(strings.NewReader(body))
	propstats := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the body does not parse: %v", err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "propstat" {
			propstats++
		}
	}
	if propstats != 1 {
		t.Errorf("an injected value produced %d propstat elements, want 1", propstats)
	}
}

// An href is escaped by the writer, not only by the encoder that usually
// produces it. A caller passing a raw path must not be able to open an element.
func TestAnHrefIsEscaped(t *testing.T) {
	// A raw href, bypassing EncodeHref. Without the writer's own escaping this
	// closes the href element and opens a response the server never wrote.
	const raw = `/a&b</D:href></D:response><D:response><D:href>/injected`

	body := render(t, nil, func(m *Multistatus) {
		m.Response(raw, nil)
	})

	dec := xml.NewDecoder(strings.NewReader(body))
	responses := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the body does not parse: %v\n%s", err, body)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "response" {
			responses++
		}
	}
	if responses != 1 {
		t.Errorf("an injected href produced %d response elements, want 1", responses)
	}
	if strings.Contains(body, "<D:href>/injected") {
		t.Errorf("an injected href element reached the body: %s", body)
	}
}

// What the encoder produces survives the writer and decodes back to the same
// segments, so a client reads the path that went in.
func TestAnHrefRoundTripsThroughTheWriter(t *testing.T) {
	body := render(t, nil, func(m *Multistatus) {
		m.Response(EncodeHref([]string{"a&b", "c<d"}, false), nil)
	})

	var doc struct {
		Href string `xml:"response>href"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the body does not parse: %v", err)
	}
	segs, err := SplitPath(doc.Href)
	if err != nil {
		t.Fatalf("the emitted href does not decode: %v", err)
	}
	if len(segs) != 2 || segs[0] != "a&b" || segs[1] != "c<d" {
		t.Errorf("the href round tripped to %q", segs)
	}
}

// The same content always produces the same bytes. A prefix taken from arrival
// order makes two identical responses differ.
func TestTheOutputIsDeterministic(t *testing.T) {
	write := func(m *Multistatus) {
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: "urn:b", Local: "two"}, Value: "2"},
				{Name: xml.Name{Space: "urn:a", Local: "one"}, Value: "1"},
			}},
		})
	}

	// The same namespaces, declared in two different orders.
	first := render(t, []string{"urn:a", "urn:b"}, write)
	second := render(t, []string{"urn:b", "urn:a"}, write)

	if first != second {
		t.Errorf("declaration order changed the response:\n%s\n%s", first, second)
	}
}

// The DAV namespace always takes the same prefix, so a golden body is a byte
// comparison rather than a parse.
func TestTheDavPrefixIsFixed(t *testing.T) {
	body := render(t, []string{"DAV:", "urn:x"}, func(m *Multistatus) {
		m.Response("/f", nil)
	})

	const want = `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:multistatus xmlns:D="DAV:" xmlns:ns0="urn:x">` +
		`<D:response><D:href>/f</D:href></D:response>` +
		`</D:multistatus>`

	if body != want {
		t.Errorf("got:\n%s\nwant:\n%s", body, want)
	}
}

// A property in a namespace nobody declared is left out rather than written
// under an invented prefix, which would make the body depend on write order.
func TestAnUndeclaredNamespaceIsNotInvented(t *testing.T) {
	body := render(t, []string{"urn:known"}, func(m *Multistatus) {
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: "urn:known", Local: "here"}, Value: "1"},
				{Name: xml.Name{Space: "urn:surprise", Local: "gone"}, Value: "2"},
			}},
		})
	})

	if !strings.Contains(body, "here") {
		t.Errorf("the declared property is missing: %s", body)
	}
	if strings.Contains(body, "gone") || strings.Contains(body, "surprise") {
		t.Errorf("an undeclared namespace reached the body: %s", body)
	}

	// And what came out still parses, which an invented prefix would break.
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Errorf("the body does not parse: %v", err)
	}
}

// propname writes names with no values, so a request for names does not
// disclose the values.
func TestPropnameWritesNoValues(t *testing.T) {
	body := render(t, nil, func(m *Multistatus) {
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: davNS, Local: "displayname"}, Value: "secret", NamesOnly: true},
			}},
		})
	})

	if strings.Contains(body, "secret") {
		t.Errorf("a propname response carried a value: %s", body)
	}
	if !strings.Contains(body, "displayname") {
		t.Errorf("the name is missing: %s", body)
	}
}

// The status line carries the code and its text.
func TestTheStatusLine(t *testing.T) {
	cases := map[int]string{
		200: "HTTP/1.1 200 OK",
		403: "HTTP/1.1 403 Forbidden",
		404: "HTTP/1.1 404 Not Found",
		423: "HTTP/1.1 423 Locked",
		424: "HTTP/1.1 424 Failed Dependency",
		507: "HTTP/1.1 507 Insufficient Storage",
	}

	for code, want := range cases {
		if got := statusLine(code); got != want {
			t.Errorf("%d: got %q, want %q", code, got, want)
		}
	}
}

// brokenWriter fails after a set number of writes, and counts what it was
// asked to do afterwards.
type brokenWriter struct {
	ok    int
	after int
	hit   bool
}

func (w *brokenWriter) Write(p []byte) (int, error) {
	if w.ok <= 0 {
		if w.hit {
			w.after++
		}
		w.hit = true
		return 0, errors.New("the connection went away")
	}
	w.ok--
	return len(p), nil
}

// A failed write ends the body. Continuing produces something that is half one
// response and half another, and the client cannot tell which half it read.
//
// Counted rather than inferred: the writer records how many times it was
// called after it first failed, and that has to be zero.
func TestAFailedWriteIsSticky(t *testing.T) {
	w := &brokenWriter{ok: 3}
	m := NewMultistatus(w, nil)

	for i := 0; i < 20; i++ {
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{{Name: xml.Name{Space: davNS, Local: "x"}, Value: "v"}}},
		})
	}

	err := m.Close()
	if err == nil {
		t.Fatal("a broken writer reported success")
	}
	if !w.hit {
		t.Fatal("the writer never failed, so the case proves nothing")
	}
	if w.after != 0 {
		t.Errorf("the writer was called %d more times after it failed", w.after)
	}
}

// The first error is what the caller sees, not the last. A later message
// describes a write that only happened because the first one failed.
func TestTheFirstErrorIsKept(t *testing.T) {
	// A writer whose message changes each time, so a replaced error is
	// visible in the text rather than only in the pointer.
	var n int
	w := writerFunc(func(p []byte) (int, error) {
		n++
		return 0, fmt.Errorf("failure %d", n)
	})

	m := NewMultistatus(w, nil)
	m.Open()

	first := m.Err()
	if first == nil {
		t.Fatal("the writer failed but reported nothing")
	}
	if !strings.Contains(first.Error(), "failure 1") {
		t.Fatalf("the first error is %v, want the first failure", first)
	}

	m.Response("/f", []PropStat{
		{Status: 200, Props: []Prop{{Name: xml.Name{Space: davNS, Local: "x"}, Value: "v"}}},
	})
	if cerr := m.Close(); cerr == nil {
		t.Fatal("Close reported success on a failed writer")
	}

	if got := m.Err(); got.Error() != first.Error() {
		t.Errorf("the error became %v, want the first one %v", got, first)
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// An empty body is still a valid document, so a PROPFIND matching nothing does
// not produce something a client cannot parse.
func TestAnEmptyBodyIsStillValid(t *testing.T) {
	body := render(t, nil, func(*Multistatus) {})

	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Errorf("an empty multistatus does not parse: %v\n%s", err, body)
	}
}

// Whatever a property value holds, the body parses and the value survives as
// text. This is the injection surface, so it is fuzzed rather than sampled.
func FuzzMultistatusValues(f *testing.F) {
	for _, seed := range []string{
		"plain",
		"a & b",
		"</D:prop>",
		"<script>alert(1)</script>",
		"\x00",
		"]]>",
		"&amp;",
		strings.Repeat("<", 100),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		var buf bytes.Buffer
		m := NewMultistatus(&buf, nil)
		m.Response("/f", []PropStat{
			{Status: 200, Props: []Prop{
				{Name: xml.Name{Space: davNS, Local: "displayname"}, Value: value},
			}},
		})
		if err := m.Close(); err != nil {
			t.Fatalf("writing %q: %v", value, err)
		}

		// The body parses, and holds exactly one response element however the
		// value was spelled.
		dec := xml.NewDecoder(bytes.NewReader(buf.Bytes()))
		responses := 0
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				// A value with a byte XML cannot carry at all is the one
				// legitimate refusal; anything else is a defect.
				if hasInvalidXMLByte(value) {
					return
				}
				t.Fatalf("%q produced a body that does not parse: %v", value, err)
			}
			if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "response" {
				responses++
			}
		}
		if responses != 1 {
			t.Errorf("%q produced %d response elements, want 1", value, responses)
		}
	})
}

// hasInvalidXMLByte reports a byte no XML document may carry.
func hasInvalidXMLByte(s string) bool {
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
