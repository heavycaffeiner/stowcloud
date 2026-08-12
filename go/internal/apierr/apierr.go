// Package apierr holds the shape every client-facing refusal takes.
//
// The constructor takes a MessageKey rather than a string, and that is the
// whole mechanism: a sentence in any language cannot reach a response body
// because there is no parameter to put one in. The browser renders the key
// from its own catalogue, in the reader's language, which the server has no
// business deciding.
package apierr

import "strings"

// Code is the machine-readable classification a client branches on. Its values
// belong to the surface that maps domain errors onto it.
type Code string

// MessageKey names an entry in the client's message catalogue. It is a distinct
// type so that a string cannot be passed where one belongs.
type MessageKey string

// Arg is one placeholder value for the catalogue entry. The name is what the
// entry interpolates; the value is data, not prose, so it renders the same in
// every language.
type Arg struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Error is what reaches a client. Msg is a catalogue key with placeholders,
// never a sentence, and no lower-layer error text reaches the wire through it.
type Error struct {
	Code    Code       `json:"code"`
	Msg     MessageKey `json:"msg"`
	Args    []Arg      `json:"args,omitempty"`
	TraceID string     `json:"trace"`
}

// NewError builds a wire error. TraceID is set by the responder, which is the
// only layer that knows the request.
func NewError(code Code, key MessageKey, args ...Arg) *Error {
	return &Error{Code: code, Msg: key, Args: args}
}

// Error renders the code and the key, never a sentence, because this string
// reaches logs and a log line is not a translation surface either.
func (e *Error) Error() string {
	parts := make([]string, 0, len(e.Args)+1)
	parts = append(parts, string(e.Code)+": "+string(e.Msg))
	for _, a := range e.Args {
		parts = append(parts, a.Name+"="+a.Value)
	}
	return strings.Join(parts, " ")
}
