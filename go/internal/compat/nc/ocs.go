//go:build compat_nc

package nc

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The OCS envelope.
//
// Reproduced exactly, including the parts that are wrong, because a client
// parses them. Getting a status code wrong here produces no error a user can
// see: a client treats an unexpected statuscode as "the call failed" and gives
// up silently, which is why the version divergence below is spelled out rather
// than folded together.
//
// The XML is written by hand. The reference renames every numerically-keyed
// entry to <element>, writes booleans as 1 and the empty string, and collapses
// an empty element to a self-closing tag. None of that is a standard mapping,
// and a close-enough one fails in ways that are invisible until a specific
// client chokes.

// OCSVersion is which entry point a request arrived through.
type OCSVersion int

const (
	// OCSv1 answers success as 100 and pins HTTP 200 for everything except
	// one status.
	OCSv1 OCSVersion = 1
	// OCSv2 answers success as 200 and mirrors the code into HTTP.
	OCSv2 OCSVersion = 2
)

// The legacy sentinel codes, so a caller can say "unauthorised" without
// writing 997 at every site.
const (
	CodeUnauthorised = 997
	CodeNotFound     = 998
	CodeServerError  = 996
	CodeUnknownError = 999
)

// SuccessCode is the statuscode a successful call reports.
//
// This is the single most commonly botched value in an OCS reimplementation.
func (v OCSVersion) SuccessCode() int {
	if v == OCSv1 {
		return 100
	}
	return 200
}

