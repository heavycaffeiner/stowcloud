package dav

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The two hard refusals. Both arrive as ordinary tokens from encoding/xml, so
// silence would be acceptance rather than an oversight.

func TestADoctypeIsRefusedEvenWithNoEntities(t *testing.T) {
	// The rule is "no DTD", not "no DTD that looked dangerous". This one
	// declares nothing at all and is still refused.
	body := []byte(`<?xml version="1.0"?><!DOCTYPE propfind><D:propfind xmlns:D="DAV:"/>`)
	if _, err := ParsePropFind(body, Limits{}); !errors.Is(err, ErrDTDForbidden) {
		t.Fatalf("a DOCTYPE returned %v, want ErrDTDForbidden", err)
	}
}

func TestTheClassicXXEBodyIsRefused(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<!DOCTYPE p [<!ENTITY x SYSTEM "file:///etc/passwd">]>
<D:propfind xmlns:D="DAV:"><D:prop><D:x>&x;</D:x></D:prop></D:propfind>`)
	if _, err := ParsePropFind(body, Limits{}); !errors.Is(err, ErrDTDForbidden) {
		t.Fatalf("an XXE body returned %v, want ErrDTDForbidden", err)
	}
}

// Billion laughs never gets to expand, because the DTD carrying it is refused
// before any entity is defined.
func TestBillionLaughsIsRefusedAtTheDoctype(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<!DOCTYPE lolz [
 <!ENTITY lol "lol">
 <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<D:propfind xmlns:D="DAV:"><D:prop><D:x>&lol3;</D:x></D:prop></D:propfind>`)
	if _, err := ParsePropFind(body, Limits{}); !errors.Is(err, ErrDTDForbidden) {
		t.Fatalf("a billion-laughs body returned %v, want ErrDTDForbidden", err)
	}
}

func TestAProcessingInstructionIsRefused(t *testing.T) {
	// The XML declaration is a ProcInst to encoding/xml but is not one to the
	// spec, so it has to be accepted while a real PI is refused. That
	// distinction is the whole point of this pair.
	body := []byte(`<?xml-stylesheet href="x.xsl"?><D:propfind xmlns:D="DAV:"/>`)
	if _, err := ParsePropFind(body, Limits{}); !errors.Is(err, ErrPIForbidden) {
		t.Fatalf("a processing instruction returned %v, want ErrPIForbidden", err)
	}
}

func TestTheXMLDeclarationIsNotMistakenForAProcessingInstruction(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`)
	got, err := ParsePropFind(body, Limits{})
	if err != nil {
		t.Fatalf("a declared document was refused: %v", err)
	}
	if got.Mode != PropFindAllProp {
		t.Fatalf("mode = %v, want allprop", got.Mode)
	}
}

// D5: each bound is proved to be the thing that refuses, at the bound.

func TestTheElementBoundIsWhatRefuses(t *testing.T) {
	const bound = 16
	body := `<D:propfind xmlns:D="DAV:"><D:prop>`
	for i := 0; i < bound; i++ {
		body += fmt.Sprintf(`<D:p%d/>`, i)
	}
	body += `</D:prop></D:propfind>`

	_, err := ParsePropFind([]byte(body), Limits{Elements: bound})
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the element bound to refuse", err)
	}
	var e *limits.Exceeded
	if !errors.As(err, &e) || e.Bound != bound {
		t.Fatalf("the refusal does not name the element bound: %v", err)
	}

	// One fewer element is accepted, so the bound is what refused and not
	// something else about the document.
	ok := `<D:propfind xmlns:D="DAV:"><D:prop>`
	for i := 0; i < bound-3; i++ {
		ok += fmt.Sprintf(`<D:p%d/>`, i)
	}
	ok += `</D:prop></D:propfind>`
	if _, err := ParsePropFind([]byte(ok), Limits{Elements: bound}); err != nil {
		t.Fatalf("a document under the bound was refused: %v", err)
	}
}

func TestTheDepthBoundIsWhatRefuses(t *testing.T) {
	const bound = 8
	deep := `<D:propfind xmlns:D="DAV:">` + strings.Repeat(`<D:a>`, bound) +
		strings.Repeat(`</D:a>`, bound) + `</D:propfind>`

	_, err := ParsePropFind([]byte(deep), Limits{Depth: bound})
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the depth bound to refuse", err)
	}
	var e *limits.Exceeded
	if !errors.As(err, &e) || e.Bound != bound {
		t.Fatalf("the refusal does not name the depth bound: %v", err)
	}

	shallow := `<D:propfind xmlns:D="DAV:">` + strings.Repeat(`<D:a>`, bound-2) +
		strings.Repeat(`</D:a>`, bound-2) + `</D:propfind>`
	if _, err := ParsePropFind([]byte(shallow), Limits{Depth: bound}); err != nil {
		t.Fatalf("a document under the depth bound was refused: %v", err)
	}
}

func TestTheNameLengthBoundIsWhatRefuses(t *testing.T) {
	const bound = 32
	long := strings.Repeat("n", bound+1)
	body := fmt.Sprintf(`<D:propfind xmlns:D="DAV:"><D:prop><D:%s/></D:prop></D:propfind>`, long)

	_, err := ParsePropFind([]byte(body), Limits{NameLength: bound})
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the name-length bound to refuse", err)
	}

	okBody := fmt.Sprintf(`<D:propfind xmlns:D="DAV:"><D:prop><D:%s/></D:prop></D:propfind>`,
		strings.Repeat("n", bound))
	if _, err := ParsePropFind([]byte(okBody), Limits{NameLength: bound}); err != nil {
		t.Fatalf("a name at the bound was refused: %v", err)
	}
}

// The text bound is on the accumulated value, because a value arrives in as
// many fragments as it has entity references. A per-fragment bound would let a
// client send an unbounded value in bounded pieces.
func TestTheTextBoundCountsTheJoinedValue(t *testing.T) {
	const bound = 64
	body := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><x xmlns="u:">` +
		strings.Repeat("a&amp;", bound) +
		`</x></D:prop></D:set></D:propertyupdate>`

	_, err := ParsePropPatch([]byte(body), Limits{TextBytes: bound})
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the text bound to refuse a value sent in fragments", err)
	}
}

