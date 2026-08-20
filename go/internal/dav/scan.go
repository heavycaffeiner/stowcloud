// Package dav implements RFC 4918 Class 2 WebDAV.
//
// Request bodies arrive from strangers, so parsing is the security-relevant
// part of this package. The rules are short enough to state completely:
//
//   - A DOCTYPE or a processing instruction is refused outright. Entities are
//     never expanded, safely or otherwise.
//   - Element count, nesting depth, name length and accumulated text are all
//     bounded inside the scan loop.
//   - Names are namespace-resolved. A prefix is never compared, so D:, d: and
//     the default namespace are one document.
package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// NSDav is the one namespace this package knows by name. Everything else is
// carried through as an opaque URI for whichever source claims it.
const NSDav = "DAV:"

// Scanner refusals. The two forbidden constructs get their own sentinels
// because the operator-facing answer differs: a DTD is a body that was
// probably hostile, a bound is a body that was merely too big.
var (
	ErrDTDForbidden    = errors.New("dav: a DOCTYPE is not accepted in a request body")
	ErrPIForbidden     = errors.New("dav: a processing instruction is not accepted in a request body")
	ErrTooManyElements = errors.New("dav: too many elements")
	ErrTooDeep         = errors.New("dav: nesting too deep")
	ErrBadXML          = errors.New("dav: malformed request body")

	// ErrBadRequest is a header or a value this package could not parse. It is
	// separate from ErrBadXML because it never came from a body.
	ErrBadRequest = errors.New("dav: malformed request")

	// ErrLocked is a resource held by a token the request did not submit.
	ErrLocked = errors.New("dav: the resource is locked")

	// ErrPreconditionFailed is an If header that parsed and did not hold.
	ErrPreconditionFailed = errors.New("dav: a precondition failed")
)

// Name is a namespace-resolved element name. Space is the URI and is never a
// prefix, which is what makes prefix comparison unavailable rather than merely
// discouraged.
type Name struct {
	Space string
	Local string
}

// DavName builds a name in the DAV: namespace.
func DavName(local string) Name { return Name{Space: NSDav, Local: local} }

// IsDav reports whether this is the named DAV: element.
func (n Name) IsDav(local string) bool { return n.Space == NSDav && n.Local == local }

func (n Name) String() string {
	if n.Space == "" {
		return n.Local
	}
	return "{" + n.Space + "}" + n.Local
}

// Limits bounds one scan. Zero means the package default, so a caller cannot
// accidentally disable a bound by leaving a field unset.
type Limits struct {
	Elements   int
	Depth      int
	NameLength int
	TextBytes  int
}

func (l Limits) withDefaults() Limits {
	if l.Elements <= 0 {
		l.Elements = limits.DavElements
	}
	if l.Depth <= 0 {
		l.Depth = limits.DavDepth
	}
	if l.NameLength <= 0 {
		l.NameLength = limits.DavNameLength
	}
	if l.TextBytes <= 0 {
		l.TextBytes = limits.DavTextBytes
	}
	return l
}

// nodeKind is what the scanner reduces the token stream to. Attributes,
// comments and the XML declaration are dropped: no WebDAV body this server
// answers carries meaning in them.
type nodeKind int

const (
	nodeStart nodeKind = iota
	nodeEnd
	nodeText
)

type node struct {
	kind  nodeKind
	name  Name
	text  string
	empty bool
}

// scanner walks the token stream, enforcing the bounds as it goes.
type scanner struct {
	dec      *xml.Decoder
	lim      Limits
	depth    int
	elements int

	// encoding/xml has no empty-element token: <a/> arrives as a start
	// immediately followed by an end. Peeking one token ahead is what lets the
	// parsers below tell a self-closing property from one with a value.
	pending *node
}

func newScanner(body []byte, lim Limits) *scanner {
	dec := xml.NewDecoder(bytes.NewReader(body))
	// Left nil deliberately: a body declaring an encoding this build cannot
	// decode is refused rather than guessed at.
	dec.CharsetReader = nil
	// Entities are never expanded. The map is empty and stays empty, so an
	// undefined entity is an error from the decoder rather than a lookup.
	dec.Entity = nil
	dec.Strict = true
	return &scanner{dec: dec, lim: lim.withDefaults()}
}

