//go:build linux && compat_nc

// The reference protocol's response envelope.
//
// The JSON and the XML are not two encodings of one Go value. Map order is
// significant, a boolean is a boolean in JSON and "1" or empty in XML, and an
// empty value self-closes. So the tree is ordered and each format has its own
// writer rather than struct tags on a shared type.
package compat

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Val is one node of a response tree.
//
// A tagged union rather than any: the writers have to know a boolean from a
// number, and reflection over any would guess.
type Val struct {
	kind  valKind
	text  string
	num   int64
	frac  float64
	flag  bool
	list  []Val
	pairs []Pair
}

// Pair is one entry of an ordered map.
type Pair struct {
	Key   string
	Value Val
}

type valKind uint8

const (
	kindEmpty valKind = iota
	kindString
	kindInt
	kindFloat
	kindBool
	kindList
	kindMap
)

// Empty is a value with no content. It self-closes in XML and is null in JSON.
func Empty() Val { return Val{kind: kindEmpty} }

// Str is a text value.
func Str(s string) Val { return Val{kind: kindString, text: s} }

// Int is a numeric value.
func Int(n int64) Val { return Val{kind: kindInt, num: n} }

// Float is a numeric value with a fraction, which the JSON form carries and
// the XML form renders as its shortest representation.
func Float(f float64) Val { return Val{kind: kindFloat, frac: f} }

// Object is an empty ordered mapping. A client reading a record here and a
// list elsewhere treats the two as different shapes, and they are.
func Object(pairs ...Pair) Val { return Map(pairs...) }

// ListOf is a list from values already built.
func ListOf(items []Val) Val { return List(items...) }

// Bool is a boolean, which the two formats render differently.
func Bool(b bool) Val { return Val{kind: kindBool, flag: b} }

// List is a sequence. Its XML items are always named element.
func List(items ...Val) Val { return Val{kind: kindList, list: items} }

// Map is an ordered mapping. Insertion order is preserved because a client
// reading the XML reads a document, and a document's order is part of it.
func Map(pairs ...Pair) Val { return Val{kind: kindMap, pairs: pairs} }

// P builds one map entry.
func P(key string, v Val) Pair { return Pair{Key: key, Value: v} }

// OCS status codes.
const (
	// StatusOKv1 is the success code a v1 envelope carries.
	StatusOKv1 = 100
	// StatusOKv2 is the success code a v2 envelope carries.
	StatusOKv2 = 200
	// StatusUnauthorized asks the client to authenticate.
	StatusUnauthorized = 997
	// StatusForbidden reports a request the caller may not make.
	StatusForbidden = 403
	// StatusNotFound reports a missing resource.
	StatusNotFound = 998
	// StatusInvalid reports an unusable request.
	StatusInvalid = 996
	// StatusFailure is the catch-all failure.
	StatusFailure = 999
)

// Version selects the envelope's status conventions.
type Version uint8

const (
	// V1 always answers HTTP 200 except for an authentication failure.
	V1 Version = 1
	// V2 mirrors the OCS code onto the HTTP status.
	V2 Version = 2
)

// SuccessCode is the code this version uses for success.
func (v Version) SuccessCode() int {
	if v == V2 {
		return StatusOKv2
	}
	return StatusOKv1
}

// HTTPStatus maps an OCS code to the HTTP status this version sends.
//
// v1 answers 200 for everything except an authentication failure, because a
// v1 client reads the envelope and not the status line. v2 mirrors the code,
// with the four non-HTTP codes translated.
func (v Version) HTTPStatus(ocs int) int {
	if v == V1 {
		if ocs == StatusUnauthorized {
			return 401
		}
		return 200
	}

	switch ocs {
	case StatusUnauthorized:
		return 401
	case StatusNotFound:
		return 404
	case StatusInvalid, StatusFailure:
		return 500
	}
	if ocs < 100 || ocs > 599 {
		return 400
	}
	return ocs
}

// Format is the response encoding.
type Format uint8

const (
	// FormatXML is the default.
	FormatXML Format = iota
	// FormatJSON is chosen by the query or by Accept.
	FormatJSON
)

// ContentType is what the response declares.
func (f Format) ContentType() string {
	if f == FormatJSON {
		return "application/json; charset=utf-8"
	}
	return "text/xml; charset=utf-8"
}

// NegotiateFormat picks the encoding.
//
// The query wins over Accept, because a client that spelled out what it wants
// in the URL said so more deliberately than one sending a header its library
// filled in. An unrecognised format query means XML rather than an error: the
// reference answers, and a client asking for something unknown still gets a
// response it can parse.
func NegotiateFormat(query, accept string) Format {
	switch strings.TrimSpace(query) {
	case "json":
		return FormatJSON
	case "xml":
		return FormatXML
	case "":
		if strings.Contains(accept, "application/json") {
			return FormatJSON
		}
		return FormatXML
	default:
		return FormatXML
	}
}

// Envelope is one complete response.
type Envelope struct {
	// Version selects the status conventions.
	Version Version
	// Status is the OCS code.
	Status int
	// Message is the human-readable half of the meta block.
	Message string
	// Data is the payload.
	Data Val
}

// Write encodes the envelope.
func Write(w io.Writer, e Envelope, f Format) error {
	meta := Map(
		P("status", Str(statusWord(e.Status, e.Version))),
		P("statuscode", Int(int64(e.Status))),
		P("message", Str(e.Message)),
	)
	root := Map(P("meta", meta), P("data", e.Data))

	if f == FormatJSON {
		return writeJSON(w, Map(P("ocs", root)))
	}
	return writeXML(w, root)
}

