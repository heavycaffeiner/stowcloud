//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// The two things a LOCK request carries outside its path: the lease it asks
// for, and the body describing the lock it wants.

// ErrBadLockInfo is a LOCK body that is not a lockinfo document.
var ErrBadLockInfo = errors.New("a malformed lockinfo body")

// LockInfo is a parsed LOCK body.
type LockInfo struct {
	// Owner is the client's own description of who holds the lock, as text.
	//
	// Text and not markup. RFC 4918 allows arbitrary content here, and clients
	// send an href or a name; keeping the markup would mean echoing a client's
	// elements into every later PROPFIND, so only the character data survives
	// and it is escaped on the way out.
	Owner string
	// Shared asks for the cooperative scope. Absent means exclusive, which is
	// the default RFC 4918 gives.
	Shared bool
	// Write is whether a write lock was asked for. This server offers no
	// other type, so a body naming something else is refused rather than
	// answered with a lock the client did not request.
	Write bool
}

// ParseLockInfo reads a LOCK body.
//
// An empty body is not handled here: that is a refresh, which carries its
// token in the If header and has no document to read.
func ParseLockInfo(body io.Reader, lim Limits) (LockInfo, error) {
	s := NewScanner(body, lim)

	var (
		out LockInfo
		// sawRoot is whether the lockinfo wrapper has opened. Anything before
		// it is a document this is not.
		sawRoot bool
		// capturing is whether the owner element is open, and depth counts
		// markup inside it so a nested end tag does not close it early.
		capturing bool
		depth     int
		owner     []byte
	)

	for {
		tok, err := s.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return LockInfo{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case capturing:
				// Markup inside the owner. Counted so its end tag is matched
				// here rather than read as the owner closing.
				depth++

			case !sawRoot:
				if t.Name.Space != davNS || t.Name.Local != "lockinfo" {
					return LockInfo{}, ErrBadLockInfo
				}
				sawRoot = true

			case t.Name.Space == davNS && t.Name.Local == "write":
				out.Write = true

			case t.Name.Space == davNS && t.Name.Local == "shared":
				out.Shared = true

			case t.Name.Space == davNS && t.Name.Local == "owner":
				capturing = true
				owner = owner[:0]
			}

		case xml.EndElement:
			switch {
			case capturing && depth > 0:
				depth--
			case capturing:
				capturing = false
				out.Owner = strings.TrimSpace(string(owner))
			}

		case xml.CharData:
			if capturing {
				if len(owner)+len(t) > lim.TextBytes {
					return LockInfo{}, ErrTooMuchText
				}
				owner = append(owner, t...)
			}
		}
	}

	if !sawRoot {
		return LockInfo{}, ErrBadLockInfo
	}
	return out, nil
}

// ParseTimeout reads the Timeout header, in seconds.
//
// The header is a preference and not a demand: RFC 4918 lets a server pick its
// own lease, and this returns what was asked for so the caller can clamp it.
// Zero means the client expressed none.
//
// A list is walked in order and the first usable value wins, which is what the
// header's own grammar describes: a client offers alternatives in preference
// order.
func ParseTimeout(header string) time.Duration {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)

		if strings.EqualFold(part, "Infinite") {
			// Not actually unbounded. The caller clamps, and "as long as you
			// will give me" is what the client is saying.
			return InfiniteTimeout
		}

		rest, ok := cutPrefixFold(part, "Second-")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		// Bounded before the multiplication rather than after: a large value
		// would otherwise overflow into a negative duration, which reads as no
		// lease at all.
		if n > int64(InfiniteTimeout/time.Second) {
			return InfiniteTimeout
		}
		return time.Duration(n) * time.Second
	}
	return 0
}

// InfiniteTimeout is what "Infinite" and any absurd figure are read as. The
// caller clamps it to whatever lease it is willing to grant.
const InfiniteTimeout = time.Duration(1<<63 - 1)

// cutPrefixFold is strings.CutPrefix, case-insensitively.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}