// Namespace handling: a prefix is never what is compared.

func TestAnyPrefixBoundToDavIsTheSameDocument(t *testing.T) {
	for _, body := range []string{
		`<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`,
		`<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`,
		`<a:propfind xmlns:a="DAV:"><a:prop><a:getetag/></a:prop></a:propfind>`,
		`<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`,
	} {
		got, err := ParsePropFind([]byte(body), Limits{})
		if err != nil {
			t.Fatalf("%s\nwas refused: %v", body, err)
		}
		if got.Mode != PropFindNamed || len(got.Props) != 1 || !got.Props[0].IsDav("getetag") {
			t.Fatalf("%s\nparsed as %+v, want the one DAV:getetag", body, got)
		}
	}
}

func TestAPrefixBoundElsewhereIsNotDav(t *testing.T) {
	// A client that binds D: to something else is not talking about DAV:, and
	// a scanner comparing prefixes would get this backwards.
	body := `<D:propfind xmlns:D="urn:not-dav"><D:prop><D:getetag/></D:prop></D:propfind>`
	if _, err := ParsePropFind([]byte(body), Limits{}); !errors.Is(err, ErrBadXML) {
		t.Fatalf("err = %v, want the root refused as not DAV:propfind", err)
	}
}

func TestAnUndeclaredPrefixIsRefused(t *testing.T) {
	// On the root, which was already refused before the scanner checked
	// prefixes at all: an unresolved D: is not DAV:, so the root is wrong.
	// That is a real refusal and it is not this rule.
	body := `<D:propfind><D:prop><D:getetag/></D:prop></D:propfind>`
	if _, err := ParsePropFind([]byte(body), Limits{}); err == nil {
		t.Fatal("an undeclared prefix was accepted")
	}

	// On an inner element, which is the case the specification's own suite
	// exercises and this build used to answer 207 to. The document is
	// namespace-malformed and the answer is 400, whatever the property means.
	//
	// encoding/xml does not report it: an undeclared prefix arrives with Space
	// set to the prefix itself, so {bar}foo is indistinguishable from a
	// properly declared namespace unless the declarations are tracked.
	inner := `<propfind xmlns="DAV:"><prop><bar:foo/></prop></propfind>`
	if _, err := ParsePropFind([]byte(inner), Limits{}); !errors.Is(err, ErrUndeclaredPrefix) {
		t.Fatalf("err = %v, want the undeclared prefix refused", err)
	}
}

