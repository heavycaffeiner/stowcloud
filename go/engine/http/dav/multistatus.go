//go:build linux

// The multistatus response writer.
//
// Everything a client sent comes back through escaping, and a namespace gets a
// prefix chosen by this file rather than by whatever the request used. A
// response that echoed a client's markup would let a stored property name put
// elements into another client's parse.
package dav

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// The fixed prefix for the DAV namespace. One spelling everywhere, so a
// golden response is a byte comparison rather than a parse.
const davPrefix = "D"

// Multistatus writes a 207 body.
type Multistatus struct {
	w io.Writer
	// prefixes maps a namespace URI to the prefix this response gave it.
	prefixes map[string]string
	// err is sticky: once a write fails, every later one is a no-op and the
	// first error is what the caller sees. Continuing after a failed write
	// produces a body that is half one response and half another.
	err error
	// open is whether the root element has been written.
	open bool
}

// NewMultistatus prepares a writer over the given namespaces.
//
// Prefixes are assigned by sorting the namespaces, so the same set always
// produces the same document. A prefix derived from arrival order makes two
// identical responses differ and a golden test impossible.
func NewMultistatus(w io.Writer, namespaces []string) *Multistatus {
	m := &Multistatus{w: w, prefixes: map[string]string{davNS: davPrefix}}

	extra := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		if ns != "" && ns != davNS {
			extra = append(extra, ns)
		}
	}
	sort.Strings(extra)

	next := 0
	for _, ns := range extra {
		if _, taken := m.prefixes[ns]; taken {
			continue
		}
		m.prefixes[ns] = fmt.Sprintf("ns%d", next)
		next++
	}
	return m
}

// Err returns the first write error, if any.
func (m *Multistatus) Err() error { return m.err }

// Open writes the declaration and the root element.
func (m *Multistatus) Open() {
	if m.err != nil || m.open {
		return
	}
	m.open = true

	m.write(`<?xml version="1.0" encoding="utf-8"?>`)
	m.write("<" + davPrefix + ":multistatus")

	for _, ns := range m.sortedNamespaces() {
		m.write(` xmlns:` + m.prefixes[ns] + `="`)
		m.escape(ns)
		m.write(`"`)
	}
	m.write(">")
}

// Close writes the closing root element.
func (m *Multistatus) Close() error {
	if m.err != nil {
		return m.err
	}
	if !m.open {
		m.Open()
	}
	m.write("</" + davPrefix + ":multistatus>")
	return m.err
}

// PropStat is one group of properties sharing a status.
type PropStat struct {
	// Status is the HTTP status for this group.
	Status int
	// Props are the properties, each already rendered as a value or empty.
	Props []Prop
}

// Prop is one property in a response.
type Prop struct {
	// Name is the property's qualified name.
	Name xml.Name
	// Value is its text content. Escaped on the way out; never written raw.
	Value string
	// NamesOnly writes the tag with no content, for a propname response.
	NamesOnly bool
}

// Response writes one resource's entry.
func (m *Multistatus) Response(href string, groups []PropStat) {
	if m.err != nil {
		return
	}
	if !m.open {
		m.Open()
	}

	m.write("<" + davPrefix + ":response><" + davPrefix + ":href>")
	m.escape(href)
	m.write("</" + davPrefix + ":href>")

	for _, group := range groups {
		m.write("<" + davPrefix + ":propstat><" + davPrefix + ":prop>")
		for _, prop := range group.Props {
			m.property(prop)
		}
		m.write("</" + davPrefix + ":prop><" + davPrefix + ":status>")
		m.escape(statusLine(group.Status))
		m.write("</" + davPrefix + ":status></" + davPrefix + ":propstat>")
	}

	m.write("</" + davPrefix + ":response>")
}

// property writes one property element.
func (m *Multistatus) property(p Prop) {
	prefix, known := m.prefixes[p.Name.Space]
	if !known {
		// A namespace nobody declared cannot be written as a prefixed tag, and
		// inventing one here would make the response depend on write order.
		// Skipping is the honest answer: the property is absent rather than
		// present under a name the client cannot resolve.
		return
	}

	tag := prefix + ":" + p.Name.Local
	if p.NamesOnly || p.Value == "" {
		m.write("<" + tag + "/>")
		return
	}
	m.write("<" + tag + ">")
	m.escape(p.Value)
	m.write("</" + tag + ">")
}

// sortedNamespaces returns the declared namespaces in a stable order.
func (m *Multistatus) sortedNamespaces() []string {
	out := make([]string, 0, len(m.prefixes))
	for ns := range m.prefixes {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// write appends a fragment unless the writer has already failed.
func (m *Multistatus) write(s string) {
	if m.err != nil {
		return
	}
	if _, err := io.WriteString(m.w, s); err != nil {
		m.err = fmt.Errorf("writing the multistatus body: %w", err)
	}
}

// escape writes text with XML escaping.
func (m *Multistatus) escape(s string) {
	if m.err != nil {
		return
	}
	if err := xml.EscapeText(m.w, []byte(s)); err != nil {
		m.err = fmt.Errorf("writing the multistatus body: %w", err)
	}
}

// statusLine renders a status as an HTTP status line.
func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + statusText(code)
}

// statusText names the codes a multistatus body carries.
func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 204:
		return "No Content"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 409:
		return "Conflict"
	case 412:
		return "Precondition Failed"
	case 423:
		return "Locked"
	case 424:
		return "Failed Dependency"
	case 507:
		return "Insufficient Storage"
	default:
		return "Unknown"
	}
}
