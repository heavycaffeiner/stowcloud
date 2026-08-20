//go:build compat_nc

package nc

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Chunked upload v2, mapped onto the upload engine's name-ordered spool mode.
//
//	MKCOL  /remote.php/dav/uploads/{user}/{tid}          create a session
//	       headers: Destination, the total length         -> 201 exactly
//	PUT    /remote.php/dav/uploads/{user}/{tid}/{name}    contribute a chunk
//	       the name is numeric, zero-padded to five       -> 201
//	MOVE   /remote.php/dav/uploads/{user}/{tid}/.file     assemble and publish
//	       headers: Destination, the total length, the mtime
//	                                                      -> 201 new, 204 over
//	DELETE /remote.php/dav/uploads/{user}/{tid}           abandon
//	PROPFIND on the collection                            the chunks held
//
// Two things here are load-bearing and easy to get wrong.
//
// The transfer id is attacker-controlled and is never a session key. The
// client derives it from a random value combined with the file's own
// properties, so it is both guessable and collidable. It is resolved through
// an alias table scoped by user id, so one account cannot name its way into
// another account's in-flight upload.
//
// The assembling response must carry the file id and a validator. The desktop
// client hard-fails the item without them even on a success, reporting a
// missing file id or an inaccessible file.

// The reference's own bound on a chunk name. A client that sends one outside
// it has a bug worth surfacing rather than papering over.
const (
	MinChunkName = 1
	MaxChunkName = 10000
)

// Chunking failures.
var (
	ErrChunkBadRequest = errors.New("nc: a malformed chunked-upload request")
	ErrChunkNotFound   = errors.New("nc: no such chunked upload")
	ErrChunkForbidden  = errors.New("nc: not permitted")
)

// The headers this surface reads. They are named here, in the layer that owns
// the vocabulary, and handed to the WebDAV package rather than written there.
const (
	HeaderTotalLength = "OC-Total-Length"
	HeaderMTime       = "X-OC-Mtime"
	HeaderCTime       = "X-OC-CTime"
	HeaderDestination = "Destination"
	HeaderFileID      = "OC-FileId"
	HeaderETag        = "OC-ETag"
)

// UploadHeaderNames is what the WebDAV collection is configured with, so that
// package reads whatever it is given and learns no vocabulary.
func UploadHeaderNames() (totalLength, mtime, etag string) {
	return HeaderTotalLength, HeaderMTime, HeaderETag
}

// ChunkName parses a chunk's name.
//
// The name decides assembly order, so it is a plain decimal within the
// reference's bound. Anything else is refused rather than coerced: a name that
// parsed loosely would assemble in an order the client did not intend.
func ChunkName(s string) (uint32, error) {
	if s == "" || len(s) > 10 {
		return 0, fmt.Errorf("%w: a chunk named %q", ErrChunkBadRequest, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%w: a chunk named %q", ErrChunkBadRequest, s)
		}
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: a chunk named %q", ErrChunkBadRequest, s)
	}
	if n < MinChunkName || n > MaxChunkName {
		return 0, fmt.Errorf("%w: chunk %d is outside %d..%d",
			ErrChunkBadRequest, n, MinChunkName, MaxChunkName)
	}
	return uint32(n), nil
}

// TotalLength reads the declared assembled size.
func TotalLength(h http.Header) (uint64, bool, error) {
	raw := strings.TrimSpace(h.Get(HeaderTotalLength))
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %s is not an integer", ErrChunkBadRequest, HeaderTotalLength)
	}
	return n, true, nil
}

// MTimeNs reads the modification time to stamp on the published file.
//
// It is a unix timestamp in seconds, converted to nanoseconds. A non-numeric
// value is refused with a client error rather than the reference's accidental
// server error.
//
// A fractional value has to be accepted. The iOS client formats this header
// from a floating-point interval, so it sends something like 1751234567.891234
// rather than a bare integer, and rejecting that as "not an integer" fails the
// final assembling request of every iOS chunked upload. It is truncated at the
// decimal point rather than rounded: the integral part is what every other
// client would have sent, and sub-second precision is not something a client
// can meaningfully assert about a file it just read from a camera roll.
func MTimeNs(h http.Header) (int64, bool, error) {
	return unixSecondsNs(h, HeaderMTime)
}

// CTimeNs reads the creation time, which the same client sends the same way.
func CTimeNs(h http.Header) (int64, bool, error) {
	return unixSecondsNs(h, HeaderCTime)
}

