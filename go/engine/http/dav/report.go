//go:build linux

package dav

import (
	"bytes"
	"errors"
	"io"

	"encoding/xml"
)

// SEARCH (RFC 5323) and the filter-files REPORT (RFC 3253), reduced to one
// body shape and one dispatch.
//
// Both carry the mobile clients: the favourites view on the phone apps is a
// REPORT. A query's filter terms are collected as resolved names and text,
// bounded, and handed to whichever source claims the namespace. This package
// interprets neither the terms nor their values; it decides only that the
// request was well formed and within its bounds.

// Leaf is one filter term as the body carried it.
//
// Collected rather than interpreted: what a leaf means belongs to the source
// claiming its namespace, and this package learning it would be the package
// learning a vendor's vocabulary.
type Leaf struct {
	// Name is the element's resolved name.
	Name xml.Name
	// Value is the element's text, when it had any. An element with none is a
	// filter on presence and arrives empty.
	Value string
}

// ReportBody is what one report body said, in the two shapes a query body
// takes: the DAV:prop set naming the response properties, and the elements
// acting as filters.
type ReportBody struct {
	// Root is the document element's resolved name. The source claiming its
	// namespace runs the query.
	Root xml.Name
	// Props are the response properties asked for.
	Props []xml.Name
	// Leaves are the filter terms, in the order they appear.
	Leaves []Leaf
}

// responsePropSet reports whether a DAV:prop at this position names the
// response properties rather than the property a filter tests.
//
// Both query bodies put the response set somewhere different: the RFC 3253
// report puts DAV:prop directly under the document element, and the RFC 5323
// search puts it under DAV:select. Everywhere else, a DAV:prop names what a
// comparison is about, and its child is a filter term. Reading those as
// response properties loses the term: a favourites search and a search by
// name arrive as the same empty filter with a literal beside it, and a source
// can then only guess from the literal's text which one it was asked.
//
// stack holds the open elements, the innermost last, without the DAV:prop
// itself.
func responsePropSet(stack []xml.Name) bool {
	if len(stack) == 1 {
		return true
	}
	return len(stack) > 0 && isDavName(stack[len(stack)-1], "select")
}

// ParseReport reduces a report body to its root, its property list and its
// filter terms. The bounds are the ones every body gets, and the only element
// interpreted here is DAV:prop; everything else passes through as a name and
// its text.
func ParseReport(body io.Reader, lim Limits) (ReportBody, error) {
	s := NewScanner(body, lim)

	var (
		out     ReportBody
		stack   []xml.Name
		sawRoot bool
		inProp  bool
		// propDepth is how deep DAV:prop sits in the document, so a prop
		// nested inside a filter term is not mistaken for a response name.
		propDepth int

		capturing bool
		capName   xml.Name
		capDepth  int
		capText   []byte
	)

	for {
		tok, err := s.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ReportBody{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				sawRoot = true
				out.Root = t.Name
				stack = append(stack, t.Name)
				continue
			}
			if isDavName(t.Name, "prop") && !inProp && responsePropSet(stack) {
				inProp = true
				propDepth = len(stack)
				stack = append(stack, t.Name)
				continue
			}
			if inProp && len(stack) == propDepth+1 {
				out.Props = append(out.Props, t.Name)
				stack = append(stack, t.Name)
				continue
			}
			// Outside DAV:prop this is a filter term, whether or not it
			// carries text: an element with none is a filter on presence.
			//
			// An element holding other elements is a container, not a leaf.
			// Capturing it whole would swallow the terms inside and hand the
			// source their combined text under one name, so it is descended
			// into instead. Whether the next token opens an element is what
			// separates the two.
			if !inProp && !capturing {
				if nextIsElement(s) {
					stack = append(stack, t.Name)
					continue
				}
				if len(out.Leaves) >= lim.Elements {
					return ReportBody{}, ErrTooManyElements
				}
				capturing, capName, capDepth = true, t.Name, len(stack)
				capText = capText[:0]
				stack = append(stack, t.Name)
				continue
			}
			stack = append(stack, t.Name)

		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if inProp && len(stack) == propDepth {
				inProp = false
			}
			if capturing && len(stack) == capDepth {
				out.Leaves = append(out.Leaves, Leaf{Name: capName, Value: string(capText)})
				capturing = false
			}

		case xml.CharData:
			if capturing {
				// The scanner already bounds total text, so this append stays
				// within the same limit.
				capText = append(capText, t...)
			}
		}
	}

	if !sawRoot {
		// The document never opened. A refusal of the body itself, not of a name in it.
		return ReportBody{}, ErrNoElements
	}
	return out, nil
}

// nextIsElement reports whether the next significant token opens an element.
//
// The scanner reads ahead by one token, which is un-gettable, so the look is
// recorded and the next Token call returns it: the caller that decided on the
// token still gets to process it. Replaying a start element does not count it
// twice, because the accounting ran when it was first read.
//
// Whitespace between elements is dropped. It is a container's own text, and
// reading it as a value makes every container in an indented body look like a
// filter term whose value is its entire subtree.
func nextIsElement(s *Scanner) bool {
	for {
		tok, err := s.Token()
		if err != nil {
			return false
		}
		if chars, ok := tok.(xml.CharData); ok && len(bytes.TrimSpace(chars)) == 0 {
			continue
		}
		s.pending, s.hasPending = tok, true
		_, isStart := tok.(xml.StartElement)
		return isStart
	}
}

func isDavName(n xml.Name, local string) bool {
	return n.Space == davNS && n.Local == local
}
