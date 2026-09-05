// Builds only on Linux, like the walk that consumes a Source, because a source
// names a *vfs.ShareRoot, an openat2 handle present on no other platform.
//go:build linux

package search

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Source is one share to walk.
type Source struct {
	Share uint32
	Root  vfs.Root
	// Base gives the starting point within the share.
	Base vfs.SafePath
	// Prefix is prepended to any reported path, so a hit identifies what the
	// caller asked about instead of a share-relative fragment.
	Prefix string
	// Allow decides whether the caller may see a path. It executes before an
	// entry is scored, so a query matching many invisible entries cannot take
	// measurably longer than one that matches nothing.
	//
	// Nil admits everything, the administrator-scoped form. The walker skips the
	// call entirely rather than treating nil as a call returning true, so the
	// administrator path bears no per-entry closure cost. A non-nil closure is
	// invoked from several goroutines simultaneously and must handle that
	// itself.
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