func unixSecondsNs(h http.Header, name string) (int64, bool, error) {
	raw := strings.TrimSpace(h.Get(name))
	if raw == "" {
		return 0, false, nil
	}
	secs, err := parseUnixSeconds(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %s is not a timestamp", ErrChunkBadRequest, name)
	}
	// The multiplication is bounded first: a timestamp far outside any real
	// one would otherwise wrap into a plausible time.
	const nsPerSecond = 1_000_000_000
	if secs > (1<<62)/nsPerSecond || secs < -(1<<62)/nsPerSecond {
		return 0, false, fmt.Errorf("%w: %s is out of range", ErrChunkBadRequest, name)
	}
	return secs * nsPerSecond, true, nil
}

// parseUnixSeconds accepts an integer or a fixed-point decimal and refuses
// anything else, including exponent notation and a second decimal point.
func parseUnixSeconds(raw string) (int64, error) {
	intPart, frac, hasFrac := strings.Cut(raw, ".")
	if hasFrac {
		if frac == "" || strings.ContainsAny(frac, ".eE") {
			return 0, fmt.Errorf("a malformed fraction")
		}
		for i := 0; i < len(frac); i++ {
			if frac[i] < '0' || frac[i] > '9' {
				return 0, fmt.Errorf("a malformed fraction")
			}
		}
	}
	if intPart == "" || intPart == "-" || intPart == "+" {
		return 0, fmt.Errorf("no integral part")
	}
	return strconv.ParseInt(intPart, 10, 64)
}

// DestinationPath reads the destination.
//
// It arrives as a full URL on the creating request and as a bare path on the
// assembling one, so both are accepted. The prefix that names the DAV mount
// and the user is stripped, leaving the path the file is published at.
func DestinationPath(h http.Header, davUser string) (string, error) {
	raw := strings.TrimSpace(h.Get(HeaderDestination))
	if raw == "" {
		return "", fmt.Errorf("%w: no %s", ErrChunkBadRequest, HeaderDestination)
	}

	p := raw
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		p = u.Path
	}
	// Percent-decoded once: the header carries an encoded path, and a name
	// containing a space or an ampersand arrives encoded.
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}

	for _, prefix := range []string{
		"/remote.php/dav/files/" + davUser,
		"/remote.php/webdav",
	} {
		if strings.HasPrefix(p, prefix) {
			p = strings.TrimPrefix(p, prefix)
			break
		}
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", fmt.Errorf("%w: the destination names no file", ErrChunkBadRequest)
	}
	// A traversal in a client-supplied path is refused here rather than left
	// for the path layer: the layer would refuse it too, but the request is
	// malformed and saying so is the better answer.
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: the destination traverses", ErrChunkBadRequest)
		}
	}
	return p, nil
}

// TransferID is a client-chosen collection name.
//
// It is never a session key. The client derives it from a random value mixed
// with the file's own properties, so it is both guessable and collidable, and
// it is resolved through an alias scoped by the calling user.
type TransferID string

// ParseTransferID validates the name.
//
// Bounded and free of separators, because it is used as a lookup key and
// appears in a log line. It is not otherwise interpreted: what it means is the
// client's business.
func ParseTransferID(s string) (TransferID, error) {
	if s == "" || len(s) > 128 {
		return "", fmt.Errorf("%w: a transfer id of %d bytes", ErrChunkBadRequest, len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return "", fmt.Errorf("%w: a transfer id containing %q", ErrChunkBadRequest, string(c))
		}
	}
	if s == "." || s == ".." {
		return "", fmt.Errorf("%w: a transfer id of %q", ErrChunkBadRequest, s)
	}
	return TransferID(s), nil
}

// AssembleResponse is what the assembling request answers with.
//
// The file id and the validator are not optional. The desktop client hard-
// fails the item without them even on a success, reporting a missing file id
// or an inaccessible file, so a response that omits either is a sync that
// stops with an error the user cannot act on.
type AssembleResponse struct {
	FileID  FileID
	ETag    string
	Created bool
}

// WriteTo sends the response.
func (a AssembleResponse) WriteTo(w http.ResponseWriter, instanceID string) {
	w.Header().Set(HeaderFileID, DavID(a.FileID, instanceID))
	w.Header().Set(HeaderETag, a.ETag)
	w.Header().Set("ETag", a.ETag)
	if a.Created {
		w.WriteHeader(http.StatusCreated)
		return
	}
	// An overwrite is 204, which is how the client tells the two apart.
	w.WriteHeader(http.StatusNoContent)
}
