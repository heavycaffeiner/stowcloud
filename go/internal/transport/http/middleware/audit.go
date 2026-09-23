// Linux only, for the same reason as the rest of this package.
//go:build linux

// The audit record a request leaves behind.
//
// The record is built from named fields rather than assembled from the request
// by a general rule, because "log the request" is how a cookie ends up in a
// log file. What may be recorded is the list below and nothing else.
package middleware

import "net/netip"

// AuditEvent is what one request contributes to the log.
//
// There is no header map, no body and no cookie. Each of those is a place a
// credential lives, and a record that carried one would turn the audit log
// into a second copy of the thing it exists to watch.
type AuditEvent struct {
	// Trace is the request id, which is what ties this record to the log lines
	// the same request produced.
	Trace string

	// Method and Route name what was asked. Route is the table's name for the
	// matched entry rather than the request's path, so a record never carries
	// a path a user typed: an id or a filename in a URL is data about that
	// user, and the route name says what kind of thing was done.
	Method string
	Route  string

	// Status is the one actually sent, which is why this step wraps the error
	// mapper rather than sitting inside it.
	Status int

	// Client is the address TrustedProxy resolved.
	Client netip.Addr

	// Principal is the account, when one was resolved. Zero means the request
	// was anonymous, which is itself worth recording.
	Principal int64

	// Credential names which kind proved the principal, never the value.
	Credential CredentialKind

	// Origin is which host role served the request.
	Origin Origin
}

// AuditSink receives the records. Implemented by whatever stores them.
type AuditSink interface {
	Record(e AuditEvent)
}

// AuditRecordFor builds the record for one request.
//
// Every argument is named, which is the point: adding a field to the record is
// an edit here, visible in a diff, rather than something a general "copy the
// request" rule would pick up on its own.
func AuditRecordFor(
	trace, method, routeName string,
	status int,
	client netip.Addr,
	principal Principal,
	origin Origin,
) AuditEvent {
	return AuditEvent{
		Trace:      trace,
		Method:     method,
		Route:      routeName,
		Status:     status,
		Client:     client,
		Principal:  principal.UserID,
		Credential: principal.Kind,
		Origin:     origin,
	}
}
