//go:build linux && compat_nc

package nc

import (
	"io"
	"net/http"
	"strconv"
)

// The multistatus writer.
//
// Three decisions here are not stylistic, and every one of them was a client
// that read nothing at all:
//
// The prefixes are fixed and lowercase. One client looks properties up by the
// literal string "d:multistatus" and "oc:fileid" rather than by namespace, so
// a document that declares the same namespaces under different prefixes parses
// as an empty listing.
//
// The successful properties are written in one propstat block, first. Another
// client reads only the first propstat of a response, and a third drops any
// block whose status line is not 200. Putting the found properties anywhere
// but first means the properties a sync needs are silently absent.
//
// An href is percent-encoded and relative. A client compares the href against
// the path it asked for and abandons the whole response when the two disagree.

// The namespaces this vocabulary uses, each with the one prefix its clients
// look for.
const (
	nsDAV       = "DAV:"
	nsOwnCloud  = "http://owncloud.org/ns"
	nsNextcloud = "http://nextcloud.org/ns"
	nsSabre     = "http://sabredav.org/ns"
	nsOCS       = "http://open-collaboration-services.org/ns"
	nsOCM       = "http://open-cloud-mesh.org/ns"
)

// prefixes is the fixed namespace-to-prefix table. A response declares every
// one of them on the root element, whatever the request asked for, so that a
// prefix-matching client finds what it is looking for in every document.
func prefixes() [6]struct {
	ns     string
	prefix string
} {
	return [6]struct {
		ns     string
		prefix string
	}{
		{nsDAV, "d"},
		{nsOwnCloud, "oc"},
		{nsNextcloud, "nc"},
		{nsSabre, "s"},
		{nsOCS, "x1"},
		{nsOCM, "x2"},
	}
}

// prefixOf gives the prefix a namespace is written under, and reports whether
// the namespace is one this document declares.
func prefixOf(ns string) (string, bool) {
	// A switch rather than a walk of the table above: this runs once per
	// property written, and a listing writes thousands.
	switch ns {
	case nsDAV:
		return "d", true
	case nsOwnCloud:
		return "oc", true
	case nsNextcloud:
		return "nc", true
	case nsSabre:
		return "s", true
	case nsOCS:
		return "x1", true
	case nsOCM:
		return "x2", true
	default:
		return "", false
	}
}

// PropName is one property's qualified name.
type PropName struct {
	NS    string
	Local string
}

// Equal compares two qualified names.
func (n PropName) Equal(o PropName) bool { return n.NS == o.NS && n.Local == o.Local }

// dav, oc and nc build a name in one of the three namespaces that carry
// almost every property.
func dav(local string) PropName { return PropName{NS: nsDAV, Local: local} }
func oc(local string) PropName  { return PropName{NS: nsOwnCloud, Local: local} }
func ncp(local string) PropName { return PropName{NS: nsNextcloud, Local: local} }

// Node is one element inside a property value.
//
// A property is not always text: a resource type is an empty element, a share
// type list is a repeated element, and a system tag carries its identity in
// attributes beside its text.
type Node struct {
	Name     PropName
	Text     string
	Attrs    []Attr
	Children []Node
}

// Attr is one attribute of a node.
type Attr struct {
	Name  PropName
	Value string
}

// Prop is one property of one resource, as a response reports it.
type Prop struct {
	Name PropName
	// Text is the value when the property is a text node.
	Text string
	// Children are the elements nested inside it, for a property whose value
	// is markup rather than text. Text is ignored when this is set.
	Children []Node
}

// TextProp builds a text-valued property.
func TextProp(name PropName, text string) Prop { return Prop{Name: name, Text: text} }

// BoolProp builds the digit spelling of a boolean, which is what every client
// here parses: the value is read as a number and any non-zero digit is true.
func BoolProp(name PropName, on bool) Prop {
	if on {
		return Prop{Name: name, Text: "1"}
	}
	return Prop{Name: name, Text: "0"}
}

// IntProp builds a number-valued property.
func IntProp(name PropName, v int64) Prop {
	return Prop{Name: name, Text: strconv.FormatInt(v, 10)}
}

// Multi writes a 207 body.
type Multi struct {
	w   io.Writer
	err error
}

// NewMulti prepares a writer over a response body.
func NewMulti(w io.Writer) *Multi { return &Multi{w: w} }

// Err reports the first write failure.
func (m *Multi) Err() error { return m.err }

// Open writes the declaration and the root element with every namespace
// declared.
func (m *Multi) Open() {
	m.write(`<?xml version="1.0" encoding="utf-8"?>`)
	m.write("<d:multistatus")
	for _, p := range prefixes() {
		m.write(` xmlns:` + p.prefix + `="` + p.ns + `"`)
	}
	m.write(">")
}

// Close writes the closing root element and reports the first failure.
func (m *Multi) Close() error {
	m.write("</d:multistatus>")
	return m.err
}

// Response writes one resource's entry.
//
// found is written first, under a 200 status. missing follows under a 404 only
// when there is something to report, because a client that reads one propstat
// per response must find the properties it can use in the one it reads.
func (m *Multi) Response(href string, found []Prop, missing []PropName) {
	m.write("<d:response><d:href>")
	m.escape(href)
	m.write("</d:href>")

	m.write("<d:propstat><d:prop>")
	for _, p := range found {
		m.property(p)
	}
	m.write("</d:prop><d:status>" + statusLine(http.StatusOK) + "</d:status></d:propstat>")

	if len(missing) > 0 {
		m.write("<d:propstat><d:prop>")
		for _, name := range missing {
			m.empty(name)
		}
		m.write("</d:prop><d:status>" + statusLine(http.StatusNotFound) + "</d:status></d:propstat>")
	}

	m.write("</d:response>")
}

