//go:build compat_nc

// Package nc is the compatibility layer that makes existing sync clients work.
//
// It imports the seam and nothing else from this tree. That is a gate, not a
// convention: the layer reaches core types through ncport by design, so the
// check is on direct imports rather than transitive ones.
//
// Everything vendor-specific lives here. The core knows nothing about it, and
// the seam it talks through carries no vocabulary of its own, so the direction
// of the dependency is the whole isolation argument.
package nc

import (
	"context"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// Deps is what the layer needs to answer a request.
//
// A struct of ports rather than a pile of concrete types, because the gate
// forbids the layer from importing what would provide them: the wiring package
// is the only one that sees both sides and it supplies these.
type Deps struct {
	FS       ncport.FS
	State    ncport.StatePort
	Accounts ncport.AccountPort
	Search   ncport.SearchPort
	Preview  ncport.PreviewPort
	Direct   DirectPort
	// FileID resolves an entry's stable id, which several surfaces report.
	FileID func(e ncport.Entry) (FileID, bool)
	// Authenticate resolves a request to a principal. Supplied rather than
	// implemented here: authentication is the server's, and a compat mount
	// with its own copy is how "who is this request from" stops having one
	// answer.
	Authenticate Authenticator
	// Revoke ends an app password, which the account-removal endpoint calls.
	Revoke func(ctx context.Context, user ncport.UserID, credential int64) error
	// Flow runs the device login, and is nil where it is not wired up.
	Flow *LoginFlow
	// Now is the clock. Nothing in this layer reads a wall clock directly.
	Now func() time.Time
	// Caps is what the capabilities document is built from.
	Caps CapsConfig
	// Warn reports a refusal produced before any handler runs, which this
	// layer logs because the case existed once and was invisible.
	Warn func(msg string, args ...any)
}

// Layer is the compatibility surface.
type Layer struct {
	deps Deps
	caps CapsConfig
	warn func(msg string, args ...any)
	now  func() time.Time
}

// New builds the layer.
func New(d Deps) *Layer {
	warn := d.Warn
	if warn == nil {
		warn = func(string, ...any) {}
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Layer{deps: d, caps: d.Caps, warn: warn, now: now}
}

// InstanceID is the identity this deployment presents to a client.
//
// Durable, because a client that saw one identity and then another treats the
// server as a different server and re-syncs from scratch.
func (l *Layer) InstanceID(ctx context.Context) (string, error) {
	return l.deps.State.InstanceID(ctx)
}
