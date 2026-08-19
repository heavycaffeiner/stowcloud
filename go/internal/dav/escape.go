package dav

import (
	"bytes"
	"encoding/xml"
)

// EscapeText writes s as XML character data.
//
// xml.EscapeText also escapes the characters that only matter inside an
// attribute value, and newline and tab, which is more than a text node needs
// and never wrong.
func EscapeText(s string) string {
	var b bytes.Buffer
	// The writer is a bytes.Buffer, whose Write never returns an error, so
	// there is no failure here to report.
	_ = xml.EscapeText(&b, []byte(s)) //nolint:errcheck // bytes.Buffer.Write cannot fail.
	return b.String()
}

// EscapeHref renders a path as an href: percent-encoded first, then
// XML-escaped.
//
// The order is what makes a file named "a&b" reachable. Percent-encoding runs
// on the raw path, so the ampersand it sees is the one in the filename; the
// XML escape then runs on the encoded form, where the only remaining special
// characters are the ones percent-encoding chose to leave alone. Doing it the
// other way round would percent-encode the "&" of "&amp;" into "%26amp;" and
// address a file nobody has.
func EscapeHref(path string) string { return EscapeText(encodePath(path)) }

// encodePath percent-encodes each segment of a path, leaving the separators.
//
// url.URL.EscapedPath is not used because it is defined against a URL's own
// grammar and leaves sub-delimiters, including "&", unescaped. An href is
// consumed by XML clients that split on far less, so this encodes everything
// outside the unreserved set plus the few characters that are safe and common
// enough that encoding them makes paths unreadable in logs.
func encodePath(p string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(p))
	// Iterating bytes rather than runes is deliberate: a multi-byte rune is
	// percent-encoded one byte at a time, which is what RFC 3986 requires for
	// anything outside ASCII.
	for i := 0; i < len(p); i++ {
		c := p[i]
		if shouldEscapeInPath(c) {
			out = append(out, '%', hex[c>>4], hex[c&0x0f])
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

func shouldEscapeInPath(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return false
	}
	switch c {
	// The unreserved punctuation of RFC 3986, plus the path separator and the
	// three characters that appear constantly in real filenames and are
	// unambiguous inside a path segment.
	case '-', '_', '.', '~', '/', '@', '+', ',':
		return false
	}
	return true
}
