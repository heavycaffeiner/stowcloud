//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, body string) (PropFind, error) {
	t.Helper()
	return ParsePropFind(strings.NewReader(body), DefaultLimits())
}

// The mode table, exactly. Each row is a body a client actually sends and the
// one mode it means.
func TestThePropfindModeTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		want PropMode
	}{
		{"an empty body", ``, ModeAllProp},
		{"whitespace only", "  \n\t ", ModeAllProp},
		{"a declaration and nothing else", `<?xml version="1.0"?>`, ModeAllProp},
		{"allprop", `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`, ModeAllProp},
		{"propname", `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`, ModePropName},
		{"a named set", `<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`, ModeNamed},
		{
			"propname wins over allprop",
			`<D:propfind xmlns:D="DAV:"><D:allprop/><D:propname/></D:propfind>`,
			ModePropName,
		},
		{
			"and wins in the other order too",
			`<D:propfind xmlns:D="DAV:"><D:propname/><D:allprop/></D:propfind>`,
			ModePropName,
		},
		{
			"include does not change allprop",
			`<D:propfind xmlns:D="DAV:"><D:allprop/><D:include><D:quota-used-bytes/></D:include></D:propfind>`,
			ModeAllProp,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parse(t, c.body)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if got.Mode != c.want {
				t.Errorf("%s: got %s, want %s", c.name, got.Mode, c.want)
			}
		})
	}
}

// propname wins because it is the smaller disclosure. A body naming both did
// not say which it meant, and answering with allprop returns every value where
// the client may only have wanted the names.
func TestPropnameIsTheSmallerDisclosure(t *testing.T) {
	got, err := parse(t, `<D:propfind xmlns:D="DAV:"><D:allprop/><D:propname/></D:propfind>`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModePropName {
		t.Errorf("got %s, want propname", got.Mode)
	}
	if len(got.Names) != 0 {
		t.Errorf("propname carries names: %v", got.Names)
	}
}

// A named set keeps what was asked for, in order, once each.
func TestANamedSetCollapsesDuplicatesInFirstSeenOrder(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:"><D:prop>
		<D:getetag/><D:displayname/><D:getetag/><D:getcontentlength/><D:displayname/>
	</D:prop></D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"getetag", "displayname", "getcontentlength"}
	if len(got.Names) != len(want) {
		t.Fatalf("got %v, want %v", got.Names, want)
	}
	for i, name := range want {
		if got.Names[i].Local != name {
			t.Errorf("position %d: got %q, want %q", i, got.Names[i].Local, name)
		}
		if got.Names[i].Space != davNS {
			t.Errorf("%s: got namespace %q, want %q", name, got.Names[i].Space, davNS)
		}
	}
}

// A dead property in a foreign namespace is a name like any other, and the
// namespace is kept so two properties with one local name stay distinct.
func TestAForeignPropertyKeepsItsNamespace(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:" xmlns:V="urn:vendor"><D:prop>
		<D:getetag/><V:getetag/>
	</D:prop></D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Names) != 2 {
		t.Fatalf("two distinct properties collapsed into %v", got.Names)
	}
	if got.Names[0].Space == got.Names[1].Space {
		t.Errorf("both landed in %q", got.Names[0].Space)
	}
}

// include collects names without changing the mode away from allprop. Those
// names are what the response adds to the standard set.
func TestIncludeCollectsWithoutChangingTheMode(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:"><D:allprop/>
		<D:include><D:quota-used-bytes/><D:quota-available-bytes/></D:include>
	</D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeAllProp {
		t.Fatalf("include changed the mode to %s", got.Mode)
	}
	if len(got.Names) != 2 {
		t.Errorf("include collected %v, want two names", got.Names)
	}
}

// include with no allprop is still an allprop request. Reading it as a named
// set answers with only the included properties, which is not what a client
// asking to add to the standard set meant.
func TestIncludeAloneStaysAllprop(t *testing.T) {
	got, err := parse(t, `<D:propfind xmlns:D="DAV:"><D:include><D:x/></D:include></D:propfind>`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeAllProp {
		t.Errorf("include alone gave %s, want allprop", got.Mode)
	}
}

// A named set alongside allprop stays named. The client listed what it wants,
// and that list is the narrower of the two answers.
func TestANamedSetBeatsAllprop(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:"><D:allprop/><D:prop><D:getetag/></D:prop></D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeNamed {
		t.Errorf("got %s, want a named set", got.Mode)
	}
	if len(got.Names) != 1 || got.Names[0].Local != "getetag" {
		t.Errorf("got %v, want just getetag", got.Names)
	}
}

