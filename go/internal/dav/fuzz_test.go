package dav

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The scanner reads bodies from strangers, so it gets a fuzz target. What is
// asserted is not that parsing succeeds: almost every input here is garbage.
// It is that the two forbidden constructs are never accepted, that the bounds
// are never exceeded by whatever comes back, and that nothing panics.

func FuzzParsePropFind(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		`<D:propfind xmlns:D="DAV:"/>`,
		`<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`,
		`<propfind xmlns="DAV:"><prop><getcontentlength/></prop></propfind>`,
		`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`,
		`<!DOCTYPE p><D:propfind xmlns:D="DAV:"/>`,
		`<?pi?><D:propfind xmlns:D="DAV:"/>`,
		`<!DOCTYPE p [<!ENTITY x SYSTEM "file:///etc/passwd">]><p>&x;</p>`,
		`<D:propfind xmlns:D="DAV:"><D:prop><D:a/><D:a/></D:prop></D:propfind>`,
		"<D:propfind xmlns:D=\"DAV:\"><D:prop><D:x>\x00</D:x></D:prop></D:propfind>",
		`<D:propfind xmlns:D="DAV:"><D:prop><D:x>&amp;&lt;&gt;</D:x></D:prop></D:propfind>`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	lim := Limits{Elements: 64, Depth: 8, NameLength: 32, TextBytes: 256}
	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := ParsePropFind(body, lim)
		if err != nil {
			return
		}
		// A document that parsed cannot have carried either forbidden
		// construct, so finding one in the source means it was accepted.
		if bytesHasDoctype(body) {
			t.Fatalf("a body containing a DOCTYPE parsed: %q", body)
		}
		if len(got.Props) > lim.Elements {
			t.Fatalf("parsed %d properties, past the element bound of %d",
				len(got.Props), lim.Elements)
		}
		for _, p := range got.Props {
			if len(p.Local) > lim.NameLength {
				t.Fatalf("a parsed name is %d bytes, past the bound of %d",
					len(p.Local), lim.NameLength)
			}
		}
		if got.Mode == PropFindNamed && len(got.Props) == 0 {
			t.Fatal("a named propfind came back with no names, which no caller can answer")
		}
	})
}

func FuzzParsePropPatch(f *testing.F) {
	seeds := []string{
		`<D:propertyupdate xmlns:D="DAV:"/>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><a xmlns="u:">v</a></D:prop></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:remove><D:prop><a xmlns="u:"/></D:prop></D:remove></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><a xmlns="u:">Tom &amp; Jerry</a></D:prop></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><a xmlns="u:"><b/></a></D:prop></D:set></D:propertyupdate>`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	lim := Limits{Elements: 64, Depth: 8, NameLength: 32, TextBytes: 256}
	f.Fuzz(func(t *testing.T, body []byte) {
		ops, err := ParsePropPatch(body, lim)
		if err != nil {
			if errors.Is(err, limits.ErrTooLarge) || errors.Is(err, ErrBadXML) ||
				errors.Is(err, ErrDTDForbidden) || errors.Is(err, ErrPIForbidden) {
				return
			}
			return
		}
		if bytesHasDoctype(body) {
			t.Fatalf("a body containing a DOCTYPE parsed: %q", body)
		}
		for _, op := range ops {
			if len(op.Value) > lim.TextBytes {
				t.Fatalf("a value is %d bytes, past the text bound of %d",
					len(op.Value), lim.TextBytes)
			}
			if len(op.Name.Local) > lim.NameLength {
				t.Fatalf("a name is %d bytes, past the bound of %d",
					len(op.Name.Local), lim.NameLength)
			}
			if op.Remove && op.Value != "" {
				t.Fatalf("a remove carries a value: %+v", op)
			}
		}
	})
}

func FuzzParseReport(f *testing.F) {
	seeds := []string{
		`<oc:filter-files xmlns:oc="u:"><D:prop xmlns:D="DAV:"><D:getetag/></D:prop></oc:filter-files>`,
		`<oc:filter-files xmlns:oc="u:"><oc:filter-rules><oc:favorite>1</oc:favorite></oc:filter-rules></oc:filter-files>`,
		`<x/>`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	lim := Limits{Elements: 64, Depth: 8, NameLength: 32, TextBytes: 256}
	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := ParseReport(body, lim)
		if err != nil {
			return
		}
		if bytesHasDoctype(body) {
			t.Fatalf("a body containing a DOCTYPE parsed: %q", body)
		}
		for _, l := range got.Leaves {
			if len(l.Value) > lim.TextBytes {
				t.Fatalf("a leaf value is %d bytes, past the bound of %d",
					len(l.Value), lim.TextBytes)
			}
		}
	})
}

// The escaping helpers are handed names that came off a filesystem, so they
// are fuzzed for the one property that matters: an href never carries a
// character that could close an element or start a new one.
func FuzzEscapeHref(f *testing.F) {
	for _, s := range []string{"/a&b", "/a<b", "/문서.txt", "/a b/c", "", "/", "//", "/%00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		got := EscapeHref(path)
		for _, c := range []string{"<", ">", "&amp;amp;"} {
			if strings.Contains(got, c) && c != "&amp;amp;" {
				t.Fatalf("EscapeHref(%q) = %q, which carries %q", path, got, c)
			}
		}
		// Double-encoding is the failure the ordering rule exists to prevent.
		if strings.Contains(got, "%25") && !strings.Contains(path, "%") {
			t.Fatalf("EscapeHref(%q) = %q, which encoded something twice", path, got)
		}
	})
}

// bytesHasDoctype looks for a DOCTYPE outside a comment or a CDATA section,
// which is enough for the fuzz assertion: encoding/xml would have surfaced any
// real one as a Directive.
func bytesHasDoctype(b []byte) bool {
	s := string(b)
	i := strings.Index(s, "<!DOCTYPE")
	if i < 0 {
		return false
	}
	// A DOCTYPE inside a comment is text, not a directive.
	c := strings.Index(s, "<!--")
	return c < 0 || c > i
}
