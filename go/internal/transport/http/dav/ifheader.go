//go:build linux

// The If header, and the two ETag comparisons that are deliberately different.
//
// If-None-Match on a GET uses weak comparison, so a revalidation succeeds
// against a semantically equal representation. The If header on a write uses
// strong comparison, so a weak ETag never satisfies a precondition: a weak tag
// says two representations mean the same thing, which is not enough to know
// that a lost update did not happen in between.
package dav

import (
	"errors"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrBadIf reports an If header that does not parse.
	ErrBadIf = errors.New("a malformed If header")
	// ErrIfTooLarge reports an If header past its bounds.
	ErrIfTooLarge = errors.New("the If header is too large")
)

// ETag is one entity tag.
type ETag struct {
	// Value is the tag without its quotes.
	Value string
	// Weak is whether the tag carried a W/ prefix.
	Weak bool
}

// StrongEquals reports the strong comparison: both tags must be strong and
// equal. Used for write preconditions, where a weak match is not evidence that
// the bytes are unchanged.
func (e ETag) StrongEquals(other ETag) bool {
	return !e.Weak && !other.Weak && e.Value == other.Value
}

// WeakEquals reports the weak comparison: equal values, whatever the strength.
// Used for revalidation, where a semantically equal representation is what the
// client is asking about.
func (e ETag) WeakEquals(other ETag) bool {
	return e.Value == other.Value
}

// ParseETag reads one entity tag.
func ParseETag(raw string) (ETag, bool) {
	raw = strings.TrimSpace(raw)

	weak := false
	if strings.HasPrefix(raw, "W/") {
		weak = true
		raw = raw[len("W/"):]
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return ETag{}, false
	}
	value := raw[1 : len(raw)-1]
	if strings.ContainsAny(value, `"`) {
		return ETag{}, false
	}
	return ETag{Value: value, Weak: weak}, true
}

// MatchesIfNoneMatch reports whether a revalidation header matches.
//
// Weak comparison, because the client is asking whether its cached copy still
// means the same thing. "*" matches whenever the resource exists.
func MatchesIfNoneMatch(header string, current ETag, exists bool) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return exists
	}

	for _, part := range splitList(header) {
		tag, ok := ParseETag(part)
		if ok && tag.WeakEquals(current) {
			return true
		}
	}
	return false
}

// Condition is a single test a list applies.
type Condition struct {
	// Not inverts this term.
	Not bool
	// Token is a state token, empty when this is an ETag condition.
	Token string
	// ETag is the tag, when Token is empty.
	ETag ETag
}

// IsToken reports whether this condition names a lock token.
func (c Condition) IsToken() bool { return c.Token != "" }

// ConditionList is one parenthesised group. Every member must hold.
type ConditionList struct {
	// Resource is the tagged resource path, empty for an untagged list.
	Resource []string
	// Conditions are the terms, all of which must hold.
	Conditions []Condition
}

// IfHeader is a parsed header. Any one list holding satisfies it.
type IfHeader struct {
	// Lists are the groups, in order.
	Lists []ConditionList
}

// IsEmpty reports whether no If header was sent.
func (h IfHeader) IsEmpty() bool { return len(h.Lists) == 0 }

// ParseIf reads the header's bounded grammar.
//
// The grammar is a sequence of lists, each optionally preceded by a resource
// tag. Anything the grammar does not describe is refused rather than skipped:
// a header this server silently misreads is a precondition the client believes
// it set and did not.
func ParseIf(header string, lim Limits, requestHost string) (IfHeader, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return IfHeader{}, nil
	}
	if lim.Bytes > 0 && int64(len(header)) > lim.Bytes {
		return IfHeader{}, ErrIfTooLarge
	}

	var (
		out      IfHeader
		resource []string
		tagged   bool
		i        int
	)

	for i < len(header) {
		switch header[i] {
		case ' ', '\t':
			i++

		case '<':
			// A resource tag applies to the lists that follow it, until the
			// next tag.
			end := strings.IndexByte(header[i:], '>')
			if end < 0 {
				return IfHeader{}, ErrBadIf
			}
			raw := header[i+1 : i+end]
			segs, err := ParseDestination(raw, requestHost)
			if err != nil {
				return IfHeader{}, ErrBadIf
			}
			resource, tagged = segs, true
			i += end + 1

		case '(':
			list, next, err := parseList(header, i, lim)
			if err != nil {
				return IfHeader{}, err
			}
			if tagged {
				list.Resource = resource
			}
			if len(out.Lists) >= lim.Conditions {
				return IfHeader{}, ErrIfTooLarge
			}
			out.Lists = append(out.Lists, list)
			i = next

		default:
			return IfHeader{}, ErrBadIf
		}
	}

	if len(out.Lists) == 0 {
		return IfHeader{}, ErrBadIf
	}
	return out, nil
}

