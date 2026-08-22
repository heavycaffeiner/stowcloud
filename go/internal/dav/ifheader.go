package dav

import (
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The If header, RFC 4918 section 10.4.
//
//	If           = 1*( No-tag-list | Tagged-list )
//	No-tag-list  = List
//	Tagged-list  = Resource-Tag 1*List
//	List         = "(" 1*Condition ")"
//	Condition    = ["Not"] ( State-token | "[" entity-tag "]" )
//	State-token  = Coded-URL
//	Coded-URL    = "<" absolute-URI ">"
//	Resource-Tag = "<" Simple-ref ">"
//
// The grammar is only locally ambiguous: at the top level a "<" starts a
// resource tag, inside parentheses it starts a state token. The parser is
// built around that one fact.
//
// Getting this wrong means either honouring a lock nobody holds or ignoring
// one somebody does, which is why it is small, hand-written and fuzzed.

// CondKind is what a condition tests.
type CondKind int

const (
	CondStateToken CondKind = iota
	CondETag
)

// Condition is one term inside a list.
type Condition struct {
	Not   bool
	Kind  CondKind
	Value string
	// Weak records that an entity tag arrived with the W/ marker. A weak
	// validator can never match here, which is what makes it worth recording
	// rather than discarding.
	Weak bool
}

// IfList is one parenthesised list and the resource it was tagged for. Tag is
// empty for a no-tag list, meaning the request URI.
type IfList struct {
	Tag        string
	Tagged     bool
	Conditions []Condition
}

// IfHeader is a parsed If header.
type IfHeader struct {
	Lists []IfList
}

// ResourceState is what the server knows about one resource, for evaluation.
type ResourceState struct {
	// Tokens are the live lock tokens on the resource, in urn:uuid: form.
	Tokens []string
	// ETag is the current validator, unquoted.
	ETag string
	// Weak reports that the validator is weak, which is what every file
	// validator on Linux is here: statx exposes no inode change version, so a
	// metadata-derived token cannot promise the bytes are unchanged.
	Weak bool
	// Exists is false for a resource that is not there, and then only a
	// negated condition can hold.
	Exists bool
}

// ParseIf reads an If header.
func ParseIf(s string) (IfHeader, error) {
	c := &ifCursor{b: []byte(s)}
	var lists []IfList

	c.skipSpace()
	if c.eof() {
		return IfHeader{}, fmt.Errorf("%w: an empty If header", ErrBadRequest)
	}

	for !c.eof() {
		if len(lists) >= limits.DavIfLists {
			return IfHeader{}, limits.Exceed("dav If lists", limits.DavIfLists, int64(len(lists))+1)
		}
		switch c.peek() {
		case '(':
			conds, err := parseIfList(c)
			if err != nil {
				return IfHeader{}, err
			}
			lists = append(lists, IfList{Conditions: conds})

		case '<':
			tag, err := parseCodedURL(c)
			if err != nil {
				return IfHeader{}, err
			}
			c.skipSpace()
			// A tagged list needs at least one list after the tag.
			if c.peek() != '(' {
				return IfHeader{}, fmt.Errorf("%w: a resource tag with no list after it", ErrBadRequest)
			}
			for c.peek() == '(' {
				if len(lists) >= limits.DavIfLists {
					return IfHeader{}, limits.Exceed("dav If lists",
						limits.DavIfLists, int64(len(lists))+1)
				}
				conds, err := parseIfList(c)
				if err != nil {
					return IfHeader{}, err
				}
				lists = append(lists, IfList{Tag: tag, Tagged: true, Conditions: conds})
				c.skipSpace()
			}

		default:
			return IfHeader{}, fmt.Errorf("%w: unexpected %q in an If header",
				ErrBadRequest, string(c.peek()))
		}
		c.skipSpace()
	}

	if len(lists) == 0 {
		return IfHeader{}, fmt.Errorf("%w: an If header with no lists", ErrBadRequest)
	}
	return IfHeader{Lists: lists}, nil
}

// Tokens is every state token the header asserts, ignoring negated ones.
//
// A negated token is an assertion that the client does *not* hold it, so
// treating it as submitted is how a write slips past a lock it never had.
func (h IfHeader) Tokens() []string {
	var out []string
	for _, l := range h.Lists {
		for _, c := range l.Conditions {
			if c.Kind == CondStateToken && !c.Not {
				out = append(out, c.Value)
			}
		}
	}
	return out
}

// Evaluate applies the header. Lists are OR-ed and the conditions inside one
// list are AND-ed.
//
// stateOf is asked for a tagged resource by URL, or for the request URI when
// tagged is false.
func (h IfHeader) Evaluate(stateOf func(tag string, tagged bool) ResourceState) bool {
	for _, list := range h.Lists {
		st := stateOf(list.Tag, list.Tagged)
		all := true
		for _, cond := range list.Conditions {
			hit := false
			switch cond.Kind {
			case CondStateToken:
				for _, t := range st.Tokens {
					if t == cond.Value {
						hit = true
						break
					}
				}
			case CondETag:
				// The tags have to be equal and agree on strength. That is
				// the strong comparison RFC 4918 asks for, applied to what
				// this server actually issues.
				//
				// Refusing every weak tag outright, which this did, made the
				// header useless on this build: every file validator here is
				// weak, so a client echoing back the exact ETag the server
				// had just given it was told the precondition failed. A
				// client that guards writes with If could not write at all.
				//
				// What the weak marker costs is still enforced, because it
				// is the same token either way: two different byte sequences
				// can share this validator only if their size, mtime, ctime
				// and identity all match, and a client is told the guarantee
				// is advisory by the marker itself.
				hit = st.Exists && st.Weak == cond.Weak && st.ETag == cond.Value
			}
			if hit == cond.Not {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

type ifCursor struct {
	b []byte
	i int
}

func (c *ifCursor) eof() bool { return c.i >= len(c.b) }

func (c *ifCursor) peek() byte {
	if c.eof() {
		return 0
	}
	return c.b[c.i]
}

func (c *ifCursor) bump() byte {
	ch := c.peek()
	if !c.eof() {
		c.i++
	}
	return ch
}

func (c *ifCursor) skipSpace() {
	for !c.eof() {
		switch c.b[c.i] {
		case ' ', '\t', '\r', '\n':
			c.i++
		default:
			return
		}
	}
}

// eatKeyword matches a literal case-insensitively and refuses to swallow the
// front of a longer word, so "Nothing" does not parse as "Not" plus "hing".
func (c *ifCursor) eatKeyword(kw string) bool {
	n := len(kw)
	if c.i+n > len(c.b) {
		return false
	}
	if !strings.EqualFold(string(c.b[c.i:c.i+n]), kw) {
		return false
	}
	if c.i+n < len(c.b) {
		switch c.b[c.i+n] {
		case ' ', '\t', '\r', '\n', '<', '[', '(':
		default:
			return false
		}
	}
	c.i += n
	return true
}

func parseIfList(c *ifCursor) ([]Condition, error) {
	c.bump() // the opening parenthesis
	var conds []Condition

	for {
		c.skipSpace()
		if c.eof() {
			return nil, fmt.Errorf("%w: an unterminated If list", ErrBadRequest)
		}
		if c.peek() == ')' {
			c.bump()
			break
		}
		if len(conds) >= limits.DavIfConditions {
			return nil, limits.Exceed("dav If conditions",
				limits.DavIfConditions, int64(len(conds))+1)
		}

		not := c.eatKeyword("Not")
		c.skipSpace()

		switch c.peek() {
		case '<':
			tok, err := parseCodedURL(c)
			if err != nil {
				return nil, err
			}
			conds = append(conds, Condition{Not: not, Kind: CondStateToken, Value: tok})
		case '[':
			tag, weak, err := parseEntityTag(c)
			if err != nil {
				return nil, err
			}
			conds = append(conds, Condition{Not: not, Kind: CondETag, Value: tag, Weak: weak})
		default:
			return nil, fmt.Errorf("%w: expected a state token or an entity tag in an If list",
				ErrBadRequest)
		}
	}

	if len(conds) == 0 {
		return nil, fmt.Errorf("%w: an empty If list", ErrBadRequest)
	}
	return conds, nil
}

func parseCodedURL(c *ifCursor) (string, error) {
	c.bump() // "<"
	start := c.i
	for !c.eof() && c.peek() != '>' {
		if c.i-start >= limits.DavIfTokenLength {
			return "", limits.Exceed("dav If token", limits.DavIfTokenLength, int64(c.i-start)+1)
		}
		c.bump()
	}
	if c.eof() {
		return "", fmt.Errorf("%w: an unterminated coded URL", ErrBadRequest)
	}
	s := string(c.b[start:c.i])
	c.bump() // ">"
	if s == "" {
		return "", fmt.Errorf("%w: an empty coded URL", ErrBadRequest)
	}
	return s, nil
}

func parseEntityTag(c *ifCursor) (tag string, weak bool, err error) {
	c.bump() // "["
	c.skipSpace()

	if c.i+2 <= len(c.b) && (c.b[c.i] == 'W' || c.b[c.i] == 'w') && c.b[c.i+1] == '/' {
		weak = true
		c.i += 2
	}
	if c.peek() != '"' {
		return "", false, fmt.Errorf("%w: an entity tag that is not quoted", ErrBadRequest)
	}
	c.bump() // the opening quote

	start := c.i
	for !c.eof() && c.peek() != '"' {
		if c.i-start >= limits.DavIfTokenLength {
			return "", false, limits.Exceed("dav If token",
				limits.DavIfTokenLength, int64(c.i-start)+1)
		}
		c.bump()
	}
	if c.eof() {
		return "", false, fmt.Errorf("%w: an unterminated entity tag", ErrBadRequest)
	}
	tag = string(c.b[start:c.i])
	c.bump() // the closing quote

	c.skipSpace()
	if c.peek() != ']' {
		return "", false, fmt.Errorf("%w: an entity tag with no closing bracket", ErrBadRequest)
	}
	c.bump()
	return tag, weak, nil
}
