// Linux only, like the walk that consumes a Source: a source names a
// *vfs.ShareRoot, which is an openat2 handle and exists on no other platform.
//go:build linux

package search

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Source is one share to walk.
type Source struct {
	Share uint32
	Root  *vfs.ShareRoot
	// Base is where under the share to start.
	Base vfs.SafePath
	// Prefix is prepended to a reported path, so a hit names what the caller
	// asked about rather than a share-relative fragment.
	Prefix string
	// Allow reports whether the caller may see a path. It runs before an entry
	// is scored, which is what keeps a query matching many invisible entries
	// from taking measurably longer than one matching none.
	//
	// Nil means everything, which is the administrator-scoped form: the walker
	// skips the call rather than treating nil as a call that returns true, so
	// the administrator path pays no per-entry closure cost. A non-nil closure
	// is called from several goroutines at once and has to be safe for that on
	// its own.
	Allow func(p vfs.SafePath, isDir bool) bool
}

// sourceOf converts the core's shape into this package's walker input.
//
// The dependency points this way and never reverses: search consumes the
// core's shares, so the core owns ScanSource and this package adapts it. A
// future per-source field search wants goes on Source, never on ScanSource.
//
// Field for field, with no reinterpretation. Allow is carried across as it
// stands: not called here, not wrapped in anything that could change its
// answer, and not cached. Prefix is this package's own addition for rendering
// a hit path, which is why it is set by the caller rather than asked of the
// core.
func sourceOf(s core.ScanSource) Source {
	return Source{
		Share: uint32(s.Share),
		Root:  s.Root,
		Base:  s.Base,
		Allow: s.Allow,
	}
}

// SourcesOf adapts a core scan-source list into walker inputs.
//
// A source whose root is nil is dropped rather than carried: the core passes a
// broken share's nil root through by contract, and skipping it is the walker
// side of that contract. Every walk in this family goes through here, so the
// skip happens once.
func SourcesOf(sources []core.ScanSource) []Source {
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s.Root == nil {
			continue
		}
		out = append(out, sourceOf(s))
	}
	return out
}
