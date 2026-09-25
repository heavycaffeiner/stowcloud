//go:build linux

// The XML reader every request body goes through.
//
// encoding/xml will happily accept a DOCTYPE, expand what a custom entity map
// defines, and read an element whose namespace prefix was never declared. None
// of that is wanted from a client, so this wraps the decoder and refuses each
// one before a handler sees a token.
package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrBodyTooLarge reports a body past the raw byte limit.
	ErrBodyTooLarge = errors.New("the XML body is too large")
	// ErrDirective reports a DOCTYPE or any other directive.
	ErrDirective = errors.New("a directive in an XML body")
	// ErrProcInst reports a processing instruction other than the leading
	// XML declaration.
	ErrProcInst = errors.New("a processing instruction in an XML body")
	// ErrUndeclaredPrefix reports an element or attribute whose namespace
	// prefix was never bound.
	ErrUndeclaredPrefix = errors.New("an undeclared namespace prefix")
	// ErrTooDeep reports nesting past the depth limit.
	ErrTooDeep = errors.New("the XML body nests too deeply")
	// ErrTooManyElements reports an element count past the limit.
	ErrTooManyElements = errors.New("too many XML elements")
	// ErrNameTooLong reports an element or attribute name past the limit.
	ErrNameTooLong = errors.New("an XML name is too long")
	// ErrTooMuchText reports accumulated character data past the limit.
	ErrTooMuchText = errors.New("too much XML character data")
	// ErrNoElements reports a body carrying no element at all.
	ErrNoElements = errors.New("the XML body has no elements")
)

// Limits bound what one body may contain.
//
// Every field is a hard refusal rather than a truncation. Truncating markup
// produces a document that parses into something the client did not send.
type Limits struct {
	// Bytes is the raw body limit.
	Bytes int64
	// Elements is the total element count.
	Elements int
	// Depth is the maximum nesting.
	Depth int
	// NameBytes is the longest element or attribute local name.
	NameBytes int
	// TextBytes is the total character data across the document.
	TextBytes int
	// Properties is the most property names one request may list.
	Properties int
	// Conditions is the most conditions or list members one header or body
	// may carry.
	Conditions int
	// ReportLeaves is the most leaf results one report may name.
	ReportLeaves int
}

// DefaultLimits are what a mount uses unless configured otherwise.
func DefaultLimits() Limits {
	return Limits{
		Bytes:        256 << 10,
		Elements:     10000,
		Depth:        64,
		NameBytes:    256,
		TextBytes:    64 << 10,
		Properties:   1024,
		Conditions:   256,
		ReportLeaves: 10000,
	}
}

// Scanner reads a bounded, defended token stream.
type Scanner struct {
	dec    *xml.Decoder
	lim    Limits
	depth  int
	count  int
	text   int
	seenPI bool
	// scopes is the namespace bindings in effect, innermost last. Tracked
	// here because encoding/xml resolves what it can and silently passes
	// through what it cannot.
	scopes []map[string]string
	// pending holds a token read ahead by a caller that had to look before
	// deciding. The next Token returns it before reading again.
	pending    xml.Token
	hasPending bool
}

// NewScanner wraps a body reader.
//
// The reader is bounded by one byte past the limit, so a body exactly at the
// limit is accepted and the first byte beyond it is what reports the overflow.
func NewScanner(body io.Reader, lim Limits) *Scanner {
	dec := xml.NewDecoder(io.LimitReader(body, lim.Bytes+1))
	// Defensive, not load-bearing: measured, a nil Entity map behaves the same
	// way, because the decoder refuses an undefined entity either way and the
	// declaration that would define one is a directive this scanner rejects.
	// Set anyway so the behaviour does not depend on that library default.
	dec.Entity = map[string]string{}
	// Load-bearing: a nil charset reader makes a non-UTF-8 encoding
	// declaration a refusal rather than a conversion by a decoder this package
	// did not choose.
	dec.CharsetReader = nil
	dec.Strict = true

	return &Scanner{dec: dec, lim: lim}
}

