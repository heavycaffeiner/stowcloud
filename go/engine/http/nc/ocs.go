//go:build linux && compat_nc

package nc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
)

// The OCS envelope, and the value tree it carries.
//
// Two encodings of one document. The clients disagree about which they want,
// and not by preference: one client's share code parses XML and never asks for
// anything else, while most of its own other calls ask for JSON, so both are
// load-bearing and neither can be dropped. A typed tree is what lets one
// handler answer both, because the two encodings disagree about more than
// syntax: a boolean is "1" in the XML form and true in the JSON one, and an
// empty list is an empty element in one and [] in the other.

// The namespace-free element names the envelope itself is built from.
const (
	elemRoot = "ocs"
	elemMeta = "meta"
	elemData = "data"
	// elemItem is the element repeated for a list member. The vocabulary has
	// one name for every list, whatever the list holds.
	elemItem = "element"
)

// OCS status codes. The two envelope versions agree on the failures and
// disagree on success, which is the whole of the difference between them.
const (
	StatusOKv1         = 100
	StatusOKv2         = 200
	StatusBadRequest   = 400
	StatusForbidden    = 403
	StatusNotFound     = 404
	StatusUnavailable  = 503
	StatusUnauthorized = 997
	StatusFailure      = 999
)

// Version is the envelope version a request arrived under.
type Version int

// The two mounted versions.
const (
	V1 Version = 1
	V2 Version = 2
)

// SuccessCode is the status a successful envelope of this version carries.
func (v Version) SuccessCode() int {
	if v == V1 {
		return StatusOKv1
	}
	return StatusOKv2
}

// HTTPStatus is the status line this version sends for an OCS code.
//
// Version 1 answers 200 for everything a client is expected to read out of the
// envelope, and 401 only for an authentication failure, because a client
// holding a stale credential has to see the refusal at the transport layer to
// start its re-login. Version 2 mirrors the code, with the three codes that
// are not HTTP statuses translated.
func (v Version) HTTPStatus(code int) int {
	if v == V1 {
		if code == StatusUnauthorized {
			return fiber.StatusUnauthorized
		}
		return fiber.StatusOK
	}
	switch code {
	case StatusUnauthorized:
		return fiber.StatusUnauthorized
	case StatusFailure:
		return fiber.StatusInternalServerError
	}
	if code < 100 || code > 599 {
		return fiber.StatusBadRequest
	}
	return code
}

// Format is the encoding a response is written in.
type Format int

// The two encodings.
const (
	FormatXML Format = iota
	FormatJSON
)

// NegotiateFormat decides the encoding for one request.
//
// The query parameter wins because a client that sends it means it: the share
// family sends none and parses XML, while the same client asks for JSON by
// parameter elsewhere. An Accept header naming JSON is honoured next, which is
// how the other client asks. Everything else is XML, which is the vocabulary's
// own default and the only answer a client that sent no preference can parse.
func NegotiateFormat(query, accept string) Format {
	switch strings.ToLower(strings.TrimSpace(query)) {
	case "json":
		return FormatJSON
	case "xml":
		return FormatXML
	}
	if strings.Contains(strings.ToLower(accept), "application/json") {
		return FormatJSON
	}
	return FormatXML
}

// ContentType is the media type this encoding is served as.
//
// A client checks it before parsing, and one of them refuses a body whose type
// does not name XML.
func (f Format) ContentType() string {
	if f == FormatJSON {
		return fiber.MIMEApplicationJSONCharsetUTF8
	}
	return "application/xml; charset=utf-8"
}

// Error is one refused request, as the envelope's meta block reports it.
//
// It carries an OCS code rather than an HTTP status, because the two versions
// answer the same refusal with different status lines and the handler that
// refused has no business knowing which version it was reached under.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("ocs %d: %s", e.Code, e.Message) }

// The refusals a handler distinguishes.
func BadRequest(message string) *Error { return &Error{Code: StatusBadRequest, Message: message} }
func Forbidden(message string) *Error  { return &Error{Code: StatusForbidden, Message: message} }
func NotFound(message string) *Error   { return &Error{Code: StatusNotFound, Message: message} }
func Failure(message string) *Error    { return &Error{Code: StatusFailure, Message: message} }