// parseList reads one parenthesised group starting at open.
func parseList(header string, open int, lim Limits) (ConditionList, int, error) {
	var list ConditionList

	i := open + 1
	for i < len(header) {
		switch header[i] {
		case ' ', '\t':
			i++

		case ')':
			if len(list.Conditions) == 0 {
				// An empty list holds vacuously, which would turn a
				// precondition into no precondition at all.
				return ConditionList{}, 0, ErrBadIf
			}
			return list, i + 1, nil

		default:
			cond, next, err := parseCondition(header, i, lim)
			if err != nil {
				return ConditionList{}, 0, err
			}
			if len(list.Conditions) >= lim.Conditions {
				return ConditionList{}, 0, ErrIfTooLarge
			}
			list.Conditions = append(list.Conditions, cond)
			i = next
		}
	}
	return ConditionList{}, 0, ErrBadIf
}

// parseCondition reads one term, with its optional Not.
func parseCondition(header string, start int, lim Limits) (Condition, int, error) {
	var cond Condition

	i := start
	if rest := header[i:]; len(rest) >= 3 && strings.EqualFold(rest[:3], "Not") {
		// Only when what follows opens a condition or is whitespace. Clients
		// send "Not <token>" and "Not<token>" both, so the separator is
		// optional. Widening this set to anything would be harmless in
		// practice, since a condition can only start with < or [ and the
		// default branch refuses everything else, but keeping it narrow means
		// the negation is decided here rather than two branches away.
		if len(rest) == 3 || rest[3] == ' ' || rest[3] == '\t' || rest[3] == '<' || rest[3] == '[' {
			cond.Not = true
			i += 3
			for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
				i++
			}
		}
	}
	if i >= len(header) {
		return Condition{}, 0, ErrBadIf
	}

	switch header[i] {
	case '<':
		end := strings.IndexByte(header[i:], '>')
		if end < 0 {
			return Condition{}, 0, ErrBadIf
		}
		token := header[i+1 : i+end]
		if token == "" {
			return Condition{}, 0, ErrBadIf
		}
		if lim.NameBytes > 0 && len(token) > lim.NameBytes {
			return Condition{}, 0, ErrIfTooLarge
		}
		cond.Token = token
		return cond, i + end + 1, nil

	case '[':
		end := strings.IndexByte(header[i:], ']')
		if end < 0 {
			return Condition{}, 0, ErrBadIf
		}
		tag, ok := ParseETag(header[i+1 : i+end])
		if !ok {
			return Condition{}, 0, ErrBadIf
		}
		cond.ETag = tag
		return cond, i + end + 1, nil

	default:
		return Condition{}, 0, ErrBadIf
	}
}

// ResourceState is what one target currently holds.
type ResourceState struct {
	// ETag is the resource's current tag.
	ETag ETag
	// Exists is whether it is there at all.
	Exists bool
	// Tokens are the lock tokens held on it.
	Tokens []string
}

// StateOf resolves a target path to its state.
type StateOf func(path []string) ResourceState

// EvaluateIf reports whether the header is satisfied, and which lock tokens
// the satisfying lists submitted.
//
// OR across lists, AND within one. Tokens are collected only from positive
// state-token conditions in a list that actually held: a token named inside a
// list that failed was not submitted, and a token behind a Not was named to
// assert its absence.
func EvaluateIf(h IfHeader, defaultTarget []string, state StateOf) (bool, []string) {
	if h.IsEmpty() {
		return true, nil
	}

	var submitted []string
	satisfied := false

	for _, list := range h.Lists {
		target := defaultTarget
		if list.Resource != nil {
			target = list.Resource
		}
		res := state(target)

		holds := true
		for _, cond := range list.Conditions {
			if !conditionHolds(cond, res) {
				holds = false
				break
			}
		}
		if !holds {
			continue
		}

		satisfied = true
		for _, cond := range list.Conditions {
			if cond.IsToken() && !cond.Not {
				submitted = append(submitted, cond.Token)
			}
		}
	}

	return satisfied, submitted
}

// conditionHolds evaluates one term against a resource.
func conditionHolds(cond Condition, res ResourceState) bool {
	var held bool
	if cond.IsToken() {
		for _, t := range res.Tokens {
			if t == cond.Token {
				held = true
				break
			}
		}
	} else {
		// Strong comparison: a weak tag never satisfies a write precondition.
		held = res.Exists && cond.ETag.StrongEquals(res.ETag)
	}

	if cond.Not {
		return !held
	}
	return held
}

// splitList splits a comma-separated header value.
func splitList(header string) []string {
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
