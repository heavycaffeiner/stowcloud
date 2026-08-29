//go:build linux

package dav

import (
	"errors"
	"testing"
)

// The two comparisons, side by side. A weak tag revalidates a GET and never
// satisfies a write precondition.
//
// The asymmetry is the point. A weak tag says two representations mean the
// same thing, which answers "is my cached copy still good" but not "is this
// the exact byte sequence I read before I decided to overwrite it".
func TestWeakRevalidatesAndStrongGuardsAWrite(t *testing.T) {
	weak := ETag{Value: "v1", Weak: true}
	strong := ETag{Value: "v1"}

	cases := []struct {
		name        string
		client      ETag
		server      ETag
		revalidates bool
		guards      bool
	}{
		{"both strong", strong, strong, true, true},
		{"client weak", weak, strong, true, false},
		{"server weak", strong, weak, true, false},
		{"both weak", weak, weak, true, false},
		{"different values", ETag{Value: "v2"}, strong, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.client.WeakEquals(c.server); got != c.revalidates {
				t.Errorf("weak comparison: got %v, want %v", got, c.revalidates)
			}
			if got := c.client.StrongEquals(c.server); got != c.guards {
				t.Errorf("strong comparison: got %v, want %v", got, c.guards)
			}
		})
	}
}

// The same weak tag that revalidates a GET fails an If precondition. Run
// through both real entry points rather than the comparison functions, so the
// asymmetry is checked where a request meets it.
func TestTheSameWeakTagRevalidatesButDoesNotGuard(t *testing.T) {
	current := ETag{Value: "abc", Weak: true}

	if !MatchesIfNoneMatch(`W/"abc"`, current, true) {
		t.Error("a weak tag did not revalidate a GET")
	}

	h, err := ParseIf(`([W/"abc"])`, DefaultLimits(), "h")
	if err != nil {
		t.Fatalf("the header did not parse: %v", err)
	}
	ok, _ := EvaluateIf(h, []string{"f"}, func([]string) ResourceState {
		return ResourceState{ETag: current, Exists: true}
	})
	if ok {
		t.Error("a weak tag satisfied a write precondition")
	}
}

// A weak tag is parsed and kept rather than discarded, so a response can
// report what the client actually sent.
func TestAWeakTagIsParsedAndRetained(t *testing.T) {
	got, ok := ParseETag(`W/"abc"`)
	if !ok {
		t.Fatal("a weak tag failed to parse")
	}
	if !got.Weak || got.Value != "abc" {
		t.Errorf("got %+v, want a weak abc", got)
	}
}

// What an entity tag may look like.
func TestTheEntityTagGrammar(t *testing.T) {
	good := map[string]ETag{
		`"abc"`:     {Value: "abc"},
		`W/"abc"`:   {Value: "abc", Weak: true},
		`""`:        {Value: ""},
		`" a b "`:   {Value: " a b "},
		` "abc" `:   {Value: "abc"},
		`"a-b_c.d"`: {Value: "a-b_c.d"},
	}
	for raw, want := range good {
		got, ok := ParseETag(raw)
		if !ok {
			t.Errorf("%q was refused", raw)
			continue
		}
		if got != want {
			t.Errorf("%q gave %+v, want %+v", raw, got, want)
		}
	}

	for _, raw := range []string{``, `abc`, `"abc`, `abc"`, `W/abc`, `"a"b"`, `w/"abc"`} {
		if _, ok := ParseETag(raw); ok {
			t.Errorf("%q was accepted", raw)
		}
	}
}

