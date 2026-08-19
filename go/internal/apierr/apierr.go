// Package apierr holds the shape every client-facing refusal takes on the
// native REST surface.
//
// The shape is not new. It is what the tree this replaces already sends and
// what the browser already parses, and it is restored here rather than improved
// on: one frontend build has to speak to both backends across the cutover, so a
// second envelope would be a second thing to parse and a second thing to get
// wrong.
//
//	{"error":{"code":"fs.invalid_name","message":"invalid name",
//	          "detail":{"reason_key":"share.name_empty",
//	                    "reason_params":{"field":"name"}}}}
//
// Message is a stable generic fallback. The reader's own language comes from
// the catalogue key in detail, which the browser renders from its own
// catalogue, and no lower-layer error text has a parameter to travel in. The
// request id is not in here at all: it goes back as the Sc-Trace response
// header, which is where the existing envelope keeps it.
package apierr

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Code is the machine-readable classification a client branches on. Its values
// belong to the surface that maps domain errors onto it.
type Code string

// Message is the generic fallback sentence for a code. It is a distinct type so
// that a formatted error, which is the thing this envelope exists to keep off
// the wire, is not what a caller reaches for by habit. Every value is a
// constant in the mapper.
type Message string

// MessageKey names an entry in the client's message catalogue. It is a distinct
// type so that a string cannot be passed where one belongs.
type MessageKey string

// Arg is one placeholder value for the catalogue entry. The name is what the
// entry interpolates; the value is data, not prose, so it renders the same in
// every language.
type Arg struct {
	Name  string
	Value string
}

// internalCode and internalMessage are what a 500 says, and all it says.
const (
	internalCode    Code    = "internal"
	internalMessage Message = "internal error"
)

// Error is a client-facing refusal. It is marshalled as the envelope above, and
// MarshalJSON is on this type rather than on a separate wire struct so that a
// handler cannot serialize it into any other shape by accident.
type Error struct {
	Code    Code
	Message Message

	// Key and Args are the localized half. They reach the wire as
	// detail.reason_key and detail.reason_params, and only there.
	Key  MessageKey
	Args []Arg

	// internal marks the 500. It suppresses detail on the way out, so a caller
	// that attaches one to an error it later reclassifies cannot leak it.
	internal bool
}

// NewError builds a refusal. The catalogue key may be empty, which is the
// ordinary case for a code the client already knows how to phrase.
func NewError(code Code, msg Message, key MessageKey, args ...Arg) *Error {
	return &Error{Code: code, Message: msg, Key: key, Args: args}
}

// Internal is the unhandled failure. It carries no key, no arguments and no
// detail, because there is nothing here a client may act on and the thing that
// went wrong is a server-side log line correlated by Sc-Trace.
func Internal() *Error {
	return &Error{Code: internalCode, Message: internalMessage, internal: true}
}

// Error renders the code, the generic message and the catalogue key. It reaches
// logs, and a log line is not a translation surface either.
func (e *Error) Error() string {
	parts := make([]string, 0, len(e.Args)+2)
	parts = append(parts, string(e.Code)+": "+string(e.Message))
	if e.Key != "" {
		parts = append(parts, string(e.Key))
	}
	for _, a := range e.Args {
		parts = append(parts, a.Name+"="+a.Value)
	}
	return strings.Join(parts, " ")
}

// envelope and wire are the JSON shape, and they are unexported because the
// only way to produce one is to marshal an Error.
type envelope struct {
	Error wire `json:"error"`
}

type wire struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// MarshalJSON writes the envelope.
func (e *Error) MarshalJSON() ([]byte, error) {
	w := wire{Code: e.Code, Message: string(e.Message)}
	if !e.internal && e.Key != "" {
		params := make(map[string]string, len(e.Args))
		for _, a := range e.Args {
			params[a.Name] = a.Value
		}
		w.Detail = map[string]any{"reason_key": string(e.Key), "reason_params": params}
	}
	return json.Marshal(envelope{Error: w})
}

// Write renders a refusal as the envelope. It is the one place this package
// talks to an http.ResponseWriter, so the Content-Type and the single-error
// shape cannot drift between callers.
func Write(w http.ResponseWriter, status int, err *Error) {
	if err == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(err) //nolint:errcheck // a client that stopped reading cannot be told anything.
}
