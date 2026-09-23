//go:build linux

// Package httpheader builds security-sensitive response header values shared
// by protocol adapters.
package httpheader

import (
	"fmt"
	"strings"
)

// SafeInlineCSP prevents inline files from executing with the application
// origin while keeping passive browser previews available.
const SafeInlineCSP = "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'"

// Attachment builds Content-Disposition with an ASCII fallback and an RFC 5987
// UTF-8 filename. Both forms reject header and quoted-string delimiters.
func Attachment(filename string) string {
	fallback := sanitizeFilename(filename)
	if fallback == "" {
		fallback = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		fallback, percentEncode(filename))
}

// IsExecutableMIME reports whether a top-level browser response can execute
// active content or script.
func IsExecutableMIME(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	base = strings.TrimSpace(strings.ToLower(base))
	switch base {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"text/javascript", "application/javascript", "application/x-javascript",
		"text/ecmascript", "application/ecmascript",
		"text/xml", "application/xml", "text/xsl", "application/xslt+xml":
		return true
	}
	return false
}

func sanitizeFilename(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r == '\r' || r == '\n':
			out = append(out, '_')
		case r < 0x20 || r == 0x7f:
			out = append(out, '_')
		case r > 0x7e:
			out = append(out, '_')
		default:
			out = append(out, byte(r))
		}
	}
	return string(out)
}

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