// Unavailable is a refusal worth retrying: the code passes through to the
// HTTP status, so a client reads 503 rather than a failure it should not
// repeat.
func Unavailable(message string) *Error {
	return &Error{Code: StatusUnavailable, Message: message}
}
func Unauthorized(message string) *Error {
	return &Error{Code: StatusUnauthorized, Message: message}
}

// valKind is what a value holds.
type valKind uint8

const (
	valAbsent valKind = iota
	valString
	valInt
	valFloat
	valBool
	valObject
	valList
)

// Val is one node of an OCS document.
//
// Typed rather than any: the two encodings render a boolean and a number
// differently, and a tree of any would decide that by reflection at write
// time, which is where a number silently becomes a quoted string in one
// encoding and not the other.
type Val struct {
	kind  valKind
	text  string
	num   int64
	real  float64
	flag  bool
	pairs []Pair
	items []Val
	// item is the element name a list's members take in the XML encoding. The
	// JSON encoding has no use for it.
	item string
}

// Pair is one named member of an object, in declaration order. Order is kept
// because a golden response is compared as bytes.
type Pair struct {
	Key string
	Val Val
}

// The constructors. Everything a handler builds goes through one of these.
func Str(s string) Val { return Val{kind: valString, text: s} }
func Int(i int64) Val  { return Val{kind: valInt, num: i} }

// Float is a fractional number. One field on this wire is one (a quota
// percentage) and it is read as a number, so it cannot be rendered as text.
func Float(f float64) Val    { return Val{kind: valFloat, real: f} }
func Bool(b bool) Val        { return Val{kind: valBool, flag: b} }
func Absent() Val            { return Val{kind: valAbsent} }
func P(k string, v Val) Pair { return Pair{Key: k, Val: v} }

// Obj is an object with members in the order given.
func Obj(pairs ...Pair) Val { return Val{kind: valObject, pairs: pairs} }

// List is a list whose members render as the vocabulary's own item element.
func List(items ...Val) Val { return Val{kind: valList, items: items, item: elemItem} }

// NamedList is a list whose members render under a chosen element name, for
// the one shape whose XML form names its members after what they are.
func NamedList(item string, items ...Val) Val {
	return Val{kind: valList, items: items, item: item}
}

// IsAbsent reports whether a value carries nothing. An absent member is
// skipped by both encoders rather than rendered as an empty one, because a
// client reading an empty string where it expected a number reads zero.
func (v Val) IsAbsent() bool { return v.kind == valAbsent }

// ErrElementName is returned when a document names an element that is not a
// legal XML name. It cannot fire for the literals this package carries; it
// exists so that a name that ever becomes dynamic fails loudly instead of
// writing a document no client can parse.
var ErrElementName = errors.New("nc: illegal element name")

// WriteOCS answers with a successful envelope.
func (s *Server) WriteOCS(c *fiber.Ctx, v Version, f Format, data Val) error {
	return s.writeEnvelope(c, v, f, v.SuccessCode(), "OK", data)
}

// WriteOCSError answers with a refused one.
func (s *Server) WriteOCSError(c *fiber.Ctx, v Version, f Format, e *Error) error {
	return s.writeEnvelope(c, v, f, e.Code, e.Message, Absent())
}

// writeEnvelope renders one envelope and sets the status line.
//
// A failure to render is reported as an empty 500 rather than a half-written
// document: a client that parses the prefix of a truncated body treats what it
// read as the whole answer.
func (s *Server) writeEnvelope(
	c *fiber.Ctx, v Version, f Format, code int, message string, data Val,
) error {
	root := Obj(
		P(elemMeta, Obj(
			P("status", Str(metaStatus(code))),
			P("statuscode", Int(int64(code))),
			P("message", Str(message)),
			P("totalitems", Str("")),
			P("itemsperpage", Str("")),
		)),
		P(elemData, data),
	)

	body, err := render(root, f)
	if err != nil {
		s.log.Error("an OCS response could not be rendered", "error", err)
		return c.SendStatus(fiber.StatusInternalServerError)
	}

	c.Set(fiber.HeaderContentType, f.ContentType())
	return c.Status(v.HTTPStatus(code)).Send(body)
}

// metaStatus is the word beside the code. A client branches on the code; the
// word is what it shows a person when something went wrong.
func metaStatus(code int) string {
	if code == StatusOKv1 || code == StatusOKv2 {
		return "ok"
	}
	return "failure"
}

