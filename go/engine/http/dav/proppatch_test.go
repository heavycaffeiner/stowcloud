//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

func patch(t *testing.T, body string) (PropPatch, error) {
	t.Helper()
	return ParsePropPatch(strings.NewReader(body), DefaultLimits())
}

// liveSet returns a predicate over local names.
func liveSet(names ...string) IsLive {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name xml.Name) bool { return name.Space == davNS && set[name.Local] }
}

// One live property refuses the whole request. That property reports 403 and
// every other one reports 424, and nothing is written.
//
// A partial commit would leave the resource in a state the client did not ask
// for and cannot work out from the response, since the response says what each
// property got but never what the resource now holds.
func TestOneLivePropertyRefusesTheWholeTransaction(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop>
		<V:author>me</V:author>
		<D:getetag>forged</D:getetag>
		<V:colour>red</V:colour>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Instructions) != 3 {
		t.Fatalf("parsed %d instructions, want 3", len(p.Instructions))
	}

	plan := PlanPropPatch(p, liveSet("getetag"))
	if plan.Commit {
		t.Error("a request touching a live property would have committed")
	}

	want := []int{StatusFailedDependency, StatusForbidden, StatusFailedDependency}
	if len(plan.Outcomes) != len(want) {
		t.Fatalf("got %d outcomes, want %d", len(plan.Outcomes), len(want))
	}
	for i, code := range want {
		if plan.Outcomes[i].Status != code {
			t.Errorf("%s: got %d, want %d", plan.Outcomes[i].Name.Local, plan.Outcomes[i].Status, code)
		}
	}
}

// The 403 lands on the live property itself, not on the first one in the
// request. A client fixing the wrong property retries and fails again.
func TestTheRefusalNamesTheLiveProperty(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop>
		<V:a>1</V:a><V:b>2</V:b><D:resourcetype/><V:c>3</V:c>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}

	plan := PlanPropPatch(p, liveSet("resourcetype"))
	for _, o := range plan.Outcomes {
		if o.Status == StatusForbidden && o.Name.Local != "resourcetype" {
			t.Errorf("403 landed on %q", o.Name.Local)
		}
		if o.Name.Local == "resourcetype" && o.Status != StatusForbidden {
			t.Errorf("the live property got %d", o.Status)
		}
	}
}

// A request of only dead properties commits, and every one reports 200.
func TestADeadOnlyRequestCommits(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v">
		<D:set><D:prop><V:a>1</V:a></D:prop></D:set>
		<D:remove><D:prop><V:b/></D:prop></D:remove>
	</D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}

	plan := PlanPropPatch(p, liveSet("getetag", "resourcetype"))
	if !plan.Commit {
		t.Fatal("a dead-only request did not commit")
	}
	for _, o := range plan.Outcomes {
		if o.Status != StatusOK {
			t.Errorf("%q got %d in a committing request", o.Name.Local, o.Status)
		}
	}
}

// Document order is preserved. A set followed by a remove of the same property
// leaves it absent; the other order leaves it set. Grouping by operation would
// silently pick one of the two.
func TestDocumentOrderIsPreserved(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []PropOp
	}{
		{
			name: "set then remove",
			body: `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v">
				<D:set><D:prop><V:a>1</V:a></D:prop></D:set>
				<D:remove><D:prop><V:a/></D:prop></D:remove>
			</D:propertyupdate>`,
			want: []PropOp{OpSet, OpRemove},
		},
		{
			name: "remove then set",
			body: `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v">
				<D:remove><D:prop><V:a/></D:prop></D:remove>
				<D:set><D:prop><V:a>1</V:a></D:prop></D:set>
			</D:propertyupdate>`,
			want: []PropOp{OpRemove, OpSet},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := patch(t, c.body)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Instructions) != len(c.want) {
				t.Fatalf("got %d instructions, want %d", len(p.Instructions), len(c.want))
			}
			for i, op := range c.want {
				if p.Instructions[i].Op != op {
					t.Errorf("position %d: got %s, want %s", i, p.Instructions[i].Op, op)
				}
			}
		})
	}
}