// HTTPStatus maps an internal status onto the response status.
//
// v1 pins 200 for everything except unauthorised, which is the one status it
// is allowed to leak into HTTP. v2 mirrors, with the sentinels remapped, in
// this evaluation order:
//
//	997 -> 401
//	998 -> 404
//	996 -> 500
//	999 -> 500
//	below 200 or above 600 -> 400   (note that 100 lands here)
//	otherwise -> the code itself
func (v OCSVersion) HTTPStatus(code int) int {
	if v == OCSv1 {
		if code == CodeUnauthorised {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	switch code {
	case CodeUnauthorised:
		return http.StatusUnauthorized
	case CodeNotFound:
		return http.StatusNotFound
	case CodeServerError, CodeUnknownError:
		return http.StatusInternalServerError
	}
	if code < 200 || code > 600 {
		return http.StatusBadRequest
	}
	return code
}

// OCSFormat is the requested serialisation.
type OCSFormat int

const (
	FormatXML OCSFormat = iota
	FormatJSON
)

// NegotiateFormat picks the serialisation.
//
// The query parameter wins, then Accept, then XML, which is the OCS default. A
// format parameter naming anything else is XML rather than an error: the
// reference does not refuse one.
func NegotiateFormat(query string, headers http.Header) OCSFormat {
	for _, pair := range strings.Split(query, "&") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k != "format" {
			continue
		}
		if v == "json" {
			return FormatJSON
		}
		return FormatXML
	}
	if strings.Contains(headers.Get("Accept"), "application/json") {
		return FormatJSON
	}
	return FormatXML
}

// ValKind is what a Val holds.
type ValKind int

const (
	KindNull ValKind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	KindList
	KindMap
)

// Val is a format-agnostic value tree.
//
// It exists because the two serialisations are not two renderings of one
// document: an empty collection is [] in JSON and an empty element in XML, a
// boolean is true in JSON and 1 or empty in XML, and a list item loses its
// index and becomes <element> in XML. One tree, two writers.
type Val struct {
	Kind  ValKind
	Bool  bool
	Int   int64
	Float float64
	Str   string
	List  []Val
	// Map preserves insertion order, because several clients are order
	// sensitive when parsing the XML form.
	Map []Field
}

// Field is one ordered map entry.
type Field struct {
	Key string
	Val Val
}

// The constructors.
func VNull() Val             { return Val{Kind: KindNull} }
func VBool(b bool) Val       { return Val{Kind: KindBool, Bool: b} }
func VInt(i int64) Val       { return Val{Kind: KindInt, Int: i} }
func VFloat(f float64) Val   { return Val{Kind: KindFloat, Float: f} }
func VStr(s string) Val      { return Val{Kind: KindString, Str: s} }
func VList(items ...Val) Val { return Val{Kind: KindList, List: items} }
func VEmptyList() Val        { return Val{Kind: KindList, List: []Val{}} }
func VEmptyMap() Val         { return Val{Kind: KindMap, Map: []Field{}} }

// VMap builds an ordered map.
func VMap(fields ...Field) Val { return Val{Kind: KindMap, Map: fields} }

// F is one field, for building a map readably.
func F(key string, v Val) Field { return Field{Key: key, Val: v} }

// Get looks a key up in a map value.
func (v Val) Get(key string) (Val, bool) {
	if v.Kind != KindMap {
		return Val{}, false
	}
	for _, f := range v.Map {
		if f.Key == key {
			return f.Val, true
		}
	}
	return Val{}, false
}

// Path is a dotted lookup, which the tests read shapes with.
func (v Val) Path(dotted string) (Val, bool) {
	cur := v
	for _, seg := range strings.Split(dotted, ".") {
		next, ok := cur.Get(seg)
		if !ok {
			return Val{}, false
		}
		cur = next
	}
	return cur, true
}

// JSON renders the value as the client's JSON layer expects it.
func (v Val) JSON() any {
	switch v.Kind {
	case KindNull:
		return nil
	case KindBool:
		return v.Bool
	case KindInt:
		return v.Int
	case KindFloat:
		return v.Float
	case KindString:
		return v.Str
	case KindList:
		out := make([]any, 0, len(v.List))
		for _, item := range v.List {
			out = append(out, item.JSON())
		}
		return out
	case KindMap:
		// An ordered map rendered through a plain Go map would lose its order,
		// so the writer emits it directly instead.
		out := make(map[string]any, len(v.Map))
		for _, f := range v.Map {
			out[f.Key] = f.Val.JSON()
		}
		return out
	}
	return nil
}

// writeJSON emits the value with map order preserved.
//
// encoding/json sorts a map's keys, and several clients read the XML form
// positionally; keeping one writer for both means the JSON form does not
// silently reorder either.
func (v Val) writeJSON(out []byte) ([]byte, error) {
	switch v.Kind {
	case KindNull:
		return append(out, "null"...), nil
	case KindBool:
		return strconv.AppendBool(out, v.Bool), nil
	case KindInt:
		return strconv.AppendInt(out, v.Int, 10), nil
	case KindFloat:
		return append(out, formatFloat(v.Float)...), nil
	case KindString:
		b, err := json.Marshal(v.Str)
		if err != nil {
			return nil, fmt.Errorf("nc: encoding a string: %w", err)
		}
		return append(out, b...), nil
	case KindList:
		out = append(out, '[')
		for i, item := range v.List {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			if out, err = item.writeJSON(out); err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	case KindMap:
		out = append(out, '{')
		for i, f := range v.Map {
			if i > 0 {
				out = append(out, ',')
			}
			k, err := json.Marshal(f.Key)
			if err != nil {
				return nil, fmt.Errorf("nc: encoding a key: %w", err)
			}
			out = append(out, k...)
			out = append(out, ':')
			if out, err = f.Val.writeJSON(out); err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	}
	return out, nil
}

// formatFloat renders a float the way the reference does: one that happens to
// be integral has no trailing decimal, which is exactly the quota case.
func formatFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// XMLEscapeText escapes character data.
//
// Quotes are escaped too. They are not required in character data, but the XML
// parsers on the client side are happier with them escaped and it costs
// nothing. A C0 control is dropped rather than emitted, because XML 1.0
// forbids most of them and a document carrying one is rejected wholesale.
func XMLEscapeText(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range s {
		switch c {
		case '&':
			out = append(out, "&amp;"...)
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '"':
			out = append(out, "&quot;"...)
		case '\'':
			out = append(out, "&apos;"...)
		default:
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				continue
			}
			out = utf8.AppendRune(out, c)
		}
	}
	return string(out)
}

// openClose writes one element, collapsing an empty one.
//
// The reference's XML writer collapses an element with no content to a
// self-closing tag. That applies to null, to the empty string, to false, and
// to an empty list, and emitting the open-close pair instead is not equivalent
// for every client-side parser.
func openClose(name, body string, out []byte) []byte {
	out = append(out, '<')
	out = append(out, name...)
	if body == "" {
		return append(out, "/>"...)
	}
	out = append(out, '>')
	out = append(out, body...)
	out = append(out, "</"...)
	out = append(out, name...)
	return append(out, '>')
}

func writeValXML(name string, v Val, out []byte) []byte {
	switch v.Kind {
	case KindNull:
		return openClose(name, "", out)
	case KindBool:
		// Casting a boolean to a string gives 1 or the empty string, never
		// "true" and "false".
		if v.Bool {
			return openClose(name, "1", out)
		}
		return openClose(name, "", out)
	case KindInt:
		return openClose(name, strconv.FormatInt(v.Int, 10), out)
	case KindFloat:
		return openClose(name, formatFloat(v.Float), out)
	case KindString:
		return openClose(name, XMLEscapeText(v.Str), out)
	case KindList:
		var inner []byte
		for _, item := range v.List {
			// A numeric key becomes <element>.
			inner = writeValXML("element", item, inner)
		}
		return openClose(name, string(inner), out)
	case KindMap:
		var inner []byte
		for _, f := range v.Map {
			inner = writeValXML(f.Key, f.Val, inner)
		}
		return openClose(name, string(inner), out)
	}
	return out
}

// OCSError is a failed call.
type OCSError struct {
	Code    int
	Message string
}

func (e *OCSError) Error() string { return fmt.Sprintf("ocs %d: %s", e.Code, e.Message) }

// The common refusals, so a handler names one rather than a number.
func BadRequest(m string) *OCSError { return &OCSError{Code: 400, Message: m} }

// Unauthorized uses the legacy sentinel rather than a bare 401.
//
// It is the only code v1 promotes to an HTTP 401, so a bare 401 on v1 comes
// back as HTTP 200 with a statuscode of 401, which some clients read as a soft
// failure and retry forever. On v2 the sentinel maps to 401 as well, so one
// spelling is correct on both.
func Unauthorized(m string) *OCSError {
	return &OCSError{Code: CodeUnauthorised, Message: m}
}
func Forbidden(m string) *OCSError   { return &OCSError{Code: 403, Message: m} }
func NotFound(m string) *OCSError    { return &OCSError{Code: 404, Message: m} }
func ServerError(m string) *OCSError { return &OCSError{Code: 500, Message: m} }

// OCS is one envelope.
type OCS struct {
	Version OCSVersion
	Format  OCSFormat
	Data    Val
	Err     *OCSError
}

// OK builds a successful envelope.
func OK(v OCSVersion, f OCSFormat, data Val) OCS {
	return OCS{Version: v, Format: f, Data: data}
}

// Fail builds a failed one.
func Fail(v OCSVersion, f OCSFormat, e *OCSError) OCS {
	return OCS{Version: v, Format: f, Err: e}
}

// parts is the statuscode, the status word, the message and the data.
//
// The internal code is 200 on success for both versions, and v1 then maps it
// to 100. Keeping that distinction matters because v1's HTTP status and its
// status word come from different sides of the mapping.
func (o OCS) parts() (code int, status, message string, data Val) {
	internal := 200
	if o.Err != nil {
		internal = o.Err.Code
	}

	if o.Version == OCSv1 {
		code = internal
		if internal == 200 {
			code = 100
		}
		status = "failure"
		if code == 100 {
			status = "ok"
		}
	} else {
		// v2 does not override the mapping, so the statuscode is the raw
		// internal code: a v2 404 shows 404, and a legacy 998 shows 998 with
		// HTTP 404.
		code = internal
		status = "failure"
		if internal >= 200 && internal < 300 {
			status = "ok"
		}
	}

	message = "OK"
	if o.Err != nil {
		message = o.Err.Message
	}

	data = o.Data
	if o.Err != nil {
		// An error carries an empty data node rather than a missing one:
		// clients dereference it unconditionally.
		data = VEmptyList()
	}
	if data.Kind == KindNull && o.Err == nil {
		data = VEmptyList()
	}
	return code, status, message, data
}

// meta is the envelope's metadata, in order.
//
// v1 always emits five keys, with the pagination pair present as empty
// strings. v2 emits three and omits the pair. Clients that pattern-match the
// v1 envelope notice the difference.
func (o OCS) meta() []Field {
	code, status, message, _ := o.parts()
	m := []Field{
		F("status", VStr(status)),
		F("statuscode", VInt(int64(code))),
		F("message", VStr(message)),
	}
	if o.Version == OCSv1 {
		m = append(m, F("totalitems", VStr("")), F("itemsperpage", VStr("")))
	}
	return m
}

// HTTPStatus is the response status for this envelope.
func (o OCS) HTTPStatus() int {
	internal := 200
	if o.Err != nil {
		internal = o.Err.Code
	}
	return o.Version.HTTPStatus(internal)
}

// XML renders the envelope.
func (o OCS) XML() string {
	_, _, _, data := o.parts()
	out := make([]byte, 0, 512)
	out = append(out, "<?xml version=\"1.0\"?>\n<ocs>\n <meta>\n"...)
	for _, f := range o.meta() {
		out = append(out, "  "...)
		out = writeValXML(f.Key, f.Val, out)
		out = append(out, '\n')
	}
	out = append(out, " </meta>\n "...)
	out = writeValXML("data", data, out)
	out = append(out, "\n</ocs>\n"...)
	return string(out)
}

// JSON renders the envelope.
func (o OCS) JSON() (string, error) {
	_, _, _, data := o.parts()
	out := make([]byte, 0, 512)
	out = append(out, `{"ocs":{"meta":`...)
	var err error
	if out, err = VMap(o.meta()...).writeJSON(out); err != nil {
		return "", err
	}
	out = append(out, `,"data":`...)
	if out, err = data.writeJSON(out); err != nil {
		return "", err
	}
	return string(append(out, "}}"...)), nil
}

// Write sends the envelope.
func (o OCS) Write(w http.ResponseWriter) {
	var body, contentType string
	switch o.Format {
	case FormatJSON:
		contentType = "application/json; charset=utf-8"
		encoded, err := o.JSON()
		if err != nil {
			// The envelope is this package's own value tree, so an encoding
			// failure is a bug here rather than something a client sent.
			body, contentType = `{"ocs":{"meta":{"status":"failure","statuscode":996,`+
				`"message":"the response could not be encoded"},"data":[]}}`,
				"application/json; charset=utf-8"
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(body)) //nolint:errcheck // the response is already failing.
			return
		}
		body = encoded
	default:
		contentType = "application/xml; charset=utf-8"
		body = o.XML()
	}

	w.Header().Set("Content-Type", contentType)
	// Set unconditionally, mirroring the entry points.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(o.HTTPStatus())
	_, _ = w.Write([]byte(body)) //nolint:errcheck // the status is already sent; a short write has no second channel.
}