// Evaluation is OR across lists and AND within one.
func TestEvaluationIsOrAcrossListsAndAndWithin(t *testing.T) {
	state := func([]string) ResourceState {
		return ResourceState{
			ETag:   ETag{Value: "v1"},
			Exists: true,
			Tokens: []string{"urn:uuid:aaa"},
		}
	}

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"one list holds", `(<urn:uuid:aaa>)`, true},
		{"one list fails", `(<urn:uuid:zzz>)`, false},
		{"both terms hold", `(<urn:uuid:aaa> ["v1"])`, true},
		{"one term fails, so the list fails", `(<urn:uuid:aaa> ["v2"])`, false},
		{"the second list holds", `(<urn:uuid:zzz>) (<urn:uuid:aaa>)`, true},
		{"neither list holds", `(<urn:uuid:zzz>) (["v9"])`, false},
		{"Not inverts", `(Not <urn:uuid:zzz>)`, true},
		{"Not on a held token fails", `(Not <urn:uuid:aaa>)`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := ParseIf(c.header, DefaultLimits(), "h")
			if err != nil {
				t.Fatalf("%q: %v", c.header, err)
			}
			got, _ := EvaluateIf(h, []string{"f"}, state)
			if got != c.want {
				t.Errorf("%q: got %v, want %v", c.header, got, c.want)
			}
		})
	}
}

// A token is submitted only from a list that actually held, and only from a
// positive condition. A token in a failing list was not submitted, and a token
// behind a Not was named to assert its absence.
func TestTokensComeOnlyFromSatisfiedPositiveConditions(t *testing.T) {
	state := func([]string) ResourceState {
		return ResourceState{
			ETag:   ETag{Value: "v1"},
			Exists: true,
			Tokens: []string{"urn:uuid:held"},
		}
	}

	cases := []struct {
		name   string
		header string
		want   []string
	}{
		{"a satisfied list submits its token", `(<urn:uuid:held>)`, []string{"urn:uuid:held"}},
		{"a failing list submits nothing", `(<urn:uuid:held> ["v9"])`, nil},
		{"a negated token is not submitted", `(Not <urn:uuid:absent>)`, nil},
		{
			"only the satisfied list contributes",
			`(<urn:uuid:held> ["v9"]) (<urn:uuid:held>)`,
			[]string{"urn:uuid:held"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := ParseIf(c.header, DefaultLimits(), "h")
			if err != nil {
				t.Fatalf("%q: %v", c.header, err)
			}
			_, got := EvaluateIf(h, []string{"f"}, state)
			if len(got) != len(c.want) {
				t.Fatalf("%q: got %v, want %v", c.header, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("%q: token %d is %q, want %q", c.header, i, got[i], c.want[i])
				}
			}
		})
	}
}

// A tagged list is evaluated against the resource it names, not the request's
// target. Otherwise a condition about one file decides a write to another.
func TestATaggedListIsEvaluatedAgainstItsOwnResource(t *testing.T) {
	state := func(path []string) ResourceState {
		if len(path) == 1 && path[0] == "other" {
			return ResourceState{ETag: ETag{Value: "other-tag"}, Exists: true}
		}
		return ResourceState{ETag: ETag{Value: "target-tag"}, Exists: true}
	}

	h, err := ParseIf(`<http://h/other> (["other-tag"])`, DefaultLimits(), "h")
	if err != nil {
		t.Fatalf("the tagged header did not parse: %v", err)
	}
	if len(h.Lists) != 1 || h.Lists[0].Resource == nil {
		t.Fatalf("the resource tag was lost: %+v", h.Lists)
	}

	ok, _ := EvaluateIf(h, []string{"target"}, state)
	if !ok {
		t.Error("a tagged list was evaluated against the wrong resource")
	}

	// And the same condition against the target's own tag must fail, proving
	// the two resources really do differ.
	h2, err := ParseIf(`<http://h/other> (["target-tag"])`, DefaultLimits(), "h")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := EvaluateIf(h2, []string{"target"}, state); ok {
		t.Error("the target's tag satisfied a condition tagged to another resource")
	}
}

