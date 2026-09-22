//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

// drain reads a whole document through the scanner.
func drain(t *testing.T, doc string, lim Limits) error {
	t.Helper()

	s := NewScanner(strings.NewReader(doc), lim)
	for {
		_, err := s.Token()
		if errors.Is(err, io.EOF) {
			return s.CheckBodySize()
		}
		if err != nil {
			return err
		}
	}
}

// A DOCTYPE is where an external entity declaration lives. Refusing the
// directive is what stops the file read before anything has to resolve it.
func TestADoctypeIsRefused(t *testing.T) {
	docs := []string{
		`<!DOCTYPE d [<!ENTITY x SYSTEM "file:///etc/passwd">]><d>&x;</d>`,
		`<!DOCTYPE d SYSTEM "http://evil.example/d.dtd"><d/>`,
		`<?xml version="1.0"?><!DOCTYPE d><d/>`,
	}

	for _, doc := range docs {
		t.Run(doc[:min(30, len(doc))], func(t *testing.T) {
			if err := drain(t, doc, DefaultLimits()); !errors.Is(err, ErrDirective) {
				t.Errorf("want a directive refusal, got %v", err)
			}
		})
	}
}

// The billion-laughs shape never gets to expand, because the declaration that
// defines the entities is a directive.
func TestAnEntityBombNeverExpands(t *testing.T) {
	const doc = `<!DOCTYPE lolz [
	 <!ENTITY lol "lol">
	 <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
	 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
	]><lolz>&lol3;</lolz>`

	if err := drain(t, doc, DefaultLimits()); !errors.Is(err, ErrDirective) {
		t.Errorf("want a directive refusal, got %v", err)
	}
}

// With no custom entity map, an undefined entity is a parse failure rather
// than an expansion into whatever a client declared.
func TestAnUndefinedEntityDoesNotResolve(t *testing.T) {
	err := drain(t, `<d>&custom;</d>`, DefaultLimits())
	if err == nil {
		t.Fatal("an undefined entity was accepted")
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("the stream ended cleanly with an unresolved entity")
	}
}

// Only the leading declaration is allowed. Any other instruction is refused.
func TestOnlyTheLeadingDeclarationIsAllowed(t *testing.T) {
	if err := drain(t, `<?xml version="1.0" encoding="UTF-8"?><d/>`, DefaultLimits()); err != nil {
		t.Errorf("a leading declaration was refused: %v", err)
	}

	for _, doc := range []string{
		`<?php echo 1; ?><d/>`,
		`<d><?target data?></d>`,
		`<?xml version="1.0"?><?xml version="1.0"?><d/>`,
		`<d/><?trailing?>`,
	} {
		t.Run(doc[:min(24, len(doc))], func(t *testing.T) {
			if err := drain(t, doc, DefaultLimits()); !errors.Is(err, ErrProcInst) {
				t.Errorf("want a processing-instruction refusal, got %v", err)
			}
		})
	}
}

// encoding/xml does not reject an undeclared prefix by itself. The scanner
// tracks bindings so it can.
func TestAnUndeclaredPrefixIsRefused(t *testing.T) {
	for _, doc := range []string{
		`<D:multistatus/>`,
		`<d xmlns:a="urn:a"><a:x/><b:y/></d>`,
		`<d attr:v="1"/>`,
	} {
		t.Run(doc[:min(30, len(doc))], func(t *testing.T) {
			if err := drain(t, doc, DefaultLimits()); !errors.Is(err, ErrUndeclaredPrefix) {
				t.Errorf("want an undeclared-prefix refusal, got %v", err)
			}
		})
	}
}

// Confirms the premise of the previous test: the standard decoder accepts an
// undeclared prefix, so the scanner's tracking is load-bearing and not a
// restatement of what the library already does.
func TestTheStandardDecoderAcceptsAnUndeclaredPrefix(t *testing.T) {
	dec := xml.NewDecoder(strings.NewReader(`<D:multistatus/>`))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("the premise is wrong: the standard decoder refused it: %v", err)
		}
	}
}

// A declared prefix is accepted, including one an element declares on itself
// and one inherited from an ancestor.
func TestADeclaredPrefixIsAccepted(t *testing.T) {
	for _, doc := range []string{
		`<D:multistatus xmlns:D="DAV:"/>`,
		`<D:a xmlns:D="DAV:"><D:b/></D:a>`,
		`<r xmlns:D="DAV:"><D:x D:attr="1"/></r>`,
		`<d xmlns="DAV:"><x/></d>`,
	} {
		t.Run(doc[:min(34, len(doc))], func(t *testing.T) {
			if err := drain(t, doc, DefaultLimits()); err != nil {
				t.Errorf("a declared prefix was refused: %v", err)
			}
		})
	}
}

// A prefix leaving scope stops being declared. Otherwise a sibling could use
// what a previous subtree bound.
//
// The first case is what encoding/xml handles by itself. The second is what
// the scanner's own scope stack handles: the library reports an unresolved
// prefix as the literal prefix string, so a closed subtree that declared that
// same string as a URI would still match unless the scope is popped.
func TestAPrefixLeavesScope(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"a sibling reuses an ancestor's prefix", `<r><a xmlns:D="DAV:"><D:x/></a><b><D:y/></b></r>`},
		{"a closed subtree declared the prefix as a URI", `<r><a xmlns:p="D"/><b><D:x/></b></r>`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := drain(t, c.doc, DefaultLimits()); !errors.Is(err, ErrUndeclaredPrefix) {
				t.Errorf("a prefix survived its own subtree: %v", err)
			}
		})
	}
}

