//go:build linux

// PROPFIND request bodies.
package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrTooManyProperties reports a property list past the limit.
	ErrTooManyProperties = errors.New("too many properties requested")
	// ErrBadDepth reports a Depth this operation does not accept.
	ErrBadDepth = errors.New("an unusable Depth")
)

// PropMode is what a PROPFIND asks for.
type PropMode uint8

const (
	// ModeAllProp returns every live property and every dead one.
	ModeAllProp PropMode = iota
	// ModePropName returns names with no values.
	ModePropName
	// ModeNamed returns the listed properties only.
	ModeNamed
)

// String is the mode's name in a diagnostic.
func (m PropMode) String() string {
	switch m {
	case ModeAllProp:
		return "allprop"
	case ModePropName:
		return "propname"
	case ModeNamed:
		return "prop"
	default:
		return "unknown"
	}
}

// PropFind is a parsed request body.
type PropFind struct {
	// Mode is what to return.
	Mode PropMode
	// Names are the requested properties in named mode, or the include list
	// in allprop mode. Duplicates are collapsed in first-seen order.
	Names []xml.Name
}

// davNS is the namespace every WebDAV element lives in.
const davNS = "DAV:"

// nameList collects property names once each, in first-seen order.
type nameList struct {
	names []xml.Name
	seen  map[xml.Name]bool
}

// add records a name unless it is already present or the limit is reached.
func (l *nameList) add(name xml.Name, limit int) error {
	if l.seen == nil {
		l.seen = map[xml.Name]bool{}
	}
	if l.seen[name] {
		return nil
	}
	if len(l.names) >= limit {
		return ErrTooManyProperties
	}
	l.seen[name] = true
	l.names = append(l.names, name)
	return nil
}

// ParsePropFind turns a request body into the one thing it asks for.
//
// An empty or whitespace-only body means allprop, which is what a client that
// sends no body is asking for.
func ParsePropFind(body io.Reader, lim Limits) (PropFind, error) {
	s := NewScanner(body, lim)

	// The two lists stay apart because they answer different questions: one is
	// what a named request asked for, the other is what an allprop request
	// wants added. Merging them makes a body carrying both return the wrong
	// set for whichever mode wins.
	var (
		props   nameList
		include nameList
		// target is the list the current element belongs to, nil outside both.
		target *nameList

		sawName bool
		sawProp bool
		sawIncl bool
		empty   = true
	)

	for {
		tok, err := s.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PropFind{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			empty = false
			// Inside prop or include, every element is a property name, DAV
			// namespace or not. A property really named DAV:allprop would
			// otherwise switch the whole request to allprop and return every
			// value the caller can see.
			if target != nil {
				if err := target.add(t.Name, lim.Properties); err != nil {
					return PropFind{}, err
				}
				continue
			}
			if t.Name.Space != davNS {
				continue
			}
			switch t.Name.Local {
			case "propname":
				sawName = true
			case "include":
				sawIncl = true
				target = &include
			case "prop":
				sawProp = true
				target = &props
			}

		case xml.EndElement:
			if t.Name.Space == davNS && (t.Name.Local == "prop" || t.Name.Local == "include") {
				target = nil
			}

		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				empty = false
			}
		}
	}

	if err := s.CheckBodySize(); err != nil {
		return PropFind{}, err
	}

	switch {
	case empty:
		// A client that sends no body is asking for everything.
		return PropFind{Mode: ModeAllProp}, nil

	case sawName:
		// propname wins over allprop when both appear: it is the smaller
		// disclosure, and a body asking for both did not say which it meant.
		// It carries no names, since the response is the names themselves.
		return PropFind{Mode: ModePropName}, nil

	case sawProp:
		// A named set is a named set even alongside allprop: the client listed
		// what it wants and that list is the narrower answer. An include list
		// sent with it is dropped, because include only adds to allprop.
		return PropFind{Mode: ModeNamed, Names: props.names}, nil

	case sawIncl:
		// include with no allprop is still allprop, with the list added.
		return PropFind{Mode: ModeAllProp, Names: include.names}, nil

	default:
		return PropFind{Mode: ModeAllProp, Names: include.names}, nil
	}
}

// Depth is the header's parsed value.
type Depth uint8

const (
	// DepthZero addresses the target only.
	DepthZero Depth = iota
	// DepthOne addresses the target and its immediate members.
	DepthOne
	// DepthInfinity addresses the whole subtree.
	DepthInfinity
)

// String is the value as it appears on the wire.
func (d Depth) String() string {
	switch d {
	case DepthZero:
		return "0"
	case DepthOne:
		return "1"
	case DepthInfinity:
		return "infinity"
	default:
		return "unknown"
	}
}

// ParseDepth reads the header, defaulting to fallback when absent.
//
// allowed lists what this operation accepts. A value outside it is refused
// rather than clamped: a client asking for a depth-infinity DELETE and getting
// a depth-zero one has deleted something other than what it asked to delete.
func ParseDepth(value string, fallback Depth, allowed ...Depth) (Depth, error) {
	value = strings.TrimSpace(value)

	var got Depth
	switch {
	case value == "":
		got = fallback
	case value == "0":
		got = DepthZero
	case value == "1":
		got = DepthOne
	case strings.EqualFold(value, "infinity"):
		got = DepthInfinity
	default:
		return 0, ErrBadDepth
	}

	for _, ok := range allowed {
		if got == ok {
			return got, nil
		}
	}
	return 0, ErrBadDepth
}