// prop and include answer different questions, so a body carrying both must
// not merge them. A named response returns what prop listed, never what
// include listed, and never the two together.
func TestPropAndIncludeStayApart(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:">
		<D:include><D:quota-used-bytes/></D:include>
		<D:prop><D:getetag/></D:prop>
	</D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeNamed {
		t.Fatalf("got %s, want a named set", got.Mode)
	}
	if len(got.Names) != 1 {
		t.Fatalf("got %v, want only what prop listed", got.Names)
	}
	if got.Names[0].Local != "getetag" {
		t.Errorf("got %q, want getetag", got.Names[0].Local)
	}
}

// An include list sent with propname is dropped: propname's response is the
// names of what exists, and a client-supplied list is not that.
func TestIncludeIsDroppedByPropname(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:"><D:include><D:x/></D:include><D:propname/></D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModePropName {
		t.Fatalf("got %s, want propname", got.Mode)
	}
	if len(got.Names) != 0 {
		t.Errorf("propname carries %v", got.Names)
	}
}

// The two lists have their own duplicate sets, so a name in both is not
// swallowed by the first one seen.
func TestANameInBothListsIsKeptInBoth(t *testing.T) {
	const body = `<D:propfind xmlns:D="DAV:">
		<D:include><D:getetag/></D:include>
		<D:prop><D:getetag/></D:prop>
	</D:propfind>`

	got, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Names) != 1 || got.Names[0].Local != "getetag" {
		t.Errorf("got %v, want getetag from the prop list", got.Names)
	}
}

// Inside prop, every element is a property name. A property whose name happens
// to be DAV:allprop must not switch the request to allprop and return every
// value the caller can see.
func TestAKeywordInsidePropIsAPropertyName(t *testing.T) {
	for _, keyword := range []string{"allprop", "propname", "include", "prop"} {
		t.Run(keyword, func(t *testing.T) {
			body := `<D:propfind xmlns:D="DAV:"><D:prop><D:` + keyword + `/></D:prop></D:propfind>`

			got, err := parse(t, body)
			if err != nil {
				t.Fatalf("%s: %v", keyword, err)
			}
			if got.Mode != ModeNamed {
				t.Errorf("a property named %s produced %s", keyword, got.Mode)
			}
			if len(got.Names) != 1 || got.Names[0].Local != keyword {
				t.Errorf("%s: got %v, want one name", keyword, got.Names)
			}
		})
	}
}

// The property limit refuses rather than truncating. A truncated list answers
// a request the client did not make.
func TestThePropertyLimitRefuses(t *testing.T) {
	lim := DefaultLimits()
	lim.Properties = 4

	body := `<D:propfind xmlns:D="DAV:"><D:prop>`
	for i := 0; i < 20; i++ {
		body += `<D:p` + string(rune('a'+i)) + `/>`
	}
	body += `</D:prop></D:propfind>`

	if _, err := ParsePropFind(strings.NewReader(body), lim); !errors.Is(err, ErrTooManyProperties) {
		t.Errorf("want a property-count refusal, got %v", err)
	}
}

// The scanner's defenses apply to this body like any other.
func TestPropfindInheritsTheXmlDefenses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"a doctype", `<!DOCTYPE d><D:propfind xmlns:D="DAV:"/>`, ErrDirective},
		{"an undeclared prefix", `<D:propfind><D:allprop/></D:propfind>`, ErrUndeclaredPrefix},
		{"an instruction", `<D:propfind xmlns:D="DAV:"><?x?></D:propfind>`, ErrProcInst},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parse(t, c.body); !errors.Is(err, c.want) {
				t.Errorf("want %v, got %v", c.want, err)
			}
		})
	}
}

