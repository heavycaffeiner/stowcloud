package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// What is allowed to differ, and why.
//
// The list being short is the point. Everything not named here is a failure,
// and each entry carries the reason it is not.

// allowedHeaders are the response headers whose values may differ.
func allowedHeaders() map[string]string {
	return map[string]string{
		// The wall clock.
		"date": "the wall clock",
		// A per-request id. There is no duplicate of it in the body, which is
		// what would make this a body difference too.
		"sc-trace": "a per-request id",
		// If it is sent at all.
		"server": "the implementation's own name",
		// A new session is a new token every time.
		"set-cookie": "a fresh session token",
		// The two builds may frame a response differently while sending the
		// same bytes, and the body comparison is what checks the bytes.
		"content-length":    "framing, and the body itself is compared",
		"transfer-encoding": "framing, and the body itself is compared",
		// A keepalive decision, not a response.
		"connection":  "a connection decision",
		"keep-alive":  "a connection decision",
		"retry-after": "measured from the wall clock",
	}
}

// The one token change that is expected, and the only one.
//
// The old build emits a metadata-derived token as though it were exact. The new
// build marks the same token as advisory, because Linux exposes no inode change
// version to derive an exact one from. The token itself does not change: only
// the marker in front of it does, and a token that differs in any other way is
// a failure.
func etagOnlyGainedTheWeakMarker(old, want string) bool {
	if old == want {
		return true
	}
	return "W/"+old == want
}

// Difference is one field that did not match.
type Difference struct {
	Group  string
	Name   string
	Method string
	Path   string
	Fields []FieldDiff
}

// FieldDiff names what differed and what each side said.
type FieldDiff struct {
	Field string
	Old   string
	New   string
}

// RequestError is a request one side could not answer at all.
type RequestError struct {
	Group string
	Name  string
	Old   string
	New   string
}

// compare returns every difference outside the allow list.
func compare(old, updated *Response) []FieldDiff {
	var out []FieldDiff

	if old.Status != updated.Status {
		out = append(out, FieldDiff{
			Field: "status",
			Old:   strconv.Itoa(old.Status),
			New:   strconv.Itoa(updated.Status),
		})
	}
	out = append(out, compareHeaders(old.Headers, updated.Headers)...)
	out = append(out, compareBodies(old, updated)...)
	return out
}

// compareHeaders compares the header sets.
//
// As a set, not a sequence: the order two implementations emit headers in is
// not something a client may depend on, and asserting it would fail on a
// difference nobody can observe.
func compareHeaders(old, updated http.Header) []FieldDiff {
	allowed := allowedHeaders()
	var out []FieldDiff

	names := map[string]struct{}{}
	for k := range old {
		names[strings.ToLower(k)] = struct{}{}
	}
	for k := range updated {
		names[strings.ToLower(k)] = struct{}{}
	}

	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		if _, ok := allowed[name]; ok {
			continue
		}
		o, u := old.Get(name), updated.Get(name)
		if o == u {
			continue
		}
		// The one expected token change.
		if name == "etag" && etagOnlyGainedTheWeakMarker(o, u) {
			continue
		}
		out = append(out, FieldDiff{Field: "header " + name, Old: o, New: u})
	}
	return out
}

// compareBodies compares the bodies structurally.
//
// For JSON, because one implementation orders object keys by declaration and
// the other alphabetically, and a textual comparison would report every
// response as different while no client could tell.
//
// For XML, canonicalised: a namespace prefix is a local choice and the resolved
// name is what a client reads, so prefixes may differ and resolved names may
// not.
func compareBodies(old, updated *Response) []FieldDiff {
	kind := bodyKind(old, updated)
	switch kind {
	case bodyJSON:
		o, oerr := canonicalJSON(old.Body)
		u, uerr := canonicalJSON(updated.Body)
		if oerr == nil && uerr == nil {
			if o != u {
				return []FieldDiff{{Field: "body (json)", Old: o, New: u}}
			}
			return nil
		}
	case bodyXML:
		o, oerr := canonicalXML(old.Body)
		u, uerr := canonicalXML(updated.Body)
		if oerr == nil && uerr == nil {
			if o != u {
				return []FieldDiff{{Field: "body (xml)", Old: o, New: u}}
			}
			return nil
		}
	}

	if string(old.Body) != string(updated.Body) {
		return []FieldDiff{{
			Field: "body (bytes)",
			Old:   preview(old.Body),
			New:   preview(updated.Body),
		}}
	}
	return nil
}

type bodyType int

const (
	bodyOpaque bodyType = iota
	bodyJSON
	bodyXML
)

func bodyKind(old, updated *Response) bodyType {
	ct := strings.ToLower(old.Headers.Get("Content-Type") + " " + updated.Headers.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "json"):
		return bodyJSON
	case strings.Contains(ct, "xml"):
		return bodyXML
	}
	return bodyOpaque
}

// canonicalJSON re-encodes a document with its keys ordered, so a difference in
// key order is not a difference.
func canonicalJSON(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(sortValue(v))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// sortValue walks a decoded document. A map already round-trips with sorted
// keys through the encoder, so this exists to normalise the numbers a decoder
// widens and to keep the walk explicit.
func sortValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			out[k] = sortValue(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = sortValue(sub)
		}
		return out
	}
	return v
}

// canonicalXML re-encodes a document with every name resolved, so a prefix
// difference is not a difference and a resolved-name difference is.
func canonicalXML(raw []byte) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attrs := make([]string, 0, len(t.Attr))
			for _, a := range t.Attr {
				// A namespace declaration is the prefix machinery itself, and
				// comparing it would be comparing the local choice rather than
				// what it resolves to.
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				attrs = append(attrs, fmt.Sprintf("%s|%s=%s", a.Name.Space, a.Name.Local, a.Value))
			}
			sort.Strings(attrs)
			out = append(out, fmt.Sprintf("<%s|%s %s>", t.Name.Space, t.Name.Local, strings.Join(attrs, " ")))
		case xml.EndElement:
			out = append(out, fmt.Sprintf("</%s|%s>", t.Name.Space, t.Name.Local))
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return strings.Join(out, ""), nil
}

func preview(b []byte) string {
	const max = 400
	s := string(b)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
