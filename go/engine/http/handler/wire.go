// Linux only, because it serves a Linux-only engine.
//go:build linux

// Package handler holds the wire discipline every route family shares: the
// PATCH tri-state, bounded parsing of untrusted request values, the two
// filename encodings of Content-Disposition, and the return-path check.
//
// These live together because they are the rules that must be the same
// everywhere. A second spelling of any of them is how one endpoint ends up
// accepting what the others refuse.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalid is a request value that did not parse or was out of range.
var ErrInvalid = errors.New("invalid request value")

// Value is a PATCH field with three states: absent, explicitly null, and set.
//
// One type rather than the two spellings the old code used, because a PATCH
// where "omitted" and "null" mean different things (leave alone, versus clear)
// cannot be expressed by a pointer alone: both decode to nil.
type Value[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// UnmarshalJSON records which of the three states the field arrived in.
//
// A field absent from the body never reaches this method, so Set stays false;
// a field present as null reaches it and sets Null.
func (v *Value[T]) UnmarshalJSON(raw []byte) error {
	v.Set = true
	if string(raw) == "null" {
		v.Null = true
		return nil
	}
	return json.Unmarshal(raw, &v.Value)
}

// MarshalJSON writes null for the explicit-null state and the value otherwise.
func (v Value[T]) MarshalJSON() ([]byte, error) {
	if v.Null || !v.Set {
		return []byte("null"), nil
	}
	return json.Marshal(v.Value)
}

// Int parses an integer from an untrusted request value, refusing anything
// outside the bounds.
//
// A route parameter is untrusted even where the framework matched its shape:
// matching ":id" says the segment exists, not that it is a number this server
// can act on.
func Int(raw string, min, max int64) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("%w: expected a number", ErrInvalid)
	}
	// ParseInt rather than Atoi and a cast: the overflow has to be caught
	// before any narrowing, not after.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a number", ErrInvalid, s)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%w: %d is outside %d..%d", ErrInvalid, n, min, max)
	}
	return n, nil
}

// ID parses a positive identifier.
func ID(raw string) (int64, error) {
	// Identifiers start at one. Zero is the zero value of a missing row rather
	// than a row, and accepting it here would turn a caller's bug into a
	// lookup for something that cannot exist.
	return Int(raw, 1, 1<<62)
}

// SafeReturnTo accepts a local path to send a browser back to, and refuses
// anything that could leave this origin.
//
// One leading slash and no second one: "//evil.example" is a protocol-relative
// URL that a browser resolves to another origin, and it is the case a naive
// "starts with /" check lets through.
func SafeReturnTo(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", fmt.Errorf("%w: a return path must be one local path", ErrInvalid)
	}
	// A backslash is a path separator to some browsers, so "/\evil.example"
	// is the same attack in a different spelling.
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: a return path carries a backslash", ErrInvalid)
	}
	for _, r := range raw {
		if r < 0x20 || r > 0x7e {
			return "", fmt.Errorf("%w: a return path is printable ASCII", ErrInvalid)
		}
	}
	return raw, nil
}

// ContentDisposition builds the header for a download, in both forms.
//
// The quoted form is the fallback every client understands, sanitised so no
// byte can end the quoted string or the header. The RFC 5987 form carries the
// real UTF-8 name for clients that read it.
func ContentDisposition(filename string) string {
	fallback := sanitizeFilename(filename)
	if fallback == "" {
		fallback = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		fallback, percentEncode(filename))
}

// sanitizeFilename strips everything that could escape the quoted form or the
// header itself: CR, LF, the quote, the backslash, and any control byte.
func sanitizeFilename(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r == '\r' || r == '\n':
			out = append(out, '_')
		case r < 0x20 || r == 0x7f:
			out = append(out, '_')
		case r > 0x7e:
			// Outside ASCII the fallback cannot represent it, and the RFC 5987
			// form carries the real name.
			out = append(out, '_')
		default:
			out = append(out, byte(r))
		}
	}
	return string(out)
}

// percentEncode writes the RFC 5987 attr-char set, escaping everything else.
func percentEncode(s string) string {
	const unreserved = "!#$&+-.^_`|~"
	const hex = "0123456789ABCDEF"

	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case strings.IndexByte(unreserved, c) >= 0:
			out = append(out, c)
		default:
			out = append(out, '%', hex[c>>4], hex[c&0x0f])
		}
	}
	return string(out)
}
