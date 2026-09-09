//go:build linux && compat_nc

package nc

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// Safe streaming parsing of the four request bodies this surface reads:
// PROPFIND, PROPPATCH, the favourites REPORT and SEARCH.
//
// A body here comes from whichever client is talking to the server, not from
// this deployment's operator, so it is read the way any other untrusted input
// is read: bounded in size, bounded in depth and element count, and refused
// outright the moment it carries a DOCTYPE. Go's decoder never expands an
// external entity or fetches a DTD either way, but a body naming one is
// refused before that ever becomes relevant, and a processing instruction
// past the leading declaration is refused for the same reason: neither is
// something four wire formats need to say.
//
// Every comparison below reads a namespace by its resolved URI, never by the
// prefix a document happened to declare it under. The three clients this
// package serves spell the same namespace with different prefixes on the way
// in, and a parser keyed on "d:" would silently see nothing from the ones
// that wrote "D:" or "davns:".

// PatchOp is one instruction inside a PROPPATCH body.
type PatchOp struct {
	Name PropName
	// Value is the property's text. Empty and meaningless when Remove is
	// set: a client removing oc:favorite sends no value, and unstarring by
	// removal rather than by setting zero is the only path one client uses.
	Value  string
	Remove bool
}

// FilterQuery is a parsed oc:filter-files REPORT body.
type FilterQuery struct {
	Query    PropQuery
	Favorite bool
	// SystemTag is the tag id a filter names, empty when the request filters
	// on something else.
	SystemTag string
}

// SearchTerm is one node of a parsed d:where clause.
//
// Op is always one of "and", "or", "eq", "like", "lt", "lte", "gt", "gte" or
// "is-collection", the DAV local name of the element that produced it: the
// vocabulary is small enough that the wire spelling and the tree's own
// vocabulary are the same string. and/or carry Terms; a comparison carries
// Prop and Literal; is-collection carries neither.
type SearchTerm struct {
	Op      string
	Prop    PropName
	Literal string
	Terms   []SearchTerm
}

// SearchQuery is a parsed d:searchrequest body.
type SearchQuery struct {
	// Select is the response property list from d:select/d:prop.
	Select []PropName
	// ScopeHref and ScopeDepth are d:from/d:scope's href and depth, the
	// depth carried as the client spelled it ("0", "1" or "infinity"),
	// unparsed: what it bounds is a decision for the caller, not this
	// parser.
	ScopeHref  string
	ScopeDepth string
	Where      SearchTerm
	// OrderBy and Descending are the first d:order rule's property and
	// direction. A body naming more than one is not malformed; only the
	// first decides the primary sort, which is what every client here
	// actually depends on.
	OrderBy    PropName
	Descending bool
	// Limit is d:limit/d:nresults, zero when the body named none.
	Limit int
}

// xmlScanner reads a bounded, defended token stream.
//
// A thin wrapper rather than a shared package: this surface owns its own
// vocabulary end to end, and a scanner reused across two packages is a
// dependency neither should have on the other.
type xmlScanner struct {
	dec    *xml.Decoder
	depth  int
	count  int
	seenPI bool
}

// newXMLScanner wraps a body reader.
//
// The reader is bounded one byte past the limit, so a body exactly at the
// bound is accepted and the first byte beyond it is what reports the
// overflow rather than a truncated document read as malformed.
func newXMLScanner(r io.Reader) *xmlScanner {
	return &xmlScanner{dec: xml.NewDecoder(io.LimitReader(r, limits.RequestBodyXML+1))}
}

// token returns the next token, refusing anything a request body must never
// carry.
//
// io.EOF ends the stream cleanly. Every other error is already a
// Malformed-classified refusal, so a caller returns it unchanged.
func (s *xmlScanner) token() (xml.Token, error) {
	tok, err := s.dec.Token()
	if err != nil {
		// An oversized body is cut mid-document, which the decoder reports
		// as a syntax error. The size is the real cause and is checked
		// first: a client told its body is too large sends a smaller one,
		// where a client told its body is malformed has nothing to fix.
		if s.dec.InputOffset() > limits.RequestBodyXML {
			return nil, apierr.BadRequest("nc.xml_too_large", "body")
		}
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, apierr.BadRequest("nc.xml_malformed", "body")
	}

	switch t := tok.(type) {
	case xml.Directive:
		// A DOCTYPE, and with it any entity declaration in its internal
		// subset: both live inside this one token's raw bytes. Go's decoder
		// never parses or expands either, but a body naming one is refused
		// outright rather than trusted to be harmless this time.
		return nil, apierr.BadRequest("nc.xml_directive", "body")

	case xml.ProcInst:
		// The leading XML declaration is the one instruction a document may
		// open with. Anything after it, or anything else at all, is
		// refused.
		if s.seenPI || s.count > 0 || !strings.EqualFold(t.Target, "xml") {
			return nil, apierr.BadRequest("nc.xml_procinst", "body")
		}
		s.seenPI = true

	case xml.StartElement:
		s.count++
		if s.count > limits.XMLElements {
			return nil, apierr.BadRequest("nc.xml_too_many_elements", "body")
		}
		s.depth++
		if s.depth > limits.XMLDepth {
			return nil, apierr.BadRequest("nc.xml_too_deep", "body")
		}
		if len(t.Name.Local) > limits.XMLElementName {
			return nil, apierr.BadRequest("nc.xml_name_too_long", "body")
		}

	case xml.EndElement:
		s.depth--
	}
	return tok, nil
}