// statusWord is the meta block's textual status.
func statusWord(code int, v Version) string {
	if code == v.SuccessCode() {
		return "ok"
	}
	return "failure"
}

// writeXML renders the tree as the reference's XML.
func writeXML(w io.Writer, root Val) error {
	out := []byte(`<?xml version="1.0"?><ocs>`)
	out, err := xmlValue(out, root)
	if err != nil {
		return err
	}
	out = append(out, "</ocs>"...)

	_, err = w.Write(out)
	return err
}

// xmlValue appends a value's content, without a surrounding tag.
func xmlValue(out []byte, v Val) ([]byte, error) {
	switch v.kind {
	case kindEmpty:
		return out, nil

	case kindString:
		return xmlEscape(out, v.text)

	case kindInt:
		return strconv.AppendInt(out, v.num, 10), nil

	case kindFloat:
		return appendFloat(out, v.frac), nil

	case kindBool:
		// A true is "1" and a false is nothing at all. A literal "false"
		// reads as a non-empty string to a client checking presence.
		if v.flag {
			out = append(out, '1')
		}
		return out, nil

	case kindList:
		for _, item := range v.list {
			var err error
			if out, err = xmlElement(out, "element", item); err != nil {
				return nil, err
			}
		}
		return out, nil

	case kindMap:
		for _, pair := range v.pairs {
			// A numeric key would produce a tag name no XML parser accepts,
			// so such an entry becomes an element carrying the key.
			name := pair.Key
			if !xmlTagName(name) {
				name = "element"
			}
			var err error
			if out, err = xmlElement(out, name, pair.Value); err != nil {
				return nil, err
			}
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unknown value kind %d", v.kind)
	}
}

// xmlElement appends one tag, self-closing when its content is empty.
func xmlElement(out []byte, name string, v Val) ([]byte, error) {
	if isEmptyContent(v) {
		return append(out, "<"+name+"/>"...), nil
	}
	out = append(out, "<"+name+">"...)

	out, err := xmlValue(out, v)
	if err != nil {
		return nil, err
	}
	return append(out, "</"+name+">"...), nil
}

// isEmptyContent reports whether a value renders to nothing.
func isEmptyContent(v Val) bool {
	switch v.kind {
	case kindEmpty:
		return true
	case kindString:
		return v.text == ""
	case kindBool:
		return !v.flag
	case kindList:
		return len(v.list) == 0
	case kindMap:
		return len(v.pairs) == 0
	default:
		return false
	}
}

// xmlTagName reports whether a key can be written as a tag.
//
// Asked of the encoder rather than answered from a table here. The XML name
// grammar is large, the standard decoder enforces its own version of it, and a
// second table that disagrees produces a document this package emits and no
// parser accepts. Fuzzing found exactly that twice: an invalid UTF-8 byte and
// U+3FFFF, both of which a hand-written range check let through.
func xmlTagName(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}

	// A name is usable when the encoder round trips it. Marshalling a start
	// element with this name fails on anything the decoder would then refuse.
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	start := xml.StartElement{Name: xml.Name{Local: s}}
	if err := enc.EncodeToken(start); err != nil {
		return false
	}
	if err := enc.EncodeToken(start.End()); err != nil {
		return false
	}
	if err := enc.Flush(); err != nil {
		return false
	}

	// And the result parses. The encoder is lenient about a few names the
	// decoder rejects, so both halves have to agree.
	dec := xml.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// xmlEscape appends text with XML escaping, through the same library call the
// DAV writer uses so the two cannot disagree about what an ampersand is.
func xmlEscape(out []byte, s string) ([]byte, error) {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return nil, fmt.Errorf("escaping text: %w", err)
	}
	return append(out, buf.Bytes()...), nil
}

// writeJSON renders the tree as JSON, preserving map order.
func writeJSON(w io.Writer, v Val) error {
	out, err := jsonValue(nil, v)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// appendFloat renders a fraction the way the reference does: shortest form
// that round-trips, with a plain integer when the fraction is whole.
func appendFloat(out []byte, f float64) []byte {
	return strconv.AppendFloat(out, f, 'f', -1, 64)
}

// jsonValue appends one value.
func jsonValue(out []byte, v Val) ([]byte, error) {
	switch v.kind {
	case kindEmpty:
		return append(out, "null"...), nil

	case kindString:
		return jsonString(out, v.text)

	case kindInt:
		return strconv.AppendInt(out, v.num, 10), nil

	case kindFloat:
		return appendFloat(out, v.frac), nil

	case kindBool:
		// A boolean stays a boolean here. The XML quirk is XML's.
		return strconv.AppendBool(out, v.flag), nil

	case kindList:
		out = append(out, '[')
		for i, item := range v.list {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			if out, err = jsonValue(out, item); err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil

	case kindMap:
		out = append(out, '{')
		for i, pair := range v.pairs {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			if out, err = jsonString(out, pair.Key); err != nil {
				return nil, err
			}
			out = append(out, ':')
			if out, err = jsonValue(out, pair.Value); err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil

	default:
		return nil, fmt.Errorf("unknown value kind %d", v.kind)
	}
}

// jsonString appends a quoted string through the standard encoder, so escaping
// is the library's and not a second implementation of it.
func jsonString(out []byte, s string) ([]byte, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encoding a JSON string: %w", err)
	}
	return append(out, encoded...), nil
}