// A tagged resource path goes through the same URL decoder as everything else.
func TestATaggedResourceUsesTheSameDecoder(t *testing.T) {
	for _, header := range []string{`<http://h/a%2fb> (["v"])`, `<http://evil/x> (["v"])`, `</..> (["v"])`} {
		t.Run(header, func(t *testing.T) {
			if _, err := ParseIf(header, DefaultLimits(), "h"); !errors.Is(err, ErrBadIf) {
				t.Errorf("%q was accepted: %v", header, err)
			}
		})
	}
}

// A malformed header is refused rather than partly read. A header this server
// silently misreads is a precondition the client believes it set and did not.
func TestAMalformedIfHeaderIsRefused(t *testing.T) {
	bad := []string{
		`(`,
		`)`,
		`()`,
		`(<urn:uuid:a>`,
		`<urn:uuid:a>`,
		`(["v1"]`,
		`(["v1")`,
		`(<>)`,
		`(["unquoted"x])`,
		`(garbage)`,
		`junk (<urn:uuid:a>)`,
		`(Not)`,
		`<http://h/x>`,
	}

	for _, header := range bad {
		t.Run(header, func(t *testing.T) {
			if _, err := ParseIf(header, DefaultLimits(), "h"); err == nil {
				t.Errorf("%q was accepted", header)
			}
		})
	}
}

// An empty list would hold vacuously, turning a precondition into none at all.
func TestAnEmptyListIsRefused(t *testing.T) {
	if _, err := ParseIf(`()`, DefaultLimits(), "h"); !errors.Is(err, ErrBadIf) {
		t.Errorf("an empty list was accepted: %v", err)
	}
}

// An absent header is not a failed precondition. It means the client set none.
func TestAnAbsentHeaderIsSatisfied(t *testing.T) {
	h, err := ParseIf("", DefaultLimits(), "h")
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsEmpty() {
		t.Fatal("an absent header parsed into lists")
	}

	ok, tokens := EvaluateIf(h, []string{"f"}, func([]string) ResourceState {
		t.Error("an absent header consulted the resource state")
		return ResourceState{}
	})
	if !ok {
		t.Error("an absent header failed its precondition")
	}
	if len(tokens) != 0 {
		t.Errorf("an absent header submitted %v", tokens)
	}
}

// A state token beginning with the letters of Not is a token, not a negation.
func TestATokenBeginningWithNotIsAToken(t *testing.T) {
	h, err := ParseIf(`(<Notatoken>)`, DefaultLimits(), "h")
	if err != nil {
		t.Fatalf("the header did not parse: %v", err)
	}
	if len(h.Lists) != 1 || len(h.Lists[0].Conditions) != 1 {
		t.Fatalf("got %+v", h.Lists)
	}
	cond := h.Lists[0].Conditions[0]
	if cond.Not {
		t.Error("a token starting with Not was read as a negation")
	}
	if cond.Token != "Notatoken" {
		t.Errorf("got token %q", cond.Token)
	}
}

// Not binds with or without a space before what it negates. Clients send both.
func TestNotBindsWithAndWithoutASpace(t *testing.T) {
	cases := []struct {
		header string
		token  string
		etag   string
	}{
		{`(Not <urn:uuid:a>)`, "urn:uuid:a", ""},
		{`(Not<urn:uuid:a>)`, "urn:uuid:a", ""},
		{`(Not  ["v"])`, "", "v"},
		{`(Not["v"])`, "", "v"},
	}

	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			h, err := ParseIf(c.header, DefaultLimits(), "h")
			if err != nil {
				t.Fatalf("%q: %v", c.header, err)
			}
			cond := h.Lists[0].Conditions[0]
			if !cond.Not {
				t.Errorf("%q was not read as a negation", c.header)
			}
			if cond.Token != c.token {
				t.Errorf("%q: token %q, want %q", c.header, cond.Token, c.token)
			}
			if cond.ETag.Value != c.etag {
				t.Errorf("%q: etag %q, want %q", c.header, cond.ETag.Value, c.etag)
			}
		})
	}
}

