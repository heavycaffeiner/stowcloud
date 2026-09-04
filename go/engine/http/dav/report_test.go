//go:build linux

package dav

import (
	"encoding/xml"
	"strings"
	"testing"
)

// Both query body shapes, against one parser.
//
// The two put the response property set in different places, and a DAV:prop
// anywhere else names what a comparison tests. Reading a filter's property as
// a response property loses the term: every query then arrives as an empty
// filter with a literal beside it, and a source can only guess from the
// literal's text what it was asked. A query for a file named "yes" is not a
// query for whatever "yes" means to some other property.
//
// The extension namespace here is a placeholder. Which vocabulary a mount
// carries belongs to the layer that speaks it; this package only decides
// which position a name arrived in.
const extNS = "urn:example:query"

// The RFC 3253 report shape: no DAV:select, and the response set sits
// directly under the document element.
const reportBodyShape = `<?xml version="1.0"?>
<x:filtered xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:prop><d:getetag/><x:size/></d:prop>
  <x:rules><x:starred>1</x:starred></x:rules>
</x:filtered>`

// The RFC 5323 search shape: the response set under DAV:select, and DAV:where
// carrying a DAV:prop of its own.
func searchBodyShape(prop, compare, literal string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/><x:id/></d:prop></d:select>
    <d:from><d:scope><d:href>/somewhere</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where>
      <d:` + compare + `>
        <d:prop><` + prop + `/></d:prop>
        <d:literal>` + literal + `</d:literal>
      </d:` + compare + `>
    </d:where>
  </d:basicsearch>
</d:searchrequest>`
}

func namesOf(names []xml.Name) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n.Space+" "+n.Local)
	}
	return out
}

func leafNamesOf(leaves []Leaf) []string {
	out := make([]string, 0, len(leaves))
	for _, l := range leaves {
		out = append(out, l.Name.Space+" "+l.Name.Local)
	}
	return out
}

func has(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func TestParseReportSeparatesResponsePropsFromFilterTerms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		// root is the document element's local name.
		root string
		// props and leaves are names that must be classified that way, given
		// as "space local".
		props  []string
		leaves []string
		// notProps names a filter term that must not be reported as a
		// response property, which is the misclassification under test.
		notProps []string
		// values are the leaf texts that must survive, keyed by leaf name.
		values map[string]string
	}{
		{
			name:  "the report shape keeps its top-level prop set",
			body:  reportBodyShape,
			root:  "filtered",
			props: []string{"DAV: getetag", extNS + " size"},
			// The rules element holds another element, so it is descended
			// into rather than captured whole.
			leaves:   []string{extNS + " starred"},
			notProps: []string{extNS + " starred"},
			values:   map[string]string{extNS + " starred": "1"},
		},
		{
			name:     "an extension property under where is a term",
			body:     searchBodyShape("x:starred", "eq", "yes"),
			root:     "searchrequest",
			props:    []string{"DAV: getetag", extNS + " id"},
			leaves:   []string{extNS + " starred", "DAV: literal"},
			notProps: []string{extNS + " starred"},
			values:   map[string]string{"DAV: literal": "yes"},
		},
		{
			name:     "a content type under where is a term",
			body:     searchBodyShape("d:getcontenttype", "like", "image/%"),
			root:     "searchrequest",
			props:    []string{"DAV: getetag"},
			leaves:   []string{"DAV: getcontenttype", "DAV: literal"},
			notProps: []string{"DAV: getcontenttype"},
			values:   map[string]string{"DAV: literal": "image/%"},
		},
		{
			name:     "a modification time under where is a term",
			body:     searchBodyShape("d:getlastmodified", "gt", "2026-08-28T00:00:00Z"),
			root:     "searchrequest",
			props:    []string{"DAV: getetag"},
			leaves:   []string{"DAV: getlastmodified", "DAV: literal"},
			notProps: []string{"DAV: getlastmodified"},
			values:   map[string]string{"DAV: literal": "2026-08-28T00:00:00Z"},
		},
		{
			name:     "a display name under where is a term",
			body:     searchBodyShape("d:displayname", "like", "%needle%"),
			root:     "searchrequest",
			props:    []string{"DAV: getetag"},
			leaves:   []string{"DAV: displayname", "DAV: literal"},
			notProps: []string{"DAV: displayname"},
			values:   map[string]string{"DAV: literal": "%needle%"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseReport(strings.NewReader(c.body), DefaultLimits())
			if err != nil {
				t.Fatalf("ParseReport: %v", err)
			}
			if got.Root.Local != c.root {
				t.Errorf("root is %q, want %q", got.Root.Local, c.root)
			}

			props, leaves := namesOf(got.Props), leafNamesOf(got.Leaves)
			for _, want := range c.props {
				if !has(props, want) {
					t.Errorf("%q is not a response property; props=%v", want, props)
				}
			}
			for _, want := range c.leaves {
				if !has(leaves, want) {
					t.Errorf("%q is not a filter term; leaves=%v", want, leaves)
				}
			}
			for _, unwanted := range c.notProps {
				if has(props, unwanted) {
					t.Errorf("the filter term %q was read as a response property; props=%v",
						unwanted, props)
				}
			}
			for name, want := range c.values {
				var found bool
				for _, l := range got.Leaves {
					if l.Name.Space+" "+l.Name.Local == name {
						found = true
						if l.Value != want {
							t.Errorf("leaf %q carries %q, want %q", name, l.Value, want)
						}
					}
				}
				if !found {
					t.Errorf("leaf %q is absent; leaves=%v", name, leaves)
				}
			}
		})
	}
}

// A comparison holding two terms, which is what a gallery view sends: one
// branch per media family under a single DAV:or.
func TestParseReportKeepsEveryTermOfADisjunction(t *testing.T) {
	t.Parallel()
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/></d:prop></d:select>
    <d:where>
      <d:or>
        <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%</d:literal></d:like>
        <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>video/%</d:literal></d:like>
      </d:or>
    </d:where>
  </d:basicsearch>
</d:searchrequest>`

	got, err := ParseReport(strings.NewReader(body), DefaultLimits())
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if props := namesOf(got.Props); len(props) != 1 || props[0] != "DAV: getetag" {
		t.Errorf("response props are %v, want only the etag", props)
	}

	var literals []string
	types := 0
	for _, l := range got.Leaves {
		switch {
		case isDavName(l.Name, "literal"):
			literals = append(literals, l.Value)
		case isDavName(l.Name, "getcontenttype"):
			types++
		}
	}
	if types != 2 {
		t.Errorf("the disjunction reported %d content-type terms, want 2", types)
	}
	if len(literals) != 2 || literals[0] != "image/%" || literals[1] != "video/%" {
		t.Errorf("literals are %v, want image and video in order", literals)
	}
}

