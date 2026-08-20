package dav

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The If header decides whether a write gets past a lock, so the two failure
// directions are what these test: honouring a lock nobody holds, and ignoring
// one somebody does.

func TestANoTagListParses(t *testing.T) {
	h, err := ParseIf(`(<urn:uuid:abc>)`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if len(h.Lists) != 1 || h.Lists[0].Tagged {
		t.Fatalf("lists = %+v, want one untagged list", h.Lists)
	}
	c := h.Lists[0].Conditions
	if len(c) != 1 || c[0].Kind != CondStateToken || c[0].Value != "urn:uuid:abc" || c[0].Not {
		t.Fatalf("conditions = %+v", c)
	}
}

func TestATaggedListCarriesItsResource(t *testing.T) {
	h, err := ParseIf(`</a/b.txt> (<urn:uuid:abc>)`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if len(h.Lists) != 1 || !h.Lists[0].Tagged || h.Lists[0].Tag != "/a/b.txt" {
		t.Fatalf("lists = %+v, want the tagged resource", h.Lists)
	}
}

func TestATagAppliesToEveryListAfterIt(t *testing.T) {
	h, err := ParseIf(`</a> (<urn:uuid:1>) (<urn:uuid:2>)`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if len(h.Lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(h.Lists))
	}
	for i, l := range h.Lists {
		if !l.Tagged || l.Tag != "/a" {
			t.Fatalf("lists[%d] = %+v, want the tag carried", i, l)
		}
	}
}

func TestNotNegatesAndDoesNotSwallowALongerWord(t *testing.T) {
	h, err := ParseIf(`(Not <urn:uuid:abc>)`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if !h.Lists[0].Conditions[0].Not {
		t.Fatal("Not was not applied")
	}

	// "Nothing" must not parse as "Not" followed by "hing", which would turn
	// a malformed header into a negated condition that quietly holds.
	if _, err := ParseIf(`(Nothing <urn:uuid:abc>)`); err == nil {
		t.Fatal("a word starting with Not was accepted as a negation")
	}
}

func TestAnEntityTagConditionParsesWithAndWithoutTheWeakMarker(t *testing.T) {
	h, err := ParseIf(`([W/"abc"] [ "def" ])`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	c := h.Lists[0].Conditions
	if len(c) != 2 {
		t.Fatalf("got %d conditions, want 2", len(c))
	}
	if c[0].Kind != CondETag || c[0].Value != "abc" || !c[0].Weak {
		t.Fatalf("conditions[0] = %+v, want a weak abc", c[0])
	}
	if c[1].Kind != CondETag || c[1].Value != "def" || c[1].Weak {
		t.Fatalf("conditions[1] = %+v, want a strong def", c[1])
	}
}

// Lists are OR-ed, conditions inside a list are AND-ed. RFC 4918 section
// 10.4.3, and getting it backwards inverts every decision the header makes.
func TestListsAreOredAndConditionsAreAnded(t *testing.T) {
	state := ResourceState{Tokens: []string{"urn:uuid:held"}, ETag: "v1", Exists: true}
	give := func(string, bool) ResourceState { return state }

	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"one list that holds", `(<urn:uuid:held>)`, true},
		{"one list that does not", `(<urn:uuid:other>)`, false},
		{"two lists, the second holds", `(<urn:uuid:other>) (<urn:uuid:held>)`, true},
		{"two lists, neither holds", `(<urn:uuid:a>) (<urn:uuid:b>)`, false},
		{"one list, both terms hold", `(<urn:uuid:held> ["v1"])`, true},
		{"one list, one term fails", `(<urn:uuid:held> ["v2"])`, false},
		{"a negation that holds", `(Not <urn:uuid:other>)`, true},
		{"a negation that does not", `(Not <urn:uuid:held>)`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ParseIf(tc.in)
			if err != nil {
				t.Fatalf("ParseIf(%q): %v", tc.in, err)
			}
			if got := h.Evaluate(give); got != tc.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A weak validator does not promise the bytes are unchanged, so it cannot
// satisfy a condition guarding a write.
func TestAWeakValidatorNeverSatisfiesACondition(t *testing.T) {
	weak := ResourceState{ETag: "v1", Weak: true, Exists: true}
	h, err := ParseIf(`(["v1"])`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if h.Evaluate(func(string, bool) ResourceState { return weak }) {
		t.Fatal("a weak validator satisfied an If condition")
	}
}

// A resource that is not there can only satisfy a negated condition.
func TestAMissingResourceOnlySatisfiesANegation(t *testing.T) {
	missing := ResourceState{Exists: false}
	positive, err := ParseIf(`(["v1"])`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if positive.Evaluate(func(string, bool) ResourceState { return missing }) {
		t.Fatal("a missing resource satisfied a positive condition")
	}
	negated, err := ParseIf(`(Not ["v1"])`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	if !negated.Evaluate(func(string, bool) ResourceState { return missing }) {
		t.Fatal("a missing resource did not satisfy a negated condition")
	}
}

// Tokens() drives the lock check, and a negated token is an assertion that the
// client does not hold it. Counting it as submitted is how a write slips past
// a lock it never had.
func TestTokensIgnoresNegatedConditions(t *testing.T) {
	h, err := ParseIf(`(<urn:uuid:held>) (Not <urn:uuid:notheld>)`)
	if err != nil {
		t.Fatalf("ParseIf: %v", err)
	}
	got := h.Tokens()
	if len(got) != 1 || got[0] != "urn:uuid:held" {
		t.Fatalf("Tokens() = %v, want only the asserted token", got)
	}
}

func TestMalformedHeadersAreRefused(t *testing.T) {
	for _, bad := range []string{
		"", "   ",
		"(", ")", "()",
		"<urn:uuid:a>",      // a tag with no list
		"<urn:uuid:a> junk", // a tag followed by something else
		"(<urn:uuid:a>",     // unterminated list
		"(<urn:uuid:a)",     // unterminated coded URL
		"([abc])",           // an entity tag that is not quoted
		`(["abc")`,          // no closing bracket
		"garbage",
		"(Not)",
	} {
		if _, err := ParseIf(bad); err == nil {
			t.Errorf("ParseIf(%q) was accepted", bad)
		}
	}
}

// D5: the three If bounds are what refuse.
func TestTheIfListBoundIsWhatRefuses(t *testing.T) {
	one := "(<urn:uuid:a>)"
	_, err := ParseIf(strings.Repeat(one, limits.DavIfLists+1))
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the list bound to refuse", err)
	}
}

func TestTheIfConditionBoundIsWhatRefuses(t *testing.T) {
	body := "(" + strings.Repeat("<urn:uuid:a> ", limits.DavIfConditions+1) + ")"
	_, err := ParseIf(body)
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the condition bound to refuse", err)
	}
}

func TestTheIfTokenLengthBoundIsWhatRefuses(t *testing.T) {
	long := "(<" + strings.Repeat("a", limits.DavIfTokenLength+1) + ">)"
	_, err := ParseIf(long)
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("err = %v, want the token-length bound to refuse", err)
	}
}

func TestParseDepthAcceptsOnlyTheThreeLegalValues(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", DepthInfinity},
		{"0", DepthZero},
		{"1", 1},
		{"infinity", DepthInfinity},
		{"Infinity", DepthInfinity},
		{" 0 ", DepthZero},
	} {
		got, err := ParseDepth(tc.in, DepthInfinity)
		if err != nil || got != tc.want {
			t.Fatalf("ParseDepth(%q) = %d, %v, want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"2", "-1", "yes", "0.5"} {
		if _, err := ParseDepth(bad, DepthZero); err == nil {
			t.Errorf("ParseDepth(%q) was accepted", bad)
		}
	}
}

// A Timeout the server will not grant is clamped rather than refused: RFC 4918
// makes the value a request, not a demand.
func TestParseTimeoutClampsRatherThanRefusing(t *testing.T) {
	if got := ParseTimeout("Second-30"); got.Seconds() != 30 {
		t.Fatalf("ParseTimeout = %v, want 30s", got)
	}
	if got := ParseTimeout("Infinite"); got != maxLockTimeout {
		t.Fatalf("Infinite = %v, want the maximum", got)
	}
	// A value past the ceiling is clamped, and a huge one must not overflow
	// into a negative duration.
	if got := ParseTimeout("Second-999999999999"); got != maxLockTimeout {
		t.Fatalf("a huge timeout = %v, want the maximum", got)
	}
	if got := ParseTimeout("nonsense"); got != defaultLockTimeout {
		t.Fatalf("an unparseable timeout = %v, want the default", got)
	}
}

func FuzzParseIf(f *testing.F) {
	for _, s := range []string{
		"", "()", "(<urn:uuid:a>)", `(["e"])`, `(Not <urn:uuid:a>)`,
		"</a> (<urn:uuid:a>)", "</a> (<urn:uuid:1>) (<urn:uuid:2>)",
		`(<urn:uuid:a> ["e"])`, `([W/"e"])`, "(Nothing <urn:uuid:a>)",
		"<", "(", "[", `("`, "</a>", "(())",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		h, err := ParseIf(in)
		if err != nil {
			return
		}
		// A header that parsed must be evaluable without panicking, and must
		// hold at least one list: an empty header decides nothing and would
		// silently pass every guard it was asked about.
		if len(h.Lists) == 0 {
			t.Fatalf("ParseIf(%q) returned no lists", in)
		}
		if len(h.Lists) > limits.DavIfLists {
			t.Fatalf("ParseIf(%q) returned %d lists, past the bound", in, len(h.Lists))
		}
		for _, l := range h.Lists {
			if len(l.Conditions) == 0 {
				t.Fatalf("ParseIf(%q) produced an empty list", in)
			}
			for _, c := range l.Conditions {
				if len(c.Value) > limits.DavIfTokenLength {
					t.Fatalf("a condition value is %d bytes, past the bound", len(c.Value))
				}
			}
		}
		h.Evaluate(func(string, bool) ResourceState { return ResourceState{} })
		// Every token reported must have come from a non-negated condition.
		for _, tok := range h.Tokens() {
			if tok == "" {
				t.Fatalf("ParseIf(%q) reported an empty token", in)
			}
		}
	})
}