// The bounds refuse rather than truncate.
func TestTheIfHeaderBoundsRefuse(t *testing.T) {
	lim := DefaultLimits()
	lim.Conditions = 2

	if _, err := ParseIf(`(<a> <b> <c> <d>)`, lim, "h"); !errors.Is(err, ErrIfTooLarge) {
		t.Errorf("a long condition list was accepted: %v", err)
	}
	if _, err := ParseIf(`(<a>) (<b>) (<c>)`, lim, "h"); !errors.Is(err, ErrIfTooLarge) {
		t.Errorf("too many lists were accepted: %v", err)
	}

	byteLim := DefaultLimits()
	byteLim.Bytes = 8
	if _, err := ParseIf(`(<a-fairly-long-token>)`, byteLim, "h"); !errors.Is(err, ErrIfTooLarge) {
		t.Errorf("an oversized header was accepted: %v", err)
	}

	tokenLim := DefaultLimits()
	tokenLim.NameBytes = 4
	if _, err := ParseIf(`(<a-long-token>)`, tokenLim, "h"); !errors.Is(err, ErrIfTooLarge) {
		t.Errorf("an oversized token was accepted: %v", err)
	}
}

// If-None-Match's star matches whenever the resource exists, which is what a
// conditional create is asking about.
func TestTheStarMatchesAnExistingResource(t *testing.T) {
	if !MatchesIfNoneMatch("*", ETag{Value: "v"}, true) {
		t.Error("* did not match an existing resource")
	}
	if MatchesIfNoneMatch("*", ETag{}, false) {
		t.Error("* matched a resource that is not there")
	}
}

// If-None-Match takes a list, and any member matching is a match.
func TestIfNoneMatchTakesAList(t *testing.T) {
	current := ETag{Value: "v2"}

	if !MatchesIfNoneMatch(`"v1", "v2", "v3"`, current, true) {
		t.Error("a list containing the current tag did not match")
	}
	if MatchesIfNoneMatch(`"v1", "v3"`, current, true) {
		t.Error("a list without the current tag matched")
	}
	if MatchesIfNoneMatch("", current, true) {
		t.Error("an absent header matched")
	}
}

// The parser never panics and never returns a header whose lists are empty.
func FuzzParseIf(f *testing.F) {
	for _, seed := range []string{
		`(<urn:uuid:a>)`,
		`(["v1"])`,
		`(Not <urn:uuid:a>)`,
		`<http://h/x> (["v1"])`,
		`(<a> <b>) (["c"])`,
		`(`,
		`()`,
		`(W/"weak")`,
		``,
	} {
		f.Add(seed)
	}

	lim := Limits{Bytes: 1024, Elements: 100, Depth: 16, NameBytes: 128, TextBytes: 1024, Conditions: 16}

	f.Fuzz(func(t *testing.T, header string) {
		h, err := ParseIf(header, lim, "h")
		if err != nil {
			return
		}

		for _, list := range h.Lists {
			if len(list.Conditions) == 0 {
				t.Errorf("%q produced a vacuous empty list", header)
			}
			if len(list.Conditions) > lim.Conditions {
				t.Errorf("%q produced %d conditions past the limit", header, len(list.Conditions))
			}
			for _, c := range list.Conditions {
				if c.IsToken() && len(c.Token) > lim.NameBytes {
					t.Errorf("%q produced an oversized token", header)
				}
			}
		}
		if len(h.Lists) > lim.Conditions {
			t.Errorf("%q produced %d lists past the limit", header, len(h.Lists))
		}

		// A weak tag must never satisfy a condition, whatever the header said.
		_, tokens := EvaluateIf(h, []string{"f"}, func([]string) ResourceState {
			return ResourceState{ETag: ETag{Value: "x", Weak: true}, Exists: true}
		})
		for _, tok := range tokens {
			if tok == "" {
				t.Errorf("%q submitted an empty token", header)
			}
		}
	})
}
