// Linux only, for the same reason as the rest of this package.
//go:build linux

// The access log: one line per request, whatever answered it.
//
// Separate from the audit record beside it, and deliberately so. The audit log
// is durable, small and about accounts acting; this is high-volume, rotated,
// and about requests arriving. Folding the two would either flood the audit
// table or leave an operator debugging a 404 with nothing to read.
//
// What may be recorded is the list below. The path is here where the audit
// record refuses one, because a request nobody can locate is not a diagnostic;
// the segments that carry a secret are replaced before it is written.
package middleware

import (
	"net/netip"
	"strings"
	"time"
)

// AccessEvent is one request as the access log sees it.
//
// No headers, no body, no cookies and no query string. Each is a place a
// credential lives, and this log is written on every request including the
// unauthenticated ones.
type AccessEvent struct {
	// Trace ties the line to everything else the same request logged.
	Trace string

	Method string

	// Route is the table's name for the matched entry, empty for the surfaces
	// that carry no route metadata: the file protocol and the compatibility
	// mount. Path is what was asked for, redacted.
	Route string
	Path  string

	// Status is the one actually sent, which is why this is recorded on the
	// way back out through the chain rather than on the way in.
	Status int

	// Duration is how long the whole chain took to answer.
	Duration time.Duration

	// Client is the address TrustedProxy resolved, not the peer.
	Client netip.Addr

	// Principal is the account, zero when the request was anonymous.
	// Credential names the kind that proved it, never the value.
	Principal  int64
	Credential CredentialKind
}

// AccessSink receives the lines. Implemented by whatever writes them.
type AccessSink interface {
	Access(e AccessEvent)
}

// pathSecretAfter lists the prefixes whose next segment is a secret.
//
// A public link's token and a login flow's token ride in the URL, so the
// request line an operator reads must not carry them: a log file is copied
// into a bug report, and a link token is the whole credential.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var pathSecretAfter = []string{
	"/s/",
	"/index.php/s/",
	"/login/v2/flow/",
	"/index.php/login/v2/flow/",
}

// RedactPath replaces the secret-bearing segment of a path with a placeholder.
//
// Only the one segment: what follows it is a path inside a share, which is
// what makes the line useful, and what precedes it says which surface was
// asked. A path that names no secret comes back unchanged.
func RedactPath(path string) string {
	for _, prefix := range pathSecretAfter {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		if rest == "" {
			return path
		}
		if cut := strings.IndexByte(rest, '/'); cut >= 0 {
			return prefix + "{token}" + rest[cut:]
		}
		return prefix + "{token}"
	}
	return path
}

// AccessRecordFor builds the line for one request.
//
// Named arguments for the same reason the audit record has them: a field is
// added by an edit here that shows up in a diff, not by a general rule that
// copies whatever the request happened to carry.
func AccessRecordFor(
	trace, method, routeName, path string,
	status int,
	took time.Duration,
	client netip.Addr,
	principal Principal,
) AccessEvent {
	return AccessEvent{
		Trace:      trace,
		Method:     method,
		Route:      routeName,
		Path:       RedactPath(path),
		Status:     status,
		Duration:   took,
		Client:     client,
		Principal:  principal.UserID,
		Credential: principal.Kind,
	}
}