// A depth outside what the operation accepts is refused, not clamped. A client
// asking for a depth-infinity DELETE and receiving a depth-zero one has
// deleted something other than what it named.
func TestAnUnacceptedDepthIsRefusedNotClamped(t *testing.T) {
	// PROPFIND takes all three; a lock takes zero or infinity only.
	if _, err := ParseDepth("1", DepthInfinity, DepthZero, DepthInfinity); !errors.Is(err, ErrBadDepth) {
		t.Errorf("depth 1 was accepted by an operation that takes 0 or infinity: %v", err)
	}
	if _, err := ParseDepth("infinity", DepthZero, DepthZero); !errors.Is(err, ErrBadDepth) {
		t.Errorf("infinity was accepted by a depth-zero-only operation: %v", err)
	}
}

// What the header accepts, and what it does not.
func TestTheDepthHeaderValues(t *testing.T) {
	all := []Depth{DepthZero, DepthOne, DepthInfinity}

	cases := []struct {
		value string
		want  Depth
		ok    bool
	}{
		{"0", DepthZero, true},
		{"1", DepthOne, true},
		{"infinity", DepthInfinity, true},
		{"Infinity", DepthInfinity, true},
		{"INFINITY", DepthInfinity, true},
		{" 1 ", DepthOne, true},
		{"2", 0, false},
		{"-1", 0, false},
		{"inf", 0, false},
		{"01", 0, false},
		{"true", 0, false},
	}

	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got, err := ParseDepth(c.value, DepthInfinity, all...)
			if c.ok {
				if err != nil {
					t.Fatalf("%q was refused: %v", c.value, err)
				}
				if got != c.want {
					t.Errorf("%q gave %s, want %s", c.value, got, c.want)
				}
				return
			}
			if !errors.Is(err, ErrBadDepth) {
				t.Errorf("%q: want a depth refusal, got %v (%s)", c.value, err, got)
			}
		})
	}
}

// An absent header takes the operation's own default, and the default still
// has to be one the operation accepts.
func TestAnAbsentDepthTakesTheDefault(t *testing.T) {
	got, err := ParseDepth("", DepthInfinity, DepthZero, DepthOne, DepthInfinity)
	if err != nil {
		t.Fatalf("an absent header was refused: %v", err)
	}
	if got != DepthInfinity {
		t.Errorf("got %s, want infinity", got)
	}

	if _, err := ParseDepth("", DepthInfinity, DepthZero); !errors.Is(err, ErrBadDepth) {
		t.Errorf("a default outside the accepted set was allowed through: %v", err)
	}
}

// Whatever the body, the parser returns a usable value or an error, never a
// mode carrying names it should not.
func FuzzPropFind(f *testing.F) {
	for _, seed := range []string{
		``,
		`<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:allprop/><D:include><D:x/></D:include></D:propfind>`,
		`<!DOCTYPE d><d/>`,
		`<D:prop>`,
	} {
		f.Add(seed)
	}

	lim := Limits{Bytes: 4096, Elements: 200, Depth: 16, NameBytes: 64, TextBytes: 2048, Properties: 32}

	f.Fuzz(func(t *testing.T, body string) {
		got, err := ParsePropFind(strings.NewReader(body), lim)
		if err != nil {
			return
		}

		if got.Mode == ModePropName && len(got.Names) != 0 {
			t.Errorf("%q gave propname with names %v", body, got.Names)
		}
		if len(got.Names) > lim.Properties {
			t.Errorf("%q gave %d names past the limit", body, len(got.Names))
		}

		seen := map[xml.Name]bool{}
		for _, n := range got.Names {
			if seen[n] {
				t.Errorf("%q repeated %v", body, n)
			}
			seen[n] = true
		}
	})
}

// A truncated body is refused and commits nothing. A client whose connection
// dropped mid-request must not have half of it applied, and a parser that
// returned what it read so far would do exactly that.
func TestATruncatedBodyDecidesNothing(t *testing.T) {
	bodies := []string{
		`<D:propfind xmlns:D="DAV:"><D:prop><D:a/>`,
		`<D:propfind xmlns:D="DAV:"><D:prop>`,
		`<D:propfind xmlns:D="DAV:">`,
		`<D:propfind`,
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			got, err := parse(t, body)
			if err == nil {
				t.Fatalf("a truncated body was accepted as %s", got.Mode)
			}
			if len(got.Names) != 0 {
				t.Errorf("a refused body returned names: %v", got.Names)
			}
		})
	}
}
