//go:build linux && compat_nc

package nc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	elemRoot = "ocs"
	elemMeta = "meta"
	elemData = "data"
	elemItem = "element"
)
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

type Version int

const (
	V1 Version = 1
	V2 Version = 2
)

func (v Version) SuccessCode() int {
	if v == V1 {
		return StatusOKv1
	}
	return StatusOKv2
}
func (v Version) HTTPStatus(code int) int {
	if v == V1 {
		if code == StatusUnauthorized {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	switch code {
	case StatusUnauthorized:
		return http.StatusUnauthorized
	case StatusFailure:
		return http.StatusInternalServerError
	}
	if code < 100 || code > 599 {
		return http.StatusBadRequest
	}
	return code
}

type Format int

const (
	FormatXML Format = iota
	FormatJSON
)

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
func (f Format) ContentType() string {
	if f == FormatJSON {
		return "application/json; charset=utf-8"
	}
	return "application/xml; charset=utf-8"
}

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string     { return fmt.Sprintf("ocs %d: %s", e.Code, e.Message) }
func BadRequest(m string) *Error   { return &Error{StatusBadRequest, m} }
func Forbidden(m string) *Error    { return &Error{StatusForbidden, m} }
func NotFound(m string) *Error     { return &Error{StatusNotFound, m} }
func Failure(m string) *Error      { return &Error{StatusFailure, m} }
func Unavailable(m string) *Error  { return &Error{StatusUnavailable, m} }
func Unauthorized(m string) *Error { return &Error{StatusUnauthorized, m} }

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

type Val struct {
	kind  valKind
	text  string
	num   int64
	real  float64
	flag  bool
	pairs []Pair
	items []Val
	item  string
}
type Pair struct {
	Key string
	Val Val
}

func Str(s string) Val                 { return Val{kind: valString, text: s} }
func Int(i int64) Val                  { return Val{kind: valInt, num: i} }
func Float(f float64) Val              { return Val{kind: valFloat, real: f} }
func Bool(b bool) Val                  { return Val{kind: valBool, flag: b} }
func Absent() Val                      { return Val{kind: valAbsent} }
func P(k string, v Val) Pair           { return Pair{k, v} }
func Obj(p ...Pair) Val                { return Val{kind: valObject, pairs: p} }
func List(i ...Val) Val                { return Val{kind: valList, items: i, item: elemItem} }
func NamedList(n string, i ...Val) Val { return Val{kind: valList, items: i, item: n} }
func (v Val) IsAbsent() bool           { return v.kind == valAbsent }

var ErrElementName = errors.New("nc: illegal element name")

func (s *Server) WriteOCS(c *gin.Context, v Version, f Format, data Val) {
	s.writeEnvelope(c, v, f, v.SuccessCode(), "OK", data)
}
func (s *Server) WriteOCSError(c *gin.Context, v Version, f Format, e *Error) {
	s.writeEnvelope(c, v, f, e.Code, e.Message, Absent())
}
func (s *Server) writeEnvelope(c *gin.Context, v Version, f Format, code int, message string, data Val) {
	root := Obj(P(elemMeta, Obj(P("status", Str(metaStatus(code))), P("statuscode", Int(int64(code))), P("message", Str(message)), P("totalitems", Str("")), P("itemsperpage", Str("")))), P(elemData, data))
	body, err := render(root, f)
	if err != nil {
		s.log.Error("an OCS response could not be rendered", "error", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("Content-Type", f.ContentType())
	c.Data(v.HTTPStatus(code), f.ContentType(), body)
}
func metaStatus(code int) string {
	if code == StatusOKv1 || code == StatusOKv2 {
		return "ok"
	}
	return "failure"
}

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
func writeJSONDoc(w io.Writer, root Val) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{elemRoot: jsonValue(root)})
}
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
			if !p.Val.IsAbsent() {
				out[p.Key] = jsonValue(p.Val)
			}
		}
		return out
	default:
		return map[string]any{}
	}
}

type xmlBuf struct{ out []byte }

func (x *xmlBuf) s(v string) { x.out = append(x.out, v...) }
func writeXMLDoc(w *xmlBuf, root Val) error {
	w.s(`<?xml version="1.0"?>`)
	return writeXMLElement(w, elemRoot, root)
}
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
			} else {
				w.s(string(r))
			}
		default:
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			w.s(string(r))
		}
	}
}