// ParsePropfind reads a PROPFIND body.
//
// An empty body, a body naming d:allprop, or a body this parser does not
// recognise at all all mean the same thing: return everything. d:propname
// wins over that when both appear, since it is the smaller disclosure and a
// client asking for both did not say which it meant.
func ParsePropfind(r io.Reader) (PropQuery, error) {
	s := newXMLScanner(r)

	var (
		names   []PropName
		seen    = map[PropName]bool{}
		inProp  bool
		sawName bool
		sawProp bool
		empty   = true
	)

	for {
		tok, err := s.token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PropQuery{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			empty = false
			if inProp {
				n := PropName{NS: t.Name.Space, Local: t.Name.Local}
				if !seen[n] {
					if len(names) >= limits.XMLElements {
						return PropQuery{}, apierr.BadRequest("nc.xml_too_many_elements", "body")
					}
					seen[n] = true
					names = append(names, n)
				}
				continue
			}
			if t.Name.Space != nsDAV {
				continue
			}
			switch t.Name.Local {
			case "propname":
				sawName = true
			case "prop":
				sawProp = true
				inProp = true
			}

		case xml.EndElement:
			if inProp && t.Name.Space == nsDAV && t.Name.Local == "prop" {
				inProp = false
			}

		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				empty = false
			}
		}
	}

	switch {
	case empty:
		return PropQuery{All: true}, nil
	case sawName:
		return PropQuery{NamesOnly: true}, nil
	case sawProp:
		return PropQuery{Names: names}, nil
	default:
		return PropQuery{All: true}, nil
	}
}

// ParseProppatch reads a PROPPATCH body into ordered instructions.
//
// A set followed by a remove of the same property is not the reverse of the
// other order, so instructions are returned in document order rather than
// grouped by operation.
func ParseProppatch(r io.Reader) ([]PatchOp, error) {
	s := newXMLScanner(r)

	var (
		out []PatchOp
		// inRemove is only meaningful while haveOp is set: it names which of
		// the two wrapper elements is open.
		haveOp   bool
		inRemove bool
		inProp   bool
		current  *PatchOp
		depth    int
		text     []byte
	)

	for {
		tok, err := s.token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case current != nil:
				// Markup inside a property value. Only its text survives
				// into the stored instruction; this just keeps the nested
				// end tag from closing the property early.
				depth++

			case inProp:
				if len(out) >= limits.XMLElements {
					return nil, apierr.BadRequest("nc.xml_too_many_elements", "body")
				}
				current = &PatchOp{Name: PropName{NS: t.Name.Space, Local: t.Name.Local}, Remove: inRemove}
				text = text[:0]

			case haveOp && t.Name.Space == nsDAV && t.Name.Local == "prop":
				inProp = true

			case !haveOp && t.Name.Space == nsDAV && t.Name.Local == "set":
				haveOp, inRemove = true, false

			case !haveOp && t.Name.Space == nsDAV && t.Name.Local == "remove":
				haveOp, inRemove = true, true
			}

		case xml.EndElement:
			switch {
			case current != nil && depth > 0:
				depth--
			case current != nil:
				current.Value = string(text)
				out = append(out, *current)
				current = nil
			case inProp:
				inProp = false
			case haveOp:
				haveOp = false
			}

		case xml.CharData:
			if current != nil {
				text = append(text, t...)
			}
		}
	}
	return out, nil
}

