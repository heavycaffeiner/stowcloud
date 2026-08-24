package dav

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Multistatus is written by hand rather than marshalled.
//
// encoding/xml's encoder has no model for a prefix bound to a URI in scope: it
// invents prefixes, re-declares them per element, and serialises xml.Name.Space
// in forms real WebDAV clients reject. A multistatus needs D: bound to DAV: on
// the root and vendor prefixes bound alongside it, so the document is emitted
// directly.
//
// Nothing accumulates. Each response is written and flushed as it is produced,
// which is what makes a PROPFIND over a directory of a million entries a
// bounded-memory operation.
type Multistatus struct {
	w       *bufio.Writer
	flusher http.Flusher

	// prefixes maps a namespace URI to the prefix declared for it on the root.
	// A namespace not in here is declared inline on the property itself.
	prefixes map[string]string

	started  bool
	closed   bool
	err      error
	written  int
	sinceFlu int
}

// The prefix bound to DAV: on every document this writes. Clients do not care
// which prefix is used, only that it is declared, but several older ones
// assume "D" when reading examples, so it costs nothing to match them.
const davPrefix = "D"

// flushEvery is how many response elements are written between flushes. A
// flush per entry syscalls once per file; never flushing defeats streaming.
const flushEvery = 64

// NewMultistatus starts a 207 response.
//
// extra maps namespace URIs to the prefixes to declare on the root, for the
// vendor namespaces a PropSource will emit into. A source that returns a
// property in a namespace not declared here still works: the property carries
// its own declaration.
func NewMultistatus(w http.ResponseWriter, extra map[string]string) *Multistatus {
	m := &Multistatus{
		w:        bufio.NewWriterSize(w, 32<<10),
		prefixes: map[string]string{NSDav: davPrefix},
	}
	if f, ok := w.(http.Flusher); ok {
		m.flusher = f
	}
	for ns, p := range extra {
		if ns == NSDav || p == davPrefix || !isValidXMLName(p) {
			continue
		}
		m.prefixes[ns] = p
	}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	return m
}

// Prop is one property to emit.
//
// Raw holds pre-serialised child markup for the properties whose value is
// structured rather than text, such as DAV:resourcetype. It is written as-is
// and is never built from anything a client sent. Value is text and is
// escaped.
type Prop struct {
	Name  Name
	Value string
	Raw   string
}

// Response is one <D:response> element: the properties that were found, the
// names that were not, and optionally a status for the whole resource.
type Response struct {
	Href     string
	Found    []Prop
	NotFound []Name
	// Status, when non-zero, replaces the propstat pair with a single status
	// for the resource. This is how a failed member of a COPY or a DELETE is
	// reported inside a multistatus.
	Status int
	// Desc is an optional human-readable error, emitted as responsedescription.
	Desc string
}

func (m *Multistatus) start() {
	if m.started || m.err != nil {
		return
	}
	m.started = true
	m.print(`<?xml version="1.0" encoding="utf-8"?>`)
	m.print("\n<" + davPrefix + ":multistatus")
	// Sorted so the root is deterministic, which makes a golden test possible.
	for _, ns := range sortedKeys(m.prefixes) {
		m.print(` xmlns:` + m.prefixes[ns] + `="` + EscapeText(ns) + `"`)
	}
	m.print(">")
}

// Write emits one response element.
func (m *Multistatus) Write(r Response) error {
	if m.err != nil {
		return m.err
	}
	if m.closed {
		return errors.New("dav: the multistatus is already closed")
	}
	m.start()

	m.print("\n<" + davPrefix + ":response>")
	m.print("<" + davPrefix + ":href>" + EscapeHref(r.Href) + "</" + davPrefix + ":href>")

	switch {
	case r.Status != 0:
		m.writeStatus(r.Status)
	default:
		if len(r.Found) > 0 {
			m.print("<" + davPrefix + ":propstat><" + davPrefix + ":prop>")
			for _, p := range r.Found {
				m.writeProp(p)
			}
			m.print("</" + davPrefix + ":prop>")
			m.writeStatus(http.StatusOK)
			m.print("</" + davPrefix + ":propstat>")
		}
		if len(r.NotFound) > 0 {
			m.print("<" + davPrefix + ":propstat><" + davPrefix + ":prop>")
			for _, n := range r.NotFound {
				m.writeEmptyProp(n)
			}
			m.print("</" + davPrefix + ":prop>")
			m.writeStatus(http.StatusNotFound)
			m.print("</" + davPrefix + ":propstat>")
		}
	}

	if r.Desc != "" {
		m.print("<" + davPrefix + ":responsedescription>" + EscapeText(r.Desc) +
			"</" + davPrefix + ":responsedescription>")
	}
	m.print("</" + davPrefix + ":response>")

	m.written++
	m.sinceFlu++
	if m.sinceFlu >= flushEvery {
		m.flush()
	}
	return m.err
}

// Close writes the closing element and flushes.
func (m *Multistatus) Close() error {
	if m.closed {
		return m.err
	}
	m.start()
	m.closed = true
	m.print("\n</" + davPrefix + ":multistatus>\n")
	m.flush()
	return m.err
}