// Status writes a response carrying a bare status rather than properties,
// which is what a refused member of a batch reports.
func (m *Multi) Status(href string, code int) {
	m.write("<d:response><d:href>")
	m.escape(href)
	m.write("</d:href><d:status>" + statusLine(code) + "</d:status></d:response>")
}

// property writes one property element.
func (m *Multi) property(p Prop) {
	prefix, known := prefixOf(p.Name.NS)
	if !known {
		// A namespace outside the declared table is written with its own
		// declaration on the element. Nothing in this package produces one;
		// the branch is what keeps a future property from emitting an
		// undeclared prefix, which is a document no client can parse.
		m.write("<" + p.Name.Local + ` xmlns="` + p.Name.NS + `">`)
		m.value(p)
		m.write("</" + p.Name.Local + ">")
		return
	}
	name := prefix + ":" + p.Name.Local
	if p.Text == "" && len(p.Children) == 0 {
		m.write("<" + name + "/>")
		return
	}
	m.write("<" + name + ">")
	m.value(p)
	m.write("</" + name + ">")
}

// value writes a property's content.
func (m *Multi) value(p Prop) {
	if len(p.Children) > 0 {
		for _, child := range p.Children {
			m.node(child)
		}
		return
	}
	m.escape(p.Text)
}

// node writes one nested element.
func (m *Multi) node(n Node) {
	prefix, known := prefixOf(n.Name.NS)
	name := n.Name.Local
	if known {
		name = prefix + ":" + name
	}
	m.write("<" + name)
	if !known {
		m.write(` xmlns="` + n.Name.NS + `"`)
	}
	for _, a := range n.Attrs {
		attr := a.Name.Local
		if ap, ok := prefixOf(a.Name.NS); ok && a.Name.NS != "" {
			attr = ap + ":" + attr
		}
		m.write(" " + attr + `="`)
		m.escape(a.Value)
		m.write(`"`)
	}
	if n.Text == "" && len(n.Children) == 0 {
		m.write("/>")
		return
	}
	m.write(">")
	for _, child := range n.Children {
		m.node(child)
	}
	m.escape(n.Text)
	m.write("</" + name + ">")
}

// empty writes a property name with no value, which is how a response reports
// a property it does not have.
func (m *Multi) empty(name PropName) {
	prefix, known := prefixOf(name.NS)
	if !known {
		m.write("<" + name.Local + ` xmlns="` + name.NS + `"/>`)
		return
	}
	m.write("<" + prefix + ":" + name.Local + "/>")
}

// write appends a fragment unless a previous write already failed.
func (m *Multi) write(s string) {
	if m.err != nil {
		return
	}
	if _, err := io.WriteString(m.w, s); err != nil {
		m.err = err
	}
}

// escape writes text with the predefined entities applied.
func (m *Multi) escape(s string) {
	if m.err != nil {
		return
	}
	var b xmlBuf
	escapeXML(&b, s)
	m.write(string(b.out))
}

// WriteLockDiscovery writes the body a LOCK answers with.
//
// Not a multistatus: it reports one property of one resource, so it carries no
// href and no status. It shares the element writer because the owner text
// inside it came from a client, and a second escaping path would mean trusting
// that path to match this one.
func WriteLockDiscovery(w io.Writer, token, owner string, timeoutSeconds int64) error {
	m := NewMulti(w)
	m.write(`<?xml version="1.0" encoding="utf-8"?>`)
	m.write(`<d:prop xmlns:d="DAV:"><d:lockdiscovery><d:activelock>`)
	m.write(`<d:locktype><d:write/></d:locktype><d:lockscope><d:exclusive/></d:lockscope>`)
	m.write(`<d:depth>0</d:depth><d:owner>`)
	m.escape(owner)
	m.write(`</d:owner><d:timeout>Second-` + strconv.FormatInt(timeoutSeconds, 10) + `</d:timeout>`)
	m.write(`<d:locktoken><d:href>`)
	m.escape(token)
	m.write(`</d:href></d:locktoken></d:activelock></d:lockdiscovery></d:prop>`)
	return m.err
}

// WriteDAVError writes the error document a refusal carries.
//
// One client checks the response's content type against a short list of exact
// strings before it will read the body at all, and reports "an unexpected
// response" for anything else. The type this sets is on that list.
func WriteDAVError(w http.ResponseWriter, code int, exception, message string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)

	m := NewMulti(w)
	m.write(`<?xml version="1.0" encoding="utf-8"?>`)
	m.write(`<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns"><s:exception>`)
	m.escape(exception)
	m.write(`</s:exception><s:message>`)
	m.escape(message)
	m.write(`</s:message></d:error>`)
}

// statusLine renders a status as the line a propstat carries.
func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + statusText(code)
}

// statusText names the codes a multistatus body carries. The standard
// library's table is not used because a phrase it does not know answers the
// empty string, and a status line with no phrase is malformed.
func statusText(code int) string {
	switch code {
	case http.StatusOK:
		return "OK"
	case http.StatusCreated:
		return "Created"
	case http.StatusNoContent:
		return "No Content"
	case http.StatusMultiStatus:
		return "Multi-Status"
	case http.StatusUnauthorized:
		return "Unauthorized"
	case http.StatusForbidden:
		return "Forbidden"
	case http.StatusNotFound:
		return "Not Found"
	case http.StatusMethodNotAllowed:
		return "Method Not Allowed"
	case http.StatusConflict:
		return "Conflict"
	case http.StatusPreconditionFailed:
		return "Precondition Failed"
	case http.StatusLocked:
		return "Locked"
	case http.StatusFailedDependency:
		return "Failed Dependency"
	case http.StatusInsufficientStorage:
		return "Insufficient Storage"
	default:
		return "Internal Server Error"
	}
}