// ParseFilterFiles reads an oc:filter-files REPORT body.
func ParseFilterFiles(r io.Reader) (FilterQuery, error) {
	s := newXMLScanner(r)

	var (
		out     FilterQuery
		sawRoot bool
		names   []PropName
		seen    = map[PropName]bool{}
	)

	for {
		tok, err := s.token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return FilterQuery{}, err
		}

		t, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !sawRoot {
			sawRoot = true
			continue
		}

		switch {
		case t.Name.Space == nsDAV && t.Name.Local == "prop":
			if perr := readPropSet(s, &names, seen); perr != nil {
				return FilterQuery{}, perr
			}

		case isVendorName(t.Name, "favorite"):
			val, verr := readText(s)
			if verr != nil {
				return FilterQuery{}, verr
			}
			// Exactly the two spellings a client sends. Anything else,
			// including the word "false", is not a request for the starred
			// set.
			switch strings.TrimSpace(strings.ToLower(val)) {
			case "1", "true":
				out.Favorite = true
			}

		case isVendorName(t.Name, "systemtag"):
			val, verr := readText(s)
			if verr != nil {
				return FilterQuery{}, verr
			}
			out.SystemTag = strings.TrimSpace(val)
		}
	}

	if !sawRoot {
		return FilterQuery{}, apierr.BadRequest("nc.xml_no_root", "body")
	}
	out.Query = PropQuery{Names: names}
	return out, nil
}

// ParseSearchRequest reads a d:searchrequest body.
func ParseSearchRequest(r io.Reader) (SearchQuery, error) {
	s := newXMLScanner(r)

	var (
		out      SearchQuery
		sawRoot  bool
		sawOrder bool
		selNames []PropName
		selSeen  = map[PropName]bool{}
	)

	for {
		tok, err := s.token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SearchQuery{}, err
		}

		t, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !sawRoot {
			sawRoot = true
			continue
		}

		switch {
		case t.Name.Space == nsDAV && t.Name.Local == "prop":
			// Reached only under d:select: any prop nested inside d:where
			// is fully consumed by parseWhereTerm below before control
			// returns here, so this case never sees one.
			if perr := readPropSet(s, &selNames, selSeen); perr != nil {
				return SearchQuery{}, perr
			}

		case t.Name.Space == nsDAV && t.Name.Local == "href":
			href, herr := readText(s)
			if herr != nil {
				return SearchQuery{}, herr
			}
			out.ScopeHref = href

		case t.Name.Space == nsDAV && t.Name.Local == "depth":
			depth, derr := readText(s)
			if derr != nil {
				return SearchQuery{}, derr
			}
			out.ScopeDepth = strings.TrimSpace(depth)

		case isSearchTermName(t.Name):
			where, werr := parseWhereTerm(s, t.Name)
			if werr != nil {
				return SearchQuery{}, werr
			}
			out.Where = where

		case t.Name.Space == nsDAV && t.Name.Local == "order":
			if oerr := readOrder(s, &out, &sawOrder); oerr != nil {
				return SearchQuery{}, oerr
			}

		case t.Name.Space == nsDAV && t.Name.Local == "nresults":
			txt, nerr := readText(s)
			if nerr != nil {
				return SearchQuery{}, nerr
			}
			if n, cerr := strconv.Atoi(strings.TrimSpace(txt)); cerr == nil && n > 0 {
				out.Limit = n
			}
		}
	}

	if !sawRoot {
		return SearchQuery{}, apierr.BadRequest("nc.xml_no_root", "body")
	}
	out.Select = selNames
	return out, nil
}

// searchTermOps names the where-clause operators this parser recognises. The
// wire's local name and the tree's Op string are the same value, so
// recognising one is producing the other.
func isSearchTermOp(local string) bool {
	switch local {
	case "and", "or", "eq", "like", "lt", "lte", "gt", "gte", "is-collection":
		return true
	default:
		return false
	}
}

func isSearchTermName(n xml.Name) bool {
	return n.Space == nsDAV && isSearchTermOp(n.Local)
}

// parseWhereTerm reads one where-clause node, called with the operator's own
// start element already consumed.
//
// Every branch fully drains what it opens, so the only end element this loop
// ever sees at its own level is the one closing root: no depth counter is
// needed to tell it apart from a child's.
func parseWhereTerm(s *xmlScanner, root xml.Name) (SearchTerm, error) {
	term := SearchTerm{Op: root.Local}

	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return SearchTerm{}, apierr.BadRequest("nc.xml_malformed", "body")
			}
			return SearchTerm{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsDAV && t.Name.Local == "prop":
				name, perr := readPropChild(s)
				if perr != nil {
					return SearchTerm{}, perr
				}
				term.Prop = name

			case t.Name.Space == nsDAV && t.Name.Local == "literal":
				lit, lerr := readText(s)
				if lerr != nil {
					return SearchTerm{}, lerr
				}
				term.Literal = lit

			case isSearchTermName(t.Name):
				if len(term.Terms) >= limits.XMLElements {
					return SearchTerm{}, apierr.BadRequest("nc.xml_too_many_elements", "body")
				}
				child, cerr := parseWhereTerm(s, t.Name)
				if cerr != nil {
					return SearchTerm{}, cerr
				}
				term.Terms = append(term.Terms, child)

			default:
				if serr := skipElement(s); serr != nil {
					return SearchTerm{}, serr
				}
			}

		case xml.EndElement:
			if t.Name == root {
				return term, nil
			}
		}
	}
}