// A property is read for its text, never for its markup. Whatever a client
// nests inside a value is dropped, so nothing it sent can be replayed into a
// later response as markup.
func TestAValueKeepsTextAndDropsMarkup(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop>
		<V:a>before<V:nested attr="x">inner</V:nested>after</V:a>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Instructions) != 1 {
		t.Fatalf("nested markup produced %d instructions, want 1", len(p.Instructions))
	}

	got := p.Instructions[0].Value
	if strings.Contains(got, "<") || strings.Contains(got, "attr") {
		t.Errorf("markup survived into the value: %q", got)
	}
	for _, want := range []string{"before", "inner", "after"} {
		if !strings.Contains(got, want) {
			t.Errorf("the value lost %q: %q", want, got)
		}
	}
}

// A nested element's end tag does not close the property. Otherwise the rest
// of the value becomes a second property with a name the client never sent.
func TestNestedMarkupDoesNotSplitAProperty(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop>
		<V:a><b><c/></b>tail</V:a><V:z>2</V:z>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Instructions) != 2 {
		t.Fatalf("got %d instructions, want a and z", len(p.Instructions))
	}
	if p.Instructions[0].Name.Local != "a" || p.Instructions[1].Name.Local != "z" {
		t.Errorf("got %q and %q", p.Instructions[0].Name.Local, p.Instructions[1].Name.Local)
	}
}

// A name that cannot be written back as a tag is refused before it is stored.
// Storing it would break every later PROPFIND on the resource, a failure the
// client that caused it never sees.
func TestANameThatCannotBeRenderedIsRefused(t *testing.T) {
	bad := []string{"", "1abc", "-x", ".x", "a b", "a<b", "a>b", "a&b", `a"b`, "a:b", "a/b"}
	for _, name := range bad {
		if ValidPropertyName(xml.Name{Space: "urn:v", Local: name}) {
			t.Errorf("%q was accepted as a property name", name)
		}
	}

	good := []string{"a", "_a", "a1", "a-b", "a.b", "author", "\u00e9t\u00e9"}
	for _, name := range good {
		if !ValidPropertyName(xml.Name{Space: "urn:v", Local: name}) {
			t.Errorf("%q was refused as a property name", name)
		}
	}
}

// Whatever the parser accepts, the validator has to accept: otherwise a
// perfectly well-formed request is parsed and then rejected for a name the
// client had no way to spell differently.
func TestEveryParsedNamePassesTheValidator(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v" xmlns:W="urn:w"><D:set><D:prop>
		<V:author>a</V:author>
		<W:_private>b</W:_private>
		<V:x-custom.v2>c</V:x-custom.v2>
		<V:` + "\u00e9t\u00e9" + `>d</V:` + "\u00e9t\u00e9" + `>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Instructions) != 4 {
		t.Fatalf("parsed %d instructions, want 4", len(p.Instructions))
	}
	for _, in := range p.Instructions {
		if !ValidPropertyName(in.Name) {
			t.Errorf("the parser accepted %q but the validator refuses it", in.Name.Local)
		}
	}
}

// A qualified name reaches the validator already split, so its local half
// carries no colon. The colon check guards a name arriving from storage or
// from a caller that did not go through the parser.
func TestAQualifiedNameArrivesSplit(t *testing.T) {
	const body = `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>
		<a:b xmlns:a="urn:a">1</a:b>
	</D:prop></D:set></D:propertyupdate>`

	p, err := patch(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Instructions) != 1 {
		t.Fatalf("got %d instructions, want 1", len(p.Instructions))
	}
	if got := p.Instructions[0].Name; got.Local != "b" || got.Space != "urn:a" {
		t.Errorf("got local %q space %q, want b and urn:a", got.Local, got.Space)
	}
}

// An empty request commits nothing, since there is nothing to commit.
func TestAnEmptyRequestCommitsNothing(t *testing.T) {
	p, err := patch(t, `<D:propertyupdate xmlns:D="DAV:"/>`)
	if err != nil {
		t.Fatal(err)
	}

	plan := PlanPropPatch(p, liveSet("getetag"))
	if plan.Commit {
		t.Error("an empty request reported a commit")
	}
	if len(plan.Outcomes) != 0 {
		t.Errorf("an empty request produced outcomes: %v", plan.Outcomes)
	}
}

