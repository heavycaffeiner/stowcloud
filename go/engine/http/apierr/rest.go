// Linux only, because it classifies errors from services that are Linux only.
//go:build linux

// The native JSON envelope and the REST status table.
//
// This is the only place assigning a native status, a stable code and a
// fallback message to a class. DAV and OCS have their own adapters over the
// same classes, so a change here does not silently move a DAV status.

package apierr

import (
	"encoding/json"
	"net/http"
)

// Error is the native envelope's error object.
//
// Code is the stable identifier a client branches on. Message is a fallback for
// a client with no catalogue entry. Detail carries the catalogue key and its
// substitutions, and is absent entirely on an internal error.
type Error struct {
	Code    string
	Message string
	Key     string
	Args    []Arg
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// MarshalJSON is the only envelope encoder.
//
// One encoder rather than a struct with tags, because the shape is nested and
// the detail object is conditional: an internal error carries no detail at all,
// and a second encoder would eventually disagree about that.
func (e *Error) MarshalJSON() ([]byte, error) {
	type detail struct {
		ReasonKey    string            `json:"reason_key"`
		ReasonParams map[string]string `json:"reason_params,omitempty"`
	}
	type body struct {
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Detail  *detail `json:"detail,omitempty"`
	}
	type envelope struct {
		Error body `json:"error"`
	}

	out := envelope{Error: body{Code: e.Code, Message: e.Message}}
	// An internal error suppresses detail. What went wrong inside is not the
	// caller's to read, and a key naming the internal condition is a
	// description of the fault by another route.
	if e.Key != "" && e.Code != codeInternal {
		d := &detail{ReasonKey: e.Key}
		if len(e.Args) > 0 {
			d.ReasonParams = make(map[string]string, len(e.Args))
			for _, a := range e.Args {
				d.ReasonParams[a.Name] = a.Value
			}
		}
		out.Error.Detail = d
	}
	return json.Marshal(out)
}

const codeInternal = "internal"

// restEntry is one class's native rendering.
type restEntry struct {
	status  int
	code    string
	message string
}

// REST renders a classified error as a native status and envelope.
//
// The table is exhaustive over the classes: a class with no entry would
// otherwise render as the zero value, which is status 0 and an empty code.
func REST(c Classified) (int, *Error) {
	e, ok := restTable()[c.Class]
	if !ok {
		e = restEntry{http.StatusInternalServerError, codeInternal, "internal error"}
	}
	key := c.Key
	if e.code == codeInternal {
		// Suppressed here as well as in the encoder, so a caller reading the
		// struct rather than the JSON sees the same thing.
		key = ""
	}
	return e.status, &Error{Code: e.code, Message: e.message, Key: key, Args: c.Args}
}

// restTable is the class-to-native mapping.
//
// Hidden and NotFound render identically on purpose: byte for byte, so a caller
// cannot tell a resource it may not see from one that is not there. That is the
// existence rule, and it is enforced by these two rows being the same rather
// than by a comment asking callers to be careful.
func restTable() map[Class]restEntry {
	return map[Class]restEntry{
		Internal: {http.StatusInternalServerError, codeInternal, "internal error"},

		Malformed:     {http.StatusBadRequest, "invalid_request", "malformed request"},
		Unprocessable: {http.StatusUnprocessableEntity, "unprocessable", "the request could not be processed"},
		BodyTooLarge:  {http.StatusRequestEntityTooLarge, "http.body_too_large", "the request is too large"},
		LimitExceeded: {http.StatusUnprocessableEntity, "limit_exceeded", "a configured limit was exceeded"},
		// 429 rather than 422: the caller is over a bound that clears as their
		// own work finishes, so waiting is the right response and giving up is
		// not.
		ResourceExhausted: {http.StatusTooManyRequests, "limit_exhausted", "a resource limit is momentarily exhausted"},

		AuthRequired:    {http.StatusUnauthorized, "auth.required", "authentication required"},
		AuthInvalid:     {http.StatusUnauthorized, "auth.invalid_credentials", "invalid credentials"},
		AccountDisabled: {http.StatusForbidden, "auth.account_disabled", "the account is disabled"},
		RateLimited:     {http.StatusTooManyRequests, "auth.rate_limited", "too many attempts"},

		// The two that must be indistinguishable.
		Hidden:   {http.StatusNotFound, "fs.not_found", "not found"},
		NotFound: {http.StatusNotFound, "fs.not_found", "not found"},

		Denied: {http.StatusForbidden, "fs.denied", "not permitted"},
		Gone:   {http.StatusGone, "link.expired", "no longer available"},

		Conflict:     {http.StatusConflict, "fs.conflict", "the current state conflicts"},
		Exists:       {http.StatusConflict, "fs.exists", "already exists"},
		NotEmpty:     {http.StatusConflict, "fs.not_empty", "not empty"},
		Precondition: {http.StatusPreconditionFailed, "fs.precondition_failed", "a precondition failed"},
		Locked:       {http.StatusLocked, "fs.locked", "locked"},
		RangeNotSatisfiable: {
			http.StatusRequestedRangeNotSatisfiable, "fs.range_not_satisfiable",
			"the requested range is not satisfiable",
		},

		NoSpace:          {http.StatusInsufficientStorage, "fs.no_space", "not enough space"},
		ShareUnavailable: {http.StatusServiceUnavailable, "fs.share_unavailable", "the share is unavailable"},

		LastAdmin:    {http.StatusConflict, "admin.last_admin", "the last administrator cannot be removed"},
		WeakPassword: {http.StatusUnprocessableEntity, "auth.weak_password", "the password is too weak"},
		NameTaken:    {http.StatusConflict, "admin.name_taken", "the name is taken"},

		SubsystemUnavailable: {http.StatusServiceUnavailable, "subsystem.unavailable", "temporarily unavailable"},
		NotImplemented:       {http.StatusNotImplemented, "not_implemented", "not supported by this build"},
		BadGateway:           {http.StatusBadGateway, "bad_gateway", "the target names another server"},

		SetupComplete:     {http.StatusConflict, "setup.complete", "setup is already complete"},
		SetupExpired:      {http.StatusGone, "setup.expired", "the setup window has closed"},
		SetupInvalidToken: {http.StatusUnauthorized, "setup.invalid_token", "invalid setup token"},

		FlowUnknown:  {http.StatusNotFound, "flow.unknown", "no such flow"},
		FlowPending:  {http.StatusAccepted, "flow.pending", "awaiting approval"},
		FlowApproved: {http.StatusOK, "flow.approved", "approved"},
		FlowTooSoon:  {http.StatusTooManyRequests, "flow.too_soon", "polled too soon"},
	}
}

// Write renders an error to a response.
//
// visibility travels from the caller because only the handler knows whether the
// caller reached this resource through a parent it may read.
func Write(w http.ResponseWriter, err error, visibility Visibility) {
	WriteClassified(w, Classify(err, visibility))
}

// WriteClassified answers with an outcome the caller already decided.
//
// For a refusal that is not carrying an error to classify, such as a boundary
// that has no credential to reject and simply has none.
func WriteClassified(w http.ResponseWriter, c Classified) {
	status, body := REST(c)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // a client that stopped reading cannot be told anything.
}

// Wire is one item's outcome inside a batch response.
//
// Exported only for that: a batch reports per-item results and each carries the
// same shape as a whole-response error, so the client renders both alike.
type Wire struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Key     string            `json:"reason_key,omitempty"`
	Params  map[string]string `json:"reason_params,omitempty"`
}

// WireOf renders an error as a batch item outcome.
func WireOf(err error, visibility Visibility) Wire {
	_, body := REST(Classify(err, visibility))
	out := Wire{Code: body.Code, Message: body.Message, Key: body.Key}
	if len(body.Args) > 0 && body.Key != "" {
		out.Params = make(map[string]string, len(body.Args))
		for _, a := range body.Args {
			out.Params[a.Name] = a.Value
		}
	}
	return out
}