// The declaration does not have to be on the element that uses it: a prefix
// bound anywhere above is bound here, and refusing that would refuse most real
// documents, which declare everything on the root.
func TestAPrefixDeclaredOnAnAncestorIsBound(t *testing.T) {
	body := `<propfind xmlns="DAV:" xmlns:x="urn:example"><prop><x:thing/></prop></propfind>`
	got, err := ParsePropFind([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("a prefix declared on the root was refused: %v", err)
	}
	if len(got.Props) != 1 || got.Props[0].Space != "urn:example" {
		t.Fatalf("parsed as %+v, want the one urn:example property", got.Props)
	}
}

// The two prefixes the specification binds without a declaration. Refusing
// either would refuse a legal document.
func TestTheReservedPrefixesNeedNoDeclaration(t *testing.T) {
	body := `<propfind xmlns="DAV:"><prop><thing xml:lang="en"/></prop></propfind>`
	if _, err := ParsePropFind([]byte(body), Limits{}); err != nil {
		t.Fatalf("xml: was refused: %v", err)
	}
}

// The text-fragment rule: accumulate, then trim once. Trimming each fragment
// is how "Tom &amp; Jerry" becomes "Tom&Jerry".
func TestTextIsTrimmedOnceAndNotPerFragment(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>
	  <author xmlns="u:">  Tom &amp; Jerry  </author>
	</D:prop></D:set></D:propertyupdate>`

	ops, err := ParsePropPatch([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropPatch: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d operations, want 1", len(ops))
	}
	if ops[0].Value != "Tom & Jerry" {
		t.Fatalf("value = %q, want %q: the spaces around the entity were eaten",
			ops[0].Value, "Tom & Jerry")
	}
}

func TestCharacterReferencesSurviveIntact(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<x xmlns="u:">a &lt; b &amp;&amp; c &gt; d</x>` +
		`</D:prop></D:set></D:propertyupdate>`
	ops, err := ParsePropPatch([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropPatch: %v", err)
	}
	if ops[0].Value != "a < b && c > d" {
		t.Fatalf("value = %q, want the references resolved once", ops[0].Value)
	}
}

// An undefined entity has no definition to find, because the only construct
// that could define one is refused. It must not silently vanish.
func TestAnUndefinedEntityIsRefusedRatherThanDropped(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<x xmlns="u:">&mystery;</x></D:prop></D:set></D:propertyupdate>`
	if _, err := ParsePropPatch([]byte(body), Limits{}); err == nil {
		t.Fatal("an undefined entity was accepted, so the value silently lost content")
	}
}

// PROPFIND shapes.

func TestAnEmptyBodyMeansAllprop(t *testing.T) {
	for _, body := range []string{"", "   ", "\n\t\r\n"} {
		got, err := ParsePropFind([]byte(body), Limits{})
		if err != nil {
			t.Fatalf("an empty body was refused: %v", err)
		}
		if got.Mode != PropFindAllProp {
			t.Fatalf("mode = %v, want allprop", got.Mode)
		}
	}
}

func TestPropnameWinsOverAllprop(t *testing.T) {
	// propname asks for a strictly smaller answer: names without values.
	// Resolving the conflict the other way would disclose more than asked.
	body := `<D:propfind xmlns:D="DAV:"><D:allprop/><D:propname/></D:propfind>`
	got, err := ParsePropFind([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropFind: %v", err)
	}
	if got.Mode != PropFindPropName {
		t.Fatalf("mode = %v, want propname", got.Mode)
	}
}

func TestANamedPropfindKeepsDocumentOrderAndDropsDuplicates(t *testing.T) {
	body := `<D:propfind xmlns:D="DAV:"><D:prop>` +
		`<D:getetag/><D:getcontentlength/><D:getetag/>` +
		`<v:starred xmlns:v="urn:example:vendor"/>` +
		`</D:prop></D:propfind>`
	got, err := ParsePropFind([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropFind: %v", err)
	}
	want := []Name{
		DavName("getetag"),
		DavName("getcontentlength"),
		{Space: "urn:example:vendor", Local: "starred"},
	}
	if len(got.Props) != len(want) {
		t.Fatalf("props = %v, want %v", got.Props, want)
	}
	for i := range want {
		if got.Props[i] != want[i] {
			t.Fatalf("props[%d] = %v, want %v", i, got.Props[i], want[i])
		}
	}
}

func TestAWrongRootIsRefused(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:"><D:prop/></D:propertyupdate>`
	if _, err := ParsePropFind([]byte(body), Limits{}); !errors.Is(err, ErrBadXML) {
		t.Fatalf("err = %v, want ErrBadXML for the wrong root", err)
	}
}

// PROPPATCH ordering. RFC 4918 applies operations in document order, and a set
// followed by a remove is not the same as the reverse.
func TestPropPatchKeepsDocumentOrderAcrossSetAndRemove(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:">` +
		`<D:set><D:prop><a xmlns="u:">1</a></D:prop></D:set>` +
		`<D:remove><D:prop><b xmlns="u:"/></D:prop></D:remove>` +
		`<D:set><D:prop><c xmlns="u:">3</c></D:prop></D:set>` +
		`</D:propertyupdate>`
	ops, err := ParsePropPatch([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropPatch: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("got %d operations, want 3", len(ops))
	}
	if ops[0].Name.Local != "a" || ops[0].Value != "1" || ops[0].Remove {
		t.Fatalf("ops[0] = %+v, want a set of a", ops[0])
	}
	if ops[1].Name.Local != "b" || !ops[1].Remove {
		t.Fatalf("ops[1] = %+v, want a remove of b", ops[1])
	}
	if ops[2].Name.Local != "c" || ops[2].Value != "3" || ops[2].Remove {
		t.Fatalf("ops[2] = %+v, want a set of c", ops[2])
	}
}

func TestAnEmptySetElementIsASetOfEmpty(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<x xmlns="u:"/></D:prop></D:set></D:propertyupdate>`
	ops, err := ParsePropPatch([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParsePropPatch: %v", err)
	}
	if len(ops) != 1 || ops[0].Remove || ops[0].Value != "" {
		t.Fatalf("ops = %+v, want one set to the empty value", ops)
	}
}

// REPORT: the root is read, DAV:prop is understood, and every other leaf is
// carried verbatim without this package learning what it means.
func TestAReportCollectsVendorFiltersVerbatim(t *testing.T) {
	body := `<v:filter-files xmlns:v="urn:example:vendor" xmlns:D="DAV:">` +
		`<D:prop><D:getetag/><v:fileid/></D:prop>` +
		`<v:filter-rules><v:starred>1</v:starred></v:filter-rules>` +
		`</v:filter-files>`
	got, err := ParseReport([]byte(body), Limits{})
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.Root.Local != "filter-files" || got.Root.Space != "urn:example:vendor" {
		t.Fatalf("root = %v, want the vendor filter-files", got.Root)
	}
	if len(got.Props) != 2 || !got.Props[0].IsDav("getetag") || got.Props[1].Local != "fileid" {
		t.Fatalf("props = %v, want getetag and fileid", got.Props)
	}
	var starred *Leaf
	for i := range got.Leaves {
		if got.Leaves[i].Name.Local == "starred" {
			starred = &got.Leaves[i]
		}
	}
	if starred == nil || starred.Value != "1" {
		t.Fatalf("leaves = %+v, want the starred filter carried verbatim", got.Leaves)
	}
	if starred.Name.Space != "urn:example:vendor" {
		t.Fatalf("the filter lost its namespace: %v", starred.Name)
	}
}

// Escaping. The order is what makes a file named "a&b" reachable.
func TestAnHrefIsPercentEncodedThenXMLEscaped(t *testing.T) {
	got := EscapeHref("/share/a&b.txt")
	if got != "/share/a%26b.txt" {
		t.Fatalf("EscapeHref = %q, want the ampersand percent-encoded once", got)
	}
	// The other order would produce this, which addresses a file nobody has.
	if strings.Contains(got, "%26amp;") {
		t.Fatal("the escape ran before the encode, so the href names the wrong file")
	}
}

func TestHrefEncodingCoversTheCharactersThatBreakXML(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a b", "/a%20b"},
		{"/a<b", "/a%3Cb"},
		{"/a>b", "/a%3Eb"},
		{`/a"b`, "/a%22b"},
		{"/a'b", "/a%27b"},
		{"/a%b", "/a%25b"},
		{"/a#b", "/a%23b"},
		{"/a?b", "/a%3Fb"},
		{"/keep-._~@+,/x", "/keep-._~@+,/x"},
	} {
		if got := EscapeHref(tc.in); got != tc.want {
			t.Fatalf("EscapeHref(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNonASCIIIsPercentEncodedPerByte(t *testing.T) {
	// RFC 3986 encodes the UTF-8 bytes, not the rune.
	if got := EscapeHref("/λε.txt"); got != "/%CE%BB%CE%B5.txt" {
		t.Fatalf("EscapeHref = %q, want each UTF-8 byte encoded", got)
	}
}

func TestEscapeTextClosesTheObviousInjection(t *testing.T) {
	got := EscapeText(`</D:href><D:evil/>`)
	if strings.Contains(got, "<D:evil") || strings.Contains(got, "</D:href>") {
		t.Fatalf("EscapeText = %q, want the markup neutralised", got)
	}
}