// The property count refuses rather than truncating.
func TestThePropPatchCountLimitRefuses(t *testing.T) {
	lim := DefaultLimits()
	lim.Properties = 3

	body := `<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop>`
	for i := 0; i < 10; i++ {
		body += `<V:p` + string(rune('a'+i)) + `>x</V:p` + string(rune('a'+i)) + `>`
	}
	body += `</D:prop></D:set></D:propertyupdate>`

	if _, err := ParsePropPatch(strings.NewReader(body), lim); !errors.Is(err, ErrTooManyProperties) {
		t.Errorf("want a property-count refusal, got %v", err)
	}
}

// The scanner's defenses apply here too.
func TestProppatchInheritsTheXmlDefenses(t *testing.T) {
	if _, err := patch(t, `<!DOCTYPE d><D:propertyupdate xmlns:D="DAV:"/>`); !errors.Is(err, ErrDirective) {
		t.Errorf("a doctype was accepted: %v", err)
	}
	if _, err := patch(t, `<D:propertyupdate><D:set/></D:propertyupdate>`); !errors.Is(err, ErrUndeclaredPrefix) {
		t.Errorf("an undeclared prefix was accepted: %v", err)
	}
}

// Whatever the body, a plan either commits everything or nothing, and never
// mixes a 200 with a refusal.
func FuzzPropPatchPlan(f *testing.F) {
	for _, seed := range []string{
		`<D:propertyupdate xmlns:D="DAV:"/>`,
		`<D:propertyupdate xmlns:D="DAV:" xmlns:V="urn:v"><D:set><D:prop><V:a>1</V:a></D:prop></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:getetag>x</D:getetag></D:prop></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:remove><D:prop><D:x/></D:prop></D:remove></D:propertyupdate>`,
		`<D:set>`,
	} {
		f.Add(seed)
	}

	lim := Limits{Bytes: 4096, Elements: 200, Depth: 16, NameBytes: 64, TextBytes: 2048, Properties: 32}
	live := liveSet("getetag", "resourcetype", "getcontentlength")

	f.Fuzz(func(t *testing.T, body string) {
		p, err := ParsePropPatch(strings.NewReader(body), lim)
		if err != nil {
			return
		}

		plan := PlanPropPatch(p, live)
		if len(plan.Outcomes) != len(p.Instructions) {
			t.Errorf("%q: %d outcomes for %d instructions", body, len(plan.Outcomes), len(p.Instructions))
		}

		ok, refused := 0, 0
		for _, o := range plan.Outcomes {
			switch o.Status {
			case StatusOK:
				ok++
			case StatusForbidden, StatusFailedDependency:
				refused++
			default:
				t.Errorf("%q: unexpected status %d", body, o.Status)
			}
		}
		if ok > 0 && refused > 0 {
			t.Errorf("%q: a plan mixed %d accepted with %d refused", body, ok, refused)
		}
		if plan.Commit != (ok > 0) {
			t.Errorf("%q: commit %v with %d accepted", body, plan.Commit, ok)
		}
	})
}

// A truncated body commits nothing. Applying the instructions read so far
// leaves the resource in a state the client never asked for, and its retry
// then applies them a second time.
func TestATruncatedProppatchCommitsNothing(t *testing.T) {
	bodies := []string{
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:a>v</D:a>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>`,
		`<D:propertyupdate xmlns:D="DAV:"><D:set>`,
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			got, err := patch(t, body)
			if err == nil {
				t.Fatalf("a truncated body was accepted with %d instructions", len(got.Instructions))
			}
			if len(got.Instructions) != 0 {
				t.Errorf("a refused body returned %d instructions", len(got.Instructions))
			}

			plan := PlanPropPatch(got, liveSet("getetag"))
			if plan.Commit {
				t.Error("a refused body produced a committing plan")
			}
		})
	}
}
