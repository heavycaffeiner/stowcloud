// Linux only, for the same reason as the rest of this package.
//go:build linux

// The event surfaces: search results streamed as they are found, and change
// notifications pushed to a connected client.
//
// Both share one rule. A frame says that something happened, never what it
// contains: an invalidation names a path and nothing else, so a client that
// has lost its permission since subscribing learns only that a directory it
// used to see has changed, and its re-fetch is what applies the current
// answer.
package handler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SSE event names. Two, because a stream that ends without saying so is
// indistinguishable from a connection that dropped.
const (
	// SSEHit is one permission-filtered result.
	SSEHit = "hit"
	// SSEDone ends the stream, whether the search finished or failed. A
	// post-commitment failure arrives here rather than as a status, because
	// the status is long gone by then.
	SSEDone = "done"
)

// SSEDoneView ends a stream.
//
// Error and the other two fields are mutually exclusive in practice: a search
// that failed reports why, and one that finished reports what it produced.
type SSEDoneView struct {
	Truncated bool   `json:"truncated"`
	Tier      string `json:"tier"`
	// Error carries a failure that happened after the stream was committed.
	// It is a code rather than a message, for the same reason a health reason
	// is: this text reaches a client that may not be entitled to detail.
	Error string `json:"error,omitempty"`
}

// SSEFrame renders one event in the wire format.
//
// The data is encoded first and checked for a newline, because a newline
// inside the payload would end the frame early and the rest would arrive as a
// second, malformed event.
func SSEFrame(event string, data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if strings.ContainsAny(string(raw), "\r\n") {
		// json.Marshal escapes newlines inside strings, so reaching this means
		// something bypassed the encoder. Refusing beats emitting a frame that
		// splits into two.
		return "", fmt.Errorf("%w: the payload carries a line break", ErrInvalid)
	}
	if strings.ContainsAny(event, "\r\n:") {
		return "", fmt.Errorf("%w: the event name is not a token", ErrInvalid)
	}
	return "event: " + event + "\ndata: " + string(raw) + "\n\n", nil
}

// SSEComment is the no-op frame sent immediately, so the client and any proxy
// between them see an established stream rather than waiting on a first
// result that may take seconds.
func SSEComment() string { return ": open\n\n" }

// WebSocket frame types.
const (
	// WSSubscribe and WSUnsubscribe carry paths.
	WSSubscribe   = "sub"
	WSUnsubscribe = "unsub"
	// WSPing and WSPong keep a connection alive and prove the peer is
	// answering, which is what closes a half-open one.
	WSPing = "ping"
	WSPong = "pong"
	// WSInvalidate names one path that changed.
	WSInvalidate = "inval"
)

// WSFrame is one frame in either direction.
//
// There is no content field, no etag and no metadata. An invalidation says a
// path changed and the client re-fetches, which is what re-applies permission:
// pushing the data would deliver what the subscriber was entitled to when it
// subscribed rather than now.
type WSFrame struct {
	Type string `json:"t"`
	// Paths carries a subscribe or unsubscribe request.
	Paths []string `json:"paths,omitempty"`
	// Path names the single changed path of an invalidation.
	Path string `json:"path,omitempty"`
}

// ParseWSFrame decodes a client frame, bounded before it is decoded.
//
// maxPaths bounds the list because a subscribe naming a hundred thousand paths
// costs a resolve each, and the bound belongs before the decode rather than
// after: refusing a frame already parsed has already paid for it.
func ParseWSFrame(raw []byte, maxBytes, maxPaths int) (WSFrame, error) {
	if len(raw) > maxBytes {
		return WSFrame{}, fmt.Errorf("%w: the frame is %d bytes", ErrInvalid, len(raw))
	}

	var f WSFrame
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return WSFrame{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}

	switch f.Type {
	case WSSubscribe, WSUnsubscribe:
		if len(f.Paths) == 0 {
			return WSFrame{}, fmt.Errorf("%w: %s names no path", ErrInvalid, f.Type)
		}
		if len(f.Paths) > maxPaths {
			return WSFrame{}, fmt.Errorf("%w: %d paths, the bound is %d",
				ErrInvalid, len(f.Paths), maxPaths)
		}
	case WSPing, WSPong:
		if len(f.Paths) > 0 || f.Path != "" {
			return WSFrame{}, fmt.Errorf("%w: %s carries a path", ErrInvalid, f.Type)
		}
	case WSInvalidate:
		// Server to client only. A client sending one would be asking the
		// server to tell other clients something changed.
		return WSFrame{}, fmt.Errorf("%w: a client does not send %s", ErrInvalid, f.Type)
	default:
		return WSFrame{}, fmt.Errorf("%w: unknown frame type %q", ErrInvalid, f.Type)
	}
	return f, nil
}

// InvalidationFrame builds the one frame the server pushes.
func InvalidationFrame(path string) WSFrame {
	return WSFrame{Type: WSInvalidate, Path: path}
}