// The xml prefix is bound by the specification and never declared, so
// xml:lang on a property must not be read as an undeclared prefix.
func TestTheXmlPrefixNeedsNoDeclaration(t *testing.T) {
	if err := drain(t, `<r xml:lang="en"><a xml:space="preserve"/></r>`, DefaultLimits()); err != nil {
		t.Errorf("xml:lang was refused: %v", err)
	}
}

// Each structural bound refuses rather than truncates. A truncated document
// parses into something the client did not send.
func TestEachStructuralBoundRefuses(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		lim  Limits
		want error
	}{
		{
			name: "depth",
			doc:  strings.Repeat("<a>", 20) + strings.Repeat("</a>", 20),
			lim:  Limits{Bytes: 1 << 20, Elements: 1000, Depth: 8, NameBytes: 100, TextBytes: 1000},
			want: ErrTooDeep,
		},
		{
			name: "element count",
			doc:  "<r>" + strings.Repeat("<a/>", 50) + "</r>",
			lim:  Limits{Bytes: 1 << 20, Elements: 10, Depth: 64, NameBytes: 100, TextBytes: 1000},
			want: ErrTooManyElements,
		},
		{
			name: "name bytes",
			doc:  "<" + strings.Repeat("n", 50) + "/>",
			lim:  Limits{Bytes: 1 << 20, Elements: 100, Depth: 64, NameBytes: 10, TextBytes: 1000},
			want: ErrNameTooLong,
		},
		{
			name: "attribute name bytes",
			doc:  `<r ` + strings.Repeat("n", 50) + `="v"/>`,
			lim:  Limits{Bytes: 1 << 20, Elements: 100, Depth: 64, NameBytes: 10, TextBytes: 1000},
			want: ErrNameTooLong,
		},
		{
			name: "text bytes",
			doc:  "<r>" + strings.Repeat("x", 500) + "</r>",
			lim:  Limits{Bytes: 1 << 20, Elements: 100, Depth: 64, NameBytes: 100, TextBytes: 50},
			want: ErrTooMuchText,
		},
		{
			name: "raw bytes",
			doc:  "<r>" + strings.Repeat("x", 500) + "</r>",
			lim:  Limits{Bytes: 64, Elements: 100, Depth: 64, NameBytes: 100, TextBytes: 1 << 20},
			want: ErrBodyTooLarge,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := drain(t, c.doc, c.lim)
			if !errors.Is(err, c.want) {
				t.Errorf("want %v, got %v", c.want, err)
			}
		})
	}
}

// Text accumulates across the document rather than resetting per element, so
// many small runs cannot together exceed what one large run may not.
func TestTextAccumulatesAcrossElements(t *testing.T) {
	lim := Limits{Bytes: 1 << 20, Elements: 1000, Depth: 64, NameBytes: 100, TextBytes: 100}
	doc := "<r>" + strings.Repeat("<a>xxxxxxxxxx</a>", 40) + "</r>"

	if err := drain(t, doc, lim); !errors.Is(err, ErrTooMuchText) {
		t.Errorf("400 bytes of text under a 100-byte limit gave %v", err)
	}
}

// A body at exactly the limit is served; one byte more is refused. An off-by-
// one here either rejects a valid request or admits an unbounded one.
func TestTheRawLimitIsExact(t *testing.T) {
	build := func(total int) string {
		const wrap = "<r></r>"
		return "<r>" + strings.Repeat("x", total-len(wrap)) + "</r>"
	}

	lim := Limits{Bytes: 200, Elements: 100, Depth: 64, NameBytes: 100, TextBytes: 1 << 20}

	if err := drain(t, build(200), lim); err != nil {
		t.Errorf("a body at exactly the limit was refused: %v", err)
	}
	if err := drain(t, build(201), lim); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("a body one byte past the limit gave %v", err)
	}
}

// The scanner never panics and never accepts a defended construct, whatever
// the input.
func FuzzScanner(f *testing.F) {
	for _, seed := range []string{
		`<d/>`,
		`<?xml version="1.0"?><d/>`,
		`<!DOCTYPE d><d/>`,
		`<D:x xmlns:D="DAV:"/>`,
		`<D:x/>`,
		`<d>&custom;</d>`,
		`<a><b><c/></b></a>`,
		strings.Repeat("<a>", 100),
		`<a xmlns="DAV:"/>`,
		``,
	} {
		f.Add(seed)
	}

	lim := Limits{Bytes: 4096, Elements: 200, Depth: 16, NameBytes: 64, TextBytes: 2048}

	f.Fuzz(func(t *testing.T, doc string) {
		s := NewScanner(strings.NewReader(doc), lim)

		depth, elems := 0, 0
		for {
			tok, err := s.Token()
			if err != nil {
				return
			}
			switch tk := tok.(type) {
			case xml.Directive:
				t.Fatalf("a directive reached the caller: %q", doc)
			case xml.ProcInst:
				if !strings.EqualFold(tk.Target, "xml") {
					t.Fatalf("a non-declaration instruction reached the caller: %q", doc)
				}
			case xml.StartElement:
				elems++
				depth++
				if depth > lim.Depth {
					t.Fatalf("depth %d past the limit: %q", depth, doc)
				}
				if elems > lim.Elements {
					t.Fatalf("element %d past the limit: %q", elems, doc)
				}
				if len(tk.Name.Local) > lim.NameBytes {
					t.Fatalf("a name past the limit reached the caller: %q", doc)
				}
			case xml.EndElement:
				depth--
			}
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
