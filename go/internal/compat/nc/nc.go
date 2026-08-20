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
}

// Layer is the compatibility surface.
type Layer struct {
	deps Deps
}

// New builds the layer.
func New(d Deps) *Layer { return &Layer{deps: d} }

// InstanceID is the identity this deployment presents to a client.
//
// Durable, because a client that saw one identity and then another treats the
// server as a different server and re-syncs from scratch.
func (l *Layer) InstanceID(ctx context.Context) (string, error) {
	return l.deps.State.InstanceID(ctx)
}