// Token returns the next token, or an error.
//
// io.EOF ends the stream. Every other error is a refusal and the caller stops.
func (s *Scanner) Token() (xml.Token, error) {
	if s.hasPending {
		t := s.pending
		s.pending, s.hasPending = nil, false
		return t, nil
	}
	tok, err := s.dec.Token()
	if err != nil {
		// An overflowing body is cut mid-document, and the parser reports that
		// as a syntax error. The size is the real answer and the truncation is
		// its consequence, so it is checked first: a client told its request
		// is malformed will send the same oversized body again.
		if s.dec.InputOffset() > s.lim.Bytes {
			return nil, ErrBodyTooLarge
		}
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("reading XML: %w", err)
	}

	switch t := tok.(type) {
	case xml.Directive:
		// DOCTYPE lives here, and with it every external entity declaration.
		return nil, ErrDirective

	case xml.ProcInst:
		// The leading declaration is the one instruction a document may open
		// with. Anything after it, and anything else at all, is refused.
		if s.seenPI || s.count > 0 || !strings.EqualFold(t.Target, "xml") {
			return nil, ErrProcInst
		}
		s.seenPI = true

	case xml.StartElement:
		if err := s.enter(t); err != nil {
			return nil, err
		}

	case xml.EndElement:
		s.leave()

	case xml.CharData:
		s.text += len(t)
		if s.text > s.lim.TextBytes {
			return nil, ErrTooMuchText
		}
	}

	return tok, nil
}

// enter accounts for a start element and checks its bounds.
func (s *Scanner) enter(el xml.StartElement) error {
	s.count++
	if s.count > s.lim.Elements {
		return ErrTooManyElements
	}
	s.depth++
	if s.depth > s.lim.Depth {
		return ErrTooDeep
	}

	// The element's own declarations come into scope before its name is
	// resolved, since an element may declare the prefix it uses.
	scope := map[string]string{}
	for _, attr := range el.Attr {
		switch {
		case attr.Name.Space == "xmlns":
			scope[attr.Name.Local] = attr.Value
		case attr.Name.Space == "" && attr.Name.Local == "xmlns":
			scope[""] = attr.Value
		}
	}
	s.scopes = append(s.scopes, scope)

	if len(el.Name.Local) > s.lim.NameBytes {
		return ErrNameTooLong
	}
	if err := s.resolved(el.Name); err != nil {
		return err
	}
	for _, attr := range el.Attr {
		if len(attr.Name.Local) > s.lim.NameBytes {
			return ErrNameTooLong
		}
		// An unprefixed attribute is in no namespace, which is not the same
		// as being in the default one, so only a prefixed attribute is
		// checked.
		if attr.Name.Space == "" || attr.Name.Space == "xmlns" {
			continue
		}
		if err := s.resolved(attr.Name); err != nil {
			return err
		}
	}
	return nil
}

// leave pops one scope.
func (s *Scanner) leave() {
	s.depth--
	if n := len(s.scopes); n > 0 {
		s.scopes = s.scopes[:n-1]
	}
}

// resolved reports whether a name's namespace was declared.
//
// encoding/xml hands back the URI in Space when it resolved the prefix and the
// literal prefix when it could not, and the two are indistinguishable from the
// name alone. So the declared URIs are tracked here and Space has to be one of
// them.
//
// That leaves one shape this cannot separate: an undeclared prefix "D" against
// a declared URI that is the string "D". Both arrive as Space="D". A document
// declaring xmlns:junk="D" could use D: without binding it, and the request is
// byte-identical in effect to the same document using junk:, which is valid.
// So the ambiguity admits nothing that a well-formed document could not say.
func (s *Scanner) resolved(name xml.Name) error {
	if name.Space == "" {
		return nil
	}
	// The XML namespace prefix is bound by the specification and needs no
	// declaration.
	if name.Space == xmlNamespace {
		return nil
	}
	for i := len(s.scopes) - 1; i >= 0; i-- {
		for _, uri := range s.scopes[i] {
			if uri == name.Space {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s", ErrUndeclaredPrefix, name.Space)
}

// xmlNamespace is the URI the "xml" prefix always denotes.
const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

// Elements returns how many start elements have been read.
func (s *Scanner) Elements() int { return s.count }

// CheckBodySize reports whether the body read so far stayed inside the limit.
//
// The reader is bounded one byte past the limit, so a document that consumed
// that extra byte overflowed. Checked after the stream ends rather than during
// it, because a body at exactly the limit is valid and only the byte after it
// tells the two apart.
func (s *Scanner) CheckBodySize() error {
	if s.dec.InputOffset() > s.lim.Bytes {
		return ErrBodyTooLarge
	}
	return nil
}
