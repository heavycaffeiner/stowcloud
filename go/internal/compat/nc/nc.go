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

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// Deps is what the layer needs to answer a request.
//
// A struct of ports rather than a pile of concrete types, because the gate
// forbids the layer from importing what would provide them: the wiring package
// is the only one that sees both sides and it supplies these.
type Deps struct {
	FS    ncport.FS
	State ncport.StatePort
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
}

// New builds the layer.
func New(d Deps) *Layer {
	warn := d.Warn
	if warn == nil {
		warn = func(string, ...any) {}
	}
	return &Layer{deps: d, caps: d.Caps, warn: warn}
}

// InstanceID is the identity this deployment presents to a client.
//
// Durable, because a client that saw one identity and then another treats the
// server as a different server and re-syncs from scratch.
func (l *Layer) InstanceID(ctx context.Context) (string, error) {
	return l.deps.State.InstanceID(ctx)
}
