package dav

import (
	"fmt"
)

// PropFindMode is which of the three PROPFIND shapes a body asked for.
type PropFindMode int

const (
	// PropFindAllProp is the default, and what an empty body means.
	PropFindAllProp PropFindMode = iota
	PropFindPropName
	PropFindNamed
)

// PropFind is a parsed PROPFIND body.
type PropFind struct {
	Mode  PropFindMode
	Props []Name
}

// ParsePropFind reads a PROPFIND body.
//
// An empty or whitespace-only body means allprop. RFC 4918 says so and several
// clients depend on it, so a missing body is not an error.
func ParsePropFind(body []byte, lim Limits) (PropFind, error) {
	if isAllSpace(body) {
		return PropFind{Mode: PropFindAllProp}, nil
	}

	sc := newScanner(body, lim)
	var (
		stack   []Name
		props   []Name
		mode    = PropFindAllProp
		sawMode bool
		sawRoot bool
	)

	for {
		n, err := sc.startNode()
		if err != nil {
			return PropFind{}, err
		}
		if n == nil {
			break
		}
		switch n.kind {
		case nodeStart:
			if !sawRoot {
				if !n.name.IsDav("propfind") {
					return PropFind{}, fmt.Errorf("%w: the root element is %s, want DAV:propfind",
						ErrBadXML, n.name)
				}
				sawRoot = true
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			switch len(stack) {
			case 1:
				// propname wins over allprop: it asks for a strictly smaller
				// answer, so honouring the larger one would over-disclose.
				if n.name.IsDav("propname") {
					mode, sawMode = PropFindPropName, true
				} else if n.name.IsDav("allprop") && !sawMode {
					mode, sawMode = PropFindAllProp, true
				}
			case 2:
				// Children of prop and include are the named set. include is
				// an allprop extension, and its names are already covered by
				// the live set or by a source, so collecting them changes
				// nothing but costs nothing either.
				parent := stack[1]
				if parent.IsDav("prop") || parent.IsDav("include") {
					if !containsName(props, n.name) {
						props = append(props, n.name)
					}
				}
			}
			if !n.empty {
				stack = append(stack, n.name)
			}
		case nodeEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case nodeText:
			// A PROPFIND carries no values, only names.
		}
	}

	if !sawRoot {
		return PropFind{}, fmt.Errorf("%w: the document has no elements", ErrBadXML)
	}
	if mode == PropFindPropName {
		return PropFind{Mode: PropFindPropName}, nil
	}
	if len(props) == 0 {
		return PropFind{Mode: PropFindAllProp}, nil
	}
	if sawMode && mode == PropFindAllProp {
		// allprop with an include list is still allprop.
		return PropFind{Mode: PropFindAllProp}, nil
	}
	return PropFind{Mode: PropFindNamed, Props: props}, nil
}

// PatchOp is one PROPPATCH instruction.
//
// A set carries text only. The client's markup is deliberately not retained:
// the value is re-serialised from this text on the way out, which is what
// makes echoing a dead property back injection-free.
type PatchOp struct {
	Name   Name
	Value  string
	Remove bool
}

// ParsePropPatch reads a PROPPATCH body into the operations it asks for, in
// document order. RFC 4918 requires them applied in that order, and a set
// followed by a remove of the same property is not the same as the reverse.
func ParsePropPatch(body []byte, lim Limits) ([]PatchOp, error) {
	sc := newScanner(body, lim)
	lim = lim.withDefaults()

	var (
		stack   []Name
		ops     []PatchOp
		sawRoot bool

		// The property being captured, and the stack depth it started at.
		capturing bool
		capName   Name
		capRemove bool
		capDepth  int
		capText   textAccumulator
	)

	for {
		n, err := sc.startNode()
		if err != nil {
			return nil, err
		}
		if n == nil {
			break
		}
		switch n.kind {
		case nodeStart:
			if !sawRoot {
				if !n.name.IsDav("propertyupdate") {
					return nil, fmt.Errorf("%w: the root element is %s, want DAV:propertyupdate",
						ErrBadXML, n.name)
				}
				sawRoot = true
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			// propertyupdate / (set|remove) / prop / the property itself.
			if !capturing && len(stack) == 3 {
				action := stack[1]
				isSet, isRemove := action.IsDav("set"), action.IsDav("remove")
				if stack[2].IsDav("prop") && (isSet || isRemove) {
					if n.empty {
						ops = append(ops, PatchOp{Name: n.name, Remove: isRemove})
						continue
					}
					capturing, capName, capRemove, capDepth = true, n.name, isRemove, len(stack)
					capText = textAccumulator{limit: lim.TextBytes}
					stack = append(stack, n.name)
					continue
				}
			}
			if !n.empty {
				stack = append(stack, n.name)
			}
		case nodeEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if capturing && len(stack) == capDepth {
				ops = append(ops, PatchOp{Name: capName, Value: capText.value(), Remove: capRemove})
				capturing = false
			}
		case nodeText:
			if capturing {
				if err := capText.add(n.text); err != nil {
					return nil, err
				}
			}
		}
	}

	if !sawRoot {
		return nil, fmt.Errorf("%w: the document has no elements", ErrBadXML)
	}
	return ops, nil
}

// ReportBody is a report reduced to the two shapes every RFC 3253 report has:
// the DAV:prop set saying what the response should carry, and the leaf
// elements outside it saying what is being filtered on.
//
// What a leaf means is the claiming source's business. This package reads the
// names and the text, bounded, and interprets neither.
type ReportBody struct {
	Root   Name
	Props  []Name
	Leaves []Leaf
}

// Leaf is one filter element, collected verbatim.
type Leaf struct {
	Name  Name
	Value string
}

// ParseReport reads a report body through the same bounds every other body
// gets, without interpreting anything but DAV:prop.
func ParseReport(body []byte, lim Limits) (ReportBody, error) {
	sc := newScanner(body, lim)
	lim = lim.withDefaults()

	var (
		out     ReportBody
		stack   []Name
		sawRoot bool
		inProp  bool
		// propDepth is the stack depth DAV:prop sits at, so a nested prop
		// inside a filter does not start collecting response names.
		propDepth int

		capturing bool
		capName   Name
		capDepth  int
		capText   textAccumulator
	)

	for {
		n, err := sc.startNode()
		if err != nil {
			return ReportBody{}, err
		}
		if n == nil {
			break
		}
		switch n.kind {
		case nodeStart:
			if !sawRoot {
				sawRoot = true
				out.Root = n.name
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			if n.name.IsDav("prop") && !inProp {
				inProp, propDepth = true, len(stack)
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			if inProp && len(stack) == propDepth+1 {
				out.Props = append(out.Props, n.name)
				if !n.empty {
					stack = append(stack, n.name)
				}
				continue
			}
			// Outside DAV:prop, a leaf is a filter term. An empty one still
			// counts: <v:starred/> is a filter on presence.
			//
			// Only an element with no element children is a leaf. A container
			// such as <v:filter-rules> is descended into, because capturing it
			// would swallow the filters inside it and report the concatenation
			// of their text under the container's own name.
			if !inProp && !capturing {
				if n.empty {
					if err := checkLeafCount(out.Leaves, lim); err != nil {
						return ReportBody{}, err
					}
					out.Leaves = append(out.Leaves, Leaf{Name: n.name})
					continue
				}
				ahead, perr := sc.peek()
				if perr != nil {
					return ReportBody{}, perr
				}
				if ahead != nil && ahead.kind == nodeStart {
					stack = append(stack, n.name)
					continue
				}
				if err := checkLeafCount(out.Leaves, lim); err != nil {
					return ReportBody{}, err
				}
				capturing, capName, capDepth = true, n.name, len(stack)
				capText = textAccumulator{limit: lim.TextBytes}
				stack = append(stack, n.name)
				continue
			}
			if !n.empty {
				stack = append(stack, n.name)
			}
		case nodeEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if inProp && len(stack) == propDepth {
				inProp = false
			}
			if capturing && len(stack) == capDepth {
				out.Leaves = append(out.Leaves, Leaf{Name: capName, Value: capText.value()})
				capturing = false
			}
		case nodeText:
			if capturing {
				if err := capText.add(n.text); err != nil {
					return ReportBody{}, err
				}
			}
		}
	}

	if !sawRoot {
		return ReportBody{}, fmt.Errorf("%w: the document has no elements", ErrBadXML)
	}
	return out, nil
}

func checkLeafCount(leaves []Leaf, lim Limits) error {
	if len(leaves) >= lim.Elements {
		return ErrTooManyElements
	}
	return nil
}

func containsName(set []Name, n Name) bool {
	for _, have := range set {
		if have == n {
			return true
		}
	}
	return false
}

func isAllSpace(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}
