// Linux only, because it serves a Linux-only engine.
//go:build linux

// URL path handling for the protocol mount.
//
// The one rule the rest of the package depends on: a path is split on "/"
// before any segment is decoded. Decoding first would let an encoded separator
// produce a boundary that appears after the check that looked at it, so a
// segment that passed a traversal check could become two.
package dav

import (
	"errors"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrBadEscape reports a percent sequence that is not two hex digits.
	ErrBadEscape = errors.New("malformed percent escape")
	// ErrEncodedSeparator reports a segment that decodes to contain "/".
	ErrEncodedSeparator = errors.New("an encoded separator in a segment")
	// ErrNUL reports a NUL byte, which no path may carry.
	ErrNUL = errors.New("a NUL byte in a path")
	// ErrDotSegment reports a "." or ".." segment.
	ErrDotSegment = errors.New("a dot segment in a path")
)

// SplitPath decodes a raw URL path into its segments.
//
// Empty segments are dropped, so "/a//b" and "/a/b" are the same path. The
// result is never nil for a valid path; the root decodes to an empty slice.
func SplitPath(raw string) ([]string, error) {
	raw = strings.TrimPrefix(raw, "/")

	var out []string
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" {
			continue
		}
		decoded, err := unescapeSegment(seg)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.Contains(decoded, "/"):
			return nil, ErrEncodedSeparator
		case strings.IndexByte(decoded, 0) >= 0:
			return nil, ErrNUL
		case decoded == "." || decoded == "..":
			return nil, ErrDotSegment
		}
		out = append(out, decoded)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// unescapeSegment decodes percent escapes in one segment.
//
// Written out rather than calling url.PathUnescape because that function
// leaves "+" alone in a path but the difference is worth stating here: this
// decoder never treats "+" as a space, on any input.
func unescapeSegment(seg string) (string, error) {
	if !strings.Contains(seg, "%") {
		return seg, nil
	}

	out := make([]byte, 0, len(seg))
	for i := 0; i < len(seg); {
		if seg[i] != '%' {
			out = append(out, seg[i])
			i++
			continue
		}
		if i+2 >= len(seg) {
			return "", ErrBadEscape
		}
		hi, hiOK := unhex(seg[i+1])
		lo, loOK := unhex(seg[i+2])
		if !hiOK || !loOK {
			return "", ErrBadEscape
		}
		out = append(out, hi<<4|lo)
		i += 3
	}
	return string(out), nil
}

// unhex converts one hex digit.
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// HrefOf renders the request's own path as the response href for that
// resource: a collection's href carries the trailing slash a client appends
// member names to, a file's does not.
//
// The href a client can use is the one it just addressed. Deriving it from
// the entry's share-relative path instead produced hrefs like "/a.txt" — a
// path that exists in no share and answers 404 to the very client that read
// it.
func HrefOf(p string, isDir bool) string {
	if p == "" {
		p = "/"
	}
	if isDir && !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	if !isDir {
		return strings.TrimSuffix(p, "/")
	}
	return p
}

// EncodeHref renders segments as a URL path safe to place in XML text.
//
// The standard path escaper leaves the sub-delimiters alone, and "&" unescaped
// inside an href element is markup rather than data. This encoder is the only
// one in the tree, so native and compatibility responses cannot disagree about
// what a path looks like on the wire.
func EncodeHref(segments []string, collection bool) string {
	out := []byte{'/'}
	for i, seg := range segments {
		if i > 0 {
			out = append(out, '/')
		}
		out = appendEscaped(out, seg)
	}
	if collection && len(segments) > 0 {
		out = append(out, '/')
	}
	return string(out)
}

// appendEscaped percent-encodes everything outside the unreserved set.
//
// Unreserved only: anything else, including every sub-delimiter and every
// non-ASCII byte, is escaped. A larger allowed set would need a second
// argument about what each character means in XML.
func appendEscaped(out []byte, seg string) []byte {
	const hexDigits = "0123456789ABCDEF"
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if unreserved(c) {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexDigits[c>>4], hexDigits[c&0x0F])
	}
	return out
}

// unreserved reports the RFC 3986 unreserved set.
func unreserved(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '.' || c == '_' || c == '~':
		return true
	default:
		return false
	}
}