// Count is how many responses have been written, which the caller uses to
// decide whether a collection hit a cap.
func (m *Multistatus) Count() int { return m.written }

func (m *Multistatus) writeProp(p Prop) {
	prefix, decl := m.bind(p.Name.Space)
	if !isValidXMLName(p.Name.Local) {
		// A name that cannot be an element would break the document. This is
		// reachable from dead properties written by an older or looser store,
		// so it is dropped rather than emitted.
		return
	}
	name := qualify(prefix, p.Name.Local)

	switch {
	case p.Raw != "":
		m.print("<" + name + decl + ">" + p.Raw + "</" + name + ">")
	case p.Value == "":
		m.print("<" + name + decl + "/>")
	default:
		// Re-serialised from text, never the client's original markup, which
		// is what makes echoing a dead property back injection-free.
		m.print("<" + name + decl + ">" + EscapeText(p.Value) + "</" + name + ">")
	}
}

func (m *Multistatus) writeEmptyProp(n Name) {
	if !isValidXMLName(n.Local) {
		return
	}
	prefix, decl := m.bind(n.Space)
	m.print("<" + qualify(prefix, n.Local) + decl + "/>")
}

// bind returns the prefix to use for a namespace and any declaration that has
// to ride along on the element.
//
// A namespace declared on the root needs no declaration here. Anything else
// carries its own, because a dead property can be in a namespace nobody
// declared and inventing a root declaration for it would mean buffering the
// document to know the set in advance.
func (m *Multistatus) bind(ns string) (prefix, decl string) {
	if ns == "" {
		return "", ""
	}
	if p, ok := m.prefixes[ns]; ok {
		return p, ""
	}
	return "x", ` xmlns:x="` + EscapeText(ns) + `"`
}

func qualify(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

func (m *Multistatus) writeStatus(code int) {
	m.print("<" + davPrefix + ":status>" + statusLine(code) + "</" + davPrefix + ":status>")
}

func (m *Multistatus) print(s string) {
	if m.err != nil {
		return
	}
	if _, err := m.w.WriteString(s); err != nil {
		m.err = err
	}
}

func (m *Multistatus) flush() {
	if m.err != nil {
		return
	}
	if err := m.w.Flush(); err != nil {
		m.err = err
		return
	}
	m.sinceFlu = 0
	if m.flusher != nil {
		m.flusher.Flush()
	}
}

// statusLine renders an HTTP status the way a DAV:status element wants it.
func statusLine(code int) string {
	text := http.StatusText(code)
	if text == "" {
		// A code net/http does not name still has to produce a well-formed
		// status line rather than a truncated one.
		return "HTTP/1.1 " + strconv.Itoa(code) + " Unknown"
	}
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + text
}

// isValidXMLName reports whether s can be an element's local name.
//
// This is the conservative subset rather than the full production: letters,
// digits, and the three name punctuation characters, not starting with a digit
// or a hyphen. Anything stored that falls outside it is dropped rather than
// emitted, because a malformed name breaks the whole document for every other
// property in it.
func isValidXMLName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	// A name beginning "xml" in any case is reserved by the spec.
	if len(s) >= 3 && strings.EqualFold(s[:3], "xml") {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			continue
		case r == ':':
			// A colon would make the local name carry its own prefix, which
			// would bind to whatever the reader has in scope.
			return false
		case i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.'):
			continue
		case r > 0x7f:
			// Outside ASCII the production is large and the payoff is small:
			// no property this server emits or accepts needs it.
			return false
		default:
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: the map has a handful of entries and this avoids the
	// import for one call.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// writePropDocument writes a bare DAV:prop document, which is what LOCK
// answers with. It shares the escaping and the name validation with the
// multistatus writer rather than repeating them.
func writePropDocument(w io.Writer, props ...Prop) error {
	m := &Multistatus{
		w:        bufio.NewWriterSize(w, 4<<10),
		prefixes: map[string]string{NSDav: davPrefix},
	}
	m.print(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	m.print("<" + davPrefix + `:prop xmlns:` + davPrefix + `="` + NSDav + `">`)
	for _, p := range props {
		m.writeProp(p)
	}
	m.print("</" + davPrefix + ":prop>\n")
	if m.err != nil {
		return m.err
	}
	return m.w.Flush()
}

// WriteError renders a single-resource error body for the cases where a bare
// status is not enough, such as a lock conflict naming the holder.
func WriteError(w http.ResponseWriter, code int, cond Name, desc string) error {
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(code)

	body := `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		"<" + davPrefix + `:error xmlns:` + davPrefix + `="` + NSDav + `">`
	if cond.Local != "" && isValidXMLName(cond.Local) {
		if cond.Space == NSDav {
			body += "<" + davPrefix + ":" + cond.Local + "/>"
		} else {
			body += `<x:` + cond.Local + ` xmlns:x="` + EscapeText(cond.Space) + `"/>`
		}
	}
	if desc != "" {
		body += "<" + davPrefix + ":responsedescription>" + EscapeText(desc) +
			"</" + davPrefix + ":responsedescription>"
	}
	body += "</" + davPrefix + ":error>\n"

	if _, err := io.WriteString(w, body); err != nil {
		return fmt.Errorf("writing the error body: %w", err)
	}
	return nil
}