// next returns the next simplified node, or nil at end of document.
func (s *scanner) next() (*node, error) {
	if s.pending != nil {
		n := s.pending
		s.pending = nil
		return n, nil
	}
	return s.scan()
}

// peek reports the node after the current one without consuming it.
//
// Whitespace-only text is skipped, so a pretty-printed document peeks the same
// as a compact one. Text with content is kept: it is a property value, and
// dropping it would lose what the client sent.
func (s *scanner) peek() (*node, error) {
	for s.pending == nil {
		n, err := s.scan()
		if err != nil {
			return nil, err
		}
		if n != nil && n.kind == nodeText && strings.TrimSpace(n.text) == "" {
			continue
		}
		s.pending = n
		break
	}
	return s.pending, nil
}

func (s *scanner) scan() (*node, error) {
	for {
		tok, err := s.dec.Token()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrBadXML, err)
		}

		switch t := tok.(type) {
		// The two hard refusals. encoding/xml surfaces both as ordinary
		// tokens, so silence here would be acceptance.
		case xml.Directive:
			return nil, ErrDTDForbidden
		case xml.ProcInst:
			// encoding/xml reports the XML declaration as a ProcInst, but the
			// spec does not make it one and every client sends it. Refusing it
			// would refuse almost every real body.
			if t.Target == "xml" {
				continue
			}
			return nil, ErrPIForbidden

		case xml.StartElement:
			s.elements++
			if s.elements > s.lim.Elements {
				return nil, limits.Exceed("dav elements", int64(s.lim.Elements), int64(s.elements))
			}
			if s.depth+1 > s.lim.Depth {
				return nil, limits.Exceed("dav depth", int64(s.lim.Depth), int64(s.depth+1))
			}
			if len(t.Name.Local) > s.lim.NameLength {
				return nil, limits.Exceed("dav element name",
					int64(s.lim.NameLength), int64(len(t.Name.Local)))
			}
			s.depth++
			return &node{kind: nodeStart, name: Name{Space: t.Name.Space, Local: t.Name.Local}}, nil

		case xml.EndElement:
			if s.depth > 0 {
				s.depth--
			}
			return &node{kind: nodeEnd}, nil

		// Text arrives in fragments, split at every entity reference. It is
		// accumulated by the caller and trimmed once, because trimming each
		// fragment turns "Tom &amp; Jerry" into "Tom&Jerry".
		case xml.CharData:
			if len(t) == 0 {
				continue
			}
			return &node{kind: nodeText, text: string(t)}, nil

		// Comments and the XML declaration carry nothing this server reads.
		default:
			continue
		}
	}
}

// startNode returns the next node, collapsing a start immediately followed by
// its end into one empty start. Everything below reads elements through this.
func (s *scanner) startNode() (*node, error) {
	n, err := s.next()
	if err != nil || n == nil {
		return n, err
	}
	if n.kind != nodeStart {
		return n, nil
	}
	after, err := s.peek()
	if err != nil {
		return nil, err
	}
	if after != nil && after.kind == nodeEnd {
		// The end token is dropped rather than returned. Its depth was already
		// unwound when it was scanned, so nothing is adjusted here.
		s.pending = nil
		n.empty = true
	}
	return n, nil
}

// textAccumulator joins the fragments of one property value under a bound.
// The bound is on the join rather than on a fragment: a value arrives in as
// many pieces as it has entity references.
type textAccumulator struct {
	buf   []byte
	limit int
}

func (a *textAccumulator) add(s string) error {
	if len(a.buf)+len(s) > a.limit {
		return limits.Exceed("dav property text", int64(a.limit), int64(len(a.buf)+len(s)))
	}
	a.buf = append(a.buf, s...)
	return nil
}

// value is the accumulated text with surrounding whitespace removed once,
// which drops pretty-print indentation without touching an interior entity.
func (a *textAccumulator) value() string { return strings.TrimSpace(string(a.buf)) }