// render encodes one document.
func render(root Val, f Format) ([]byte, error) {
	if f == FormatJSON {
		var b bytes.Buffer
		if err := writeJSONDoc(&b, root); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	var out xmlBuf
	if err := writeXMLDoc(&out, root); err != nil {
		return nil, err
	}
	return out.out, nil
}

// writeJSONDoc wraps the payload in the object key the JSON form nests under.
func writeJSONDoc(w io.Writer, root Val) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{elemRoot: jsonValue(root)})
}

// jsonValue projects the tree onto what the standard encoder understands.
//
// An absent value becomes an empty object rather than null: a client reading
// null where it expected a document treats the response as broken, while an
// empty object reads as "nothing to report", which is what it means.
func jsonValue(v Val) any {
	switch v.kind {
	case valString:
		return v.text
	case valInt:
		return v.num
	case valFloat:
		return v.real
	case valBool:
		return v.flag
	case valList:
		out := make([]any, 0, len(v.items))
		for _, item := range v.items {
			out = append(out, jsonValue(item))
		}
		return out
	case valObject:
		out := make(map[string]any, len(v.pairs))
		for _, p := range v.pairs {
			if p.Val.IsAbsent() {
				continue
			}
			out[p.Key] = jsonValue(p.Val)
		}
		return out
	default:
		return map[string]any{}
	}
}

// xmlBuf is the document under construction.
//
// An append buffer rather than a builder: appending has no failure to report,
// so no call site here carries an error path that cannot run.
type xmlBuf struct{ out []byte }

func (x *xmlBuf) s(v string) { x.out = append(x.out, v...) }

// writeXMLDoc writes the declaration and the root element.
func writeXMLDoc(w *xmlBuf, root Val) error {
	w.s(`<?xml version="1.0"?>`)
	return writeXMLElement(w, elemRoot, root)
}

// writeXMLElement writes one element and whatever nests inside it.
func writeXMLElement(w *xmlBuf, name string, v Val) error {
	if !validName(name) {
		return fmt.Errorf("%w: %q", ErrElementName, name)
	}
	if v.kind == valAbsent {
		w.s("<" + name + "/>")
		return nil
	}

	w.s("<" + name + ">")
	switch v.kind {
	case valString:
		escapeXML(w, v.text)
	case valInt:
		w.s(strconv.FormatInt(v.num, 10))
	case valFloat:
		w.s(strconv.FormatFloat(v.real, 'f', 2, 64))
	case valBool:
		// The wire spelling of a boolean in this encoding. A client reads the
		// text as a number and treats a non-zero digit as true, so the two
		// digits are the only spelling every client agrees on.
		if v.flag {
			w.s("1")
		} else {
			w.s("0")
		}
	case valList:
		for _, item := range v.items {
			if err := writeXMLElement(w, v.item, item); err != nil {
				return err
			}
		}
	case valObject:
		for _, p := range v.pairs {
			if p.Val.IsAbsent() {
				continue
			}
			if err := writeXMLElement(w, p.Key, p.Val); err != nil {
				return err
			}
		}
	}
	w.s("</" + name + ">")
	return nil
}

// validName reports whether a string is a legal XML element name, restricted
// to what this vocabulary actually uses: an ASCII letter or underscore, then
// letters, digits, hyphens, underscores and dots.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_':
		case i > 0 && (ch >= '0' && ch <= '9' || ch == '-' || ch == '.'):
		default:
			return false
		}
	}
	return true
}

// escapeXML writes text with the five predefined entities applied, and with
// anything that cannot appear in a well-formed document replaced.
//
// Everything a client stored comes back through here, and a name on a POSIX
// filesystem is a byte string: it may hold a control character or a byte
// sequence that is not UTF-8 at all. Either one makes the whole document
// unparseable, so one badly named file would empty the listing of every
// folder it sits in. A control character is dropped, since no interface can
// draw it, and an invalid sequence becomes the replacement character, which
// leaves the name readable. Neither loses the file: a client addresses it by
// the href, which is percent-encoded byte for byte.
func escapeXML(w *xmlBuf, s string) {
	for i, r := range s {
		switch r {
		case '&':
			w.s("&amp;")
		case '<':
			w.s("&lt;")
		case '>':
			w.s("&gt;")
		case '"':
			w.s("&quot;")
		case '\'':
			w.s("&apos;")
		case utf8.RuneError:
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				w.s(string(utf8.RuneError))
				continue
			}
			w.s(string(r))
		default:
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			w.s(string(r))
		}
	}
}
