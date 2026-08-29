// Linux only, for the same reason as the rest of this package.
//go:build linux

// The TUS protocol's headers.
//
// This mount is a named exception to the shared JSON and error discipline: it
// speaks a protocol with its own header spellings and its own statuses, and a
// client library implements that protocol rather than this server's
// conventions. Parsing lives here so the exception is one file rather than a
// set of special cases spread through the handlers.
package handler

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The protocol's header names, in their own spelling.
const (
	TusResumable   = "Tus-Resumable"
	TusVersion     = "Tus-Version"
	TusExtension   = "Tus-Extension"
	TusMaxSize     = "Tus-Max-Size"
	UploadOffset   = "Upload-Offset"
	UploadLength   = "Upload-Length"
	UploadDefer    = "Upload-Defer-Length"
	UploadMetadata = "Upload-Metadata"
	UploadChecksum = "Upload-Checksum"
	UploadExpires  = "Upload-Expires"
)

// TusProtocolVersion is the one version this server speaks.
const TusProtocolVersion = "1.0.0"

// StatusChecksumMismatch is the protocol's own status for a chunk whose digest
// does not match. Not in the standard library's list, and not a 4xx the shared
// mapper would produce.
const StatusChecksumMismatch = 460

// ErrTus is a protocol-level refusal.
var ErrTus = errors.New("the upload request is not valid for this protocol")

// ErrTusVersion is a client speaking a version this server does not.
var ErrTusVersion = errors.New("unsupported protocol version")

// CheckResumable verifies the version header.
//
// Absent is refused as firmly as wrong: the header is the protocol's way of
// saying which contract the request is written against, and a request without
// one is a request whose meaning has to be guessed.
func CheckResumable(header string) error {
	v := strings.TrimSpace(header)
	if v == "" {
		return fmt.Errorf("%w: no %s header", ErrTusVersion, TusResumable)
	}
	if v != TusProtocolVersion {
		return fmt.Errorf("%w: %q", ErrTusVersion, v)
	}
	return nil
}

// ParseOffset reads an Upload-Offset header.
//
// Non-negative and bounded. A negative offset would seek backwards through the
// part file and a value past the declared length is a client that has lost
// track of its own upload.
func ParseOffset(header string) (uint64, error) {
	v := strings.TrimSpace(header)
	if v == "" {
		return 0, fmt.Errorf("%w: no %s header", ErrTus, UploadOffset)
	}
	// ParseUint rather than ParseInt: a minus sign is not a small number here,
	// it is a different request.
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an offset", ErrTus, v)
	}
	return n, nil
}

// Length is a creation request's declared size.
type Length struct {
	// Deferred is a client that will announce the size later. Its Value is
	// meaningless and must not be read as zero, which is a real length.
	Deferred bool
	Value    uint64
}

// ParseLength reads the length headers as one decision.
//
// The two are mutually exclusive by the protocol, and reading them separately
// is how a request carrying both ends up treated as whichever one the code
// happened to check first.
func ParseLength(length, defer_ string) (Length, error) {
	l, d := strings.TrimSpace(length), strings.TrimSpace(defer_)

	switch {
	case l == "" && d == "":
		return Length{}, fmt.Errorf("%w: neither %s nor %s", ErrTus, UploadLength, UploadDefer)
	case l != "" && d != "":
		return Length{}, fmt.Errorf("%w: both %s and %s", ErrTus, UploadLength, UploadDefer)
	case d != "":
		// The protocol spells this exactly "1". Anything else is a client
		// that thinks it is deferring and is not.
		if d != "1" {
			return Length{}, fmt.Errorf("%w: %s is %q, not \"1\"", ErrTus, UploadDefer, d)
		}
		return Length{Deferred: true}, nil
	default:
		n, err := strconv.ParseUint(l, 10, 64)
		if err != nil {
			return Length{}, fmt.Errorf("%w: %q is not a length", ErrTus, l)
		}
		return Length{Value: n}, nil
	}
}

// ParseMetadata reads an Upload-Metadata header.
//
// The format is comma-separated pairs of a key and a base64 value, and the
// value is a filename the client chose. It is decoded here and validated by
// whoever uses it: this function's job is to refuse what is not the format,
// not to decide what a good filename is.
func ParseMetadata(header string, maxPairs int) (map[string]string, error) {
	out := map[string]string{}
	h := strings.TrimSpace(header)
	if h == "" {
		return out, nil
	}

	pairs := strings.Split(h, ",")
	if len(pairs) > maxPairs {
		return nil, fmt.Errorf("%w: %d metadata pairs, the bound is %d", ErrTus, len(pairs), maxPairs)
	}

	for _, pair := range pairs {
		key, encoded, hasValue := strings.Cut(strings.TrimSpace(pair), " ")
		if key == "" {
			return nil, fmt.Errorf("%w: a metadata pair names no key", ErrTus)
		}
		if strings.ContainsAny(key, ",= ") {
			return nil, fmt.Errorf("%w: the metadata key %q is not a token", ErrTus, key)
		}
		if _, dup := out[key]; dup {
			// A repeated key has no defined winner, and picking one silently
			// means two clients disagree about what they sent.
			return nil, fmt.Errorf("%w: the metadata key %q appears twice", ErrTus, key)
		}
		if !hasValue {
			// A key with no value is legal and means the key is present as a
			// flag.
			out[key] = ""
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("%w: the value of %q is not base64", ErrTus, key)
		}
		out[key] = string(raw)
	}
	return out, nil
}