// readOrder reads one d:order rule, applying it to out only when it is the
// first one seen: a body naming several is not malformed, but only the
// first decides the primary sort a client actually depends on.
func readOrder(s *xmlScanner, out *SearchQuery, sawOrder *bool) error {
	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return apierr.BadRequest("nc.xml_malformed", "body")
			}
			return err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsDAV && t.Name.Local == "prop":
				name, perr := readPropChild(s)
				if perr != nil {
					return perr
				}
				if !*sawOrder {
					out.OrderBy = name
				}

			case t.Name.Space == nsDAV && t.Name.Local == "descending":
				if !*sawOrder {
					out.Descending = true
				}

			case t.Name.Space == nsDAV && t.Name.Local == "ascending":
				if !*sawOrder {
					out.Descending = false
				}

			default:
				if serr := skipElement(s); serr != nil {
					return serr
				}
			}

		case xml.EndElement:
			if t.Name.Space == nsDAV && t.Name.Local == "order" {
				*sawOrder = true
				return nil
			}
		}
	}
}

// readPropSet drains a d:prop element, collecting each child's resolved name
// once, called with d:prop's own start element already consumed.
func readPropSet(s *xmlScanner, names *[]PropName, seen map[PropName]bool) error {
	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return apierr.BadRequest("nc.xml_malformed", "body")
			}
			return err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			n := PropName{NS: t.Name.Space, Local: t.Name.Local}
			if !seen[n] {
				if len(*names) >= limits.XMLElements {
					return apierr.BadRequest("nc.xml_too_many_elements", "body")
				}
				seen[n] = true
				*names = append(*names, n)
			}

		case xml.EndElement:
			if t.Name.Space == nsDAV && t.Name.Local == "prop" {
				return nil
			}
		}
	}
}

// readPropChild reads the single property name inside a d:prop wrapper used
// as a comparison operand, called with d:prop's own start element already
// consumed. Only the first child's name is kept; none of the bodies this
// parser reads ever nests a second one.
func readPropChild(s *xmlScanner) (PropName, error) {
	var (
		name  PropName
		got   bool
		depth int
	)
	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return PropName{}, apierr.BadRequest("nc.xml_malformed", "body")
			}
			return PropName{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && !got {
				name = PropName{NS: t.Name.Space, Local: t.Name.Local}
				got = true
			}
			depth++

		case xml.EndElement:
			if depth == 0 {
				return name, nil
			}
			depth--
		}
	}
}

// readText reads the character data directly inside one element, called with
// that element's own start element already consumed.
func readText(s *xmlScanner) (string, error) {
	var (
		text  []byte
		depth int
	)
	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", apierr.BadRequest("nc.xml_malformed", "body")
			}
			return "", err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return string(text), nil
			}
			depth--
		case xml.CharData:
			if depth == 0 {
				text = append(text, t...)
			}
		}
	}
}

// skipElement discards an element this parser does not interpret, called
// with that element's own start element already consumed.
func skipElement(s *xmlScanner) error {
	depth := 0
	for {
		tok, err := s.token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return apierr.BadRequest("nc.xml_malformed", "body")
			}
			return err
		}

		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return nil
			}
			depth--
		}
	}
}

// nsNextcloudAlt is the second spelling of the vendor's own namespace.
//
// Its clients send both, sometimes in the same session and sometimes bound to
// the prefix the other namespace usually carries, so a parser that keyed on
// the documented URI alone read a favourites query as an unrecognised filter
// and answered an empty listing.
const nsNextcloudAlt = "http://nextcloud.com/ns"

// isVendorName reports whether a name is the given vendor property, under any
// of the namespaces its clients bind it to.
//
// Matched by local name inside the vendor namespaces rather than by exact
// URI: which of the two a client picks for a filter property is not a fact
// about what it is asking for.
func isVendorName(n xml.Name, local string) bool {
	if n.Local != local {
		return false
	}
	switch n.Space {
	case nsOwnCloud, nsNextcloud, nsNextcloudAlt:
		return true
	default:
		return false
	}
}
