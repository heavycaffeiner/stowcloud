//go:build linux

// PROPPATCH request bodies.
//
// The transaction rule is the whole point: a request that touches one live
// property commits nothing. Half-applying it would leave the resource in a
// state the client never asked for and cannot predict from the response.
package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrBadPropertyName reports a name that cannot be written back as a tag.
	ErrBadPropertyName = errors.New("a property name is not a valid XML name")
)

// PropOp is what one instruction does.
type PropOp uint8

const (
	// OpSet writes a value.
	OpSet PropOp = iota
	// OpRemove deletes a property.
	OpRemove
)

// String is the operation's name in a diagnostic.
func (o PropOp) String() string {
	if o == OpRemove {
		return "remove"
	}
	return "set"
}

// Instruction is one set or remove.
type Instruction struct {
	// Op is what to do.
	Op PropOp
	// Name is the property.
	Name xml.Name
	// Value is the property's text content, collected rather than retained as
	// markup. A parser never keeps client markup, so a value is serialized
	// afresh on the way out and cannot smuggle elements back into a response.
	Value string
}

// PropPatch is a parsed request body.
type PropPatch struct {
	// Instructions are in document order. A set followed by a remove of the
	// same property is not the reverse of the other order, so the order is
	// preserved rather than grouped by operation.
	Instructions []Instruction
}

// ParsePropPatch reads a body into ordered instructions.
func ParsePropPatch(body io.Reader, lim Limits) (PropPatch, error) {
	s := NewScanner(body, lim)

	var (
		out PropPatch
		// op is the enclosing set or remove, nil outside both.
		op *PropOp
		// inProp is whether the DAV:prop wrapper is open. Tracked separately
		// because the wrapper sits between the operation and the properties,
		// and reading it as a property makes every real one its content.
		inProp bool
		// current is the property being read, nil outside one.
		current *Instruction
		// depth counts elements inside the current property, so nested markup
		// does not each start a new property.
		depth int
		text  []byte
	)

	for {
		tok, err := s.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PropPatch{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case current != nil:
				// Markup inside a property value. Counted so its end tag does
				// not close the property, and otherwise ignored: only text
				// survives into the stored value.
				depth++

			case inProp:
				// Directly inside prop: this is the property itself.
				if len(out.Instructions) >= lim.Properties {
					return PropPatch{}, ErrTooManyProperties
				}
				current = &Instruction{Op: *op, Name: t.Name}
				text = text[:0]

			case op != nil && t.Name.Space == davNS && t.Name.Local == "prop":
				inProp = true

			case op == nil && t.Name.Space == davNS && t.Name.Local == "set":
				o := OpSet
				op = &o

			case op == nil && t.Name.Space == davNS && t.Name.Local == "remove":
				o := OpRemove
				op = &o
			}

		case xml.EndElement:
			switch {
			case current != nil && depth > 0:
				depth--

			case current != nil:
				current.Value = string(text)
				out.Instructions = append(out.Instructions, *current)
				current = nil

			case inProp:
				inProp = false

			case op != nil:
				op = nil
			}

		case xml.CharData:
			if current != nil {
				text = append(text, t...)
			}
		}
	}

	if err := s.CheckBodySize(); err != nil {
		return PropPatch{}, err
	}
	return out, nil
}

// The status codes a PROPPATCH response carries per property.
const (
	// StatusOK is a property the transaction would have applied.
	StatusOK = 200
	// StatusForbidden is the property that refused the transaction.
	StatusForbidden = 403
	// StatusFailedDependency is a property that was fine on its own and was
	// dropped because another one in the same request refused.
	StatusFailedDependency = 424
)

// Outcome is what one instruction gets in the response.
type Outcome struct {
	// Name is the property.
	Name xml.Name
	// Status is its code.
	Status int
}

// Plan is the decision for a whole request.
type Plan struct {
	// Outcomes are in the request's own order.
	Outcomes []Outcome
	// Commit is whether any change is written at all.
	Commit bool
}

// IsLive reports whether a property is server-maintained.
type IsLive func(xml.Name) bool

// PlanPropPatch decides a request without touching storage.
//
// One live property refuses the whole request: that property reports 403 and
// every other one reports 424, with nothing written. A partial commit would
// leave a resource in a state the client did not ask for and cannot work out
// from the response, since the response says what each property got but not
// what the resource now holds.
func PlanPropPatch(p PropPatch, live IsLive) Plan {
	plan := Plan{Outcomes: make([]Outcome, 0, len(p.Instructions))}

	refused := false
	for _, in := range p.Instructions {
		if live(in.Name) {
			refused = true
			break
		}
	}

	for _, in := range p.Instructions {
		switch {
		case !refused:
			plan.Outcomes = append(plan.Outcomes, Outcome{Name: in.Name, Status: StatusOK})
		case live(in.Name):
			plan.Outcomes = append(plan.Outcomes, Outcome{Name: in.Name, Status: StatusForbidden})
		default:
			plan.Outcomes = append(plan.Outcomes, Outcome{Name: in.Name, Status: StatusFailedDependency})
		}
	}

	plan.Commit = !refused && len(p.Instructions) > 0
	return plan
}

// ValidPropertyName reports whether a name can be written back as a tag.
//
// Checked before storing rather than before serializing: a name that cannot be
// rendered would be stored and then break every later PROPFIND on the
// resource, which is a failure the client that caused it never sees.
func ValidPropertyName(name xml.Name) bool {
	if name.Local == "" {
		return false
	}
	if !xmlNameStart(rune(name.Local[0])) && name.Local[0] < 0x80 {
		return false
	}
	for _, r := range name.Local {
		if !xmlNameChar(r) {
			return false
		}
	}
	// Defensive rather than load-bearing on this parser's output: the XML
	// parser splits a qualified name at the colon, so a local name it produces
	// never holds one. This function is exported and also guards a name coming
	// from storage or from a caller that did not go through the parser, where
	// a colon would create a prefix the writer never declared.
	return !strings.Contains(name.Local, ":")
}

// xmlNameStart reports whether a rune may open an XML name.
func xmlNameStart(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r == '_':
		return true
	case r >= 0x80:
		return true
	default:
		return false
	}
}

// xmlNameChar reports whether a rune may appear in an XML name.
func xmlNameChar(r rune) bool {
	switch {
	case xmlNameStart(r):
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '.':
		return true
	default:
		return false
	}
}
