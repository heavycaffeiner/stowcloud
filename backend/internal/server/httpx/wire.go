// Linux only, because it serves a Linux-only engine.
//go:build linux

package httpx

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is a request value that did not parse or was out of range.
var ErrInvalid = errors.New("invalid request value")

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