// A DAV:prop under the document element still names the response set when the
// body also carries a filter that has one, so a body mixing the two shapes
// loses neither.
func TestParseReportTakesTheTopLevelPropSetBesideAFilterProp(t *testing.T) {
	t.Parallel()
	body := `<?xml version="1.0"?>
<x:filtered xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:prop><d:getetag/></d:prop>
  <d:where><d:eq><d:prop><x:starred/></d:prop><d:literal>yes</d:literal></d:eq></d:where>
</x:filtered>`

	got, err := ParseReport(strings.NewReader(body), DefaultLimits())
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if props := namesOf(got.Props); len(props) != 1 || props[0] != "DAV: getetag" {
		t.Errorf("response props are %v, want only the etag", props)
	}
	if leaves := leafNamesOf(got.Leaves); !has(leaves, extNS+" starred") {
		t.Errorf("the filter term is missing; leaves=%v", leaves)
	}
}

// Indented markup must not turn a container into a term. The peek that
// decides between the two skips the whitespace a formatted body puts between
// elements; taking it as text made every container a term whose value was its
// whole subtree.
func TestParseReportIgnoresWhitespaceBetweenElements(t *testing.T) {
	t.Parallel()
	body := "<?xml version=\"1.0\"?>\n" +
		"<x:filtered xmlns:d=\"DAV:\" xmlns:x=\"urn:example:query\">\n" +
		"\t<d:prop>\n\t\t<d:getetag/>\n\t</d:prop>\n" +
		"\t<x:rules>\n\t\t<x:starred>1</x:starred>\n\t</x:rules>\n" +
		"</x:filtered>"

	got, err := ParseReport(strings.NewReader(body), DefaultLimits())
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if props := namesOf(got.Props); len(props) != 1 || props[0] != "DAV: getetag" {
		t.Errorf("response props are %v, want only the etag", props)
	}
	leaves := leafNamesOf(got.Leaves)
	if len(leaves) != 1 || leaves[0] != extNS+" starred" {
		t.Fatalf("leaves are %v, want only the term inside the rules element", leaves)
	}
	if got.Leaves[0].Value != "1" {
		t.Errorf("the term carries %q, want %q", got.Leaves[0].Value, "1")
	}
}

// Which prop set the body states first must not decide what either one is.
//
// One pair of variables tracks the open response set, and it is cleared when
// that element closes. A filter's own DAV:prop opening or closing must not
// clear it, and a response set stated after a filter must still be read as
// one: the shapes do not fix the order, and a client that reorders them is
// not sending a different query.
func TestParseReportDoesNotDependOnTheOrderOfPropSets(t *testing.T) {
	t.Parallel()

	const whereClause = `<d:where><d:eq><d:prop><x:starred/></d:prop>` +
		`<d:literal>yes</d:literal></d:eq></d:where>`
	const selectClause = `<d:select><d:prop><d:getetag/><x:id/></d:prop></d:select>`

	cases := map[string]string{
		"search, select first": `<?xml version="1.0"?>
<d:searchrequest xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:basicsearch>` + selectClause + whereClause + `</d:basicsearch>
</d:searchrequest>`,
		"search, where first": `<?xml version="1.0"?>
<d:searchrequest xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:basicsearch>` + whereClause + selectClause + `</d:basicsearch>
</d:searchrequest>`,
		"report, prop first": `<?xml version="1.0"?>
<x:filtered xmlns:d="DAV:" xmlns:x="urn:example:query">
  <d:prop><d:getetag/><x:id/></d:prop>` + whereClause + `
</x:filtered>`,
		"report, where first": `<?xml version="1.0"?>
<x:filtered xmlns:d="DAV:" xmlns:x="urn:example:query">
  ` + whereClause + `<d:prop><d:getetag/><x:id/></d:prop>
</x:filtered>`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseReport(strings.NewReader(body), DefaultLimits())
			if err != nil {
				t.Fatalf("ParseReport: %v", err)
			}

			props := namesOf(got.Props)
			if len(props) != 2 || !has(props, "DAV: getetag") || !has(props, extNS+" id") {
				t.Errorf("response props are %v, want the etag and the id", props)
			}
			leaves := leafNamesOf(got.Leaves)
			if !has(leaves, extNS+" starred") {
				t.Errorf("the filter term is missing; leaves=%v", leaves)
			}
			if has(props, extNS+" starred") {
				t.Errorf("the filter term was read as a response property; props=%v", props)
			}
			for _, l := range got.Leaves {
				if isDavName(l.Name, "literal") && l.Value != "yes" {
					t.Errorf("the literal carries %q, want %q", l.Value, "yes")
				}
			}
		})
	}
}
