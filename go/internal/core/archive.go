//go:build linux

package core

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Archive is a server-side zip of a subtree. The core enumerates the tree and
// hands each file to a visitor; the zip writer lives in the protocol layer,
// which is what owns the wire format. ACL gates descent, the same rule the
// search walker follows: a subtree the caller cannot read is never entered.

// WalkEntry is one descendant discovered while walking an archive root.
type WalkEntry struct {
	// RelPath is the path relative to the archive root, slash-joined, which is
	// the natural zip entry name.
	RelPath string
	IsDir   bool
	// Readable is false when the name exists but the caller may not read it
	// (or it raced out from under us between the ACL check and the open). The
	// caller records it as skipped; it must not fail the archive.
	Readable bool
	Size     uint64
	MTimeNs  int64
}

// archiveCallback is what ArchiveWalk calls once per entry. For a readable
// file it also receives an open reader held for exactly as long as the
// callback needs it; a large archive never holds more than one file open.
func (c *Core) ArchiveWalk(ctx context.Context, r Resolved, visit func(WalkEntry, *Stream) error) error {
	if err := r.Require(acl.Read); err != nil {
		return err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return mapVFSErr(err)
	}

	base := r.path.Name()
	if !st.Kind.IsDir() {
		entry, stream, serr := c.OpenStream(ctx, r, nil)
		if serr != nil {
			return serr
		}
		return visit(WalkEntry{
			RelPath:  base,
			Readable: true,
			Size:     entry.Size,
			MTimeNs:  entry.MTime,
		}, stream)
	}
	return c.walkArchiveRec(ctx, r, base, visit)
}

// walkArchiveRec is the recursive descent.
func (c *Core) walkArchiveRec(ctx context.Context, r Resolved, rel string, visit func(WalkEntry, *Stream) error) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		// The directory vanished or became unreadable between the parent's ACL
		// check and this step: report nothing further under it rather than
		// failing the whole archive.
		return nil
	}
	for _, e := range entries {
		childPath, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		childRel := e.Name
		if rel != "" {
			childRel = rel + "/" + e.Name
		}
		st, serr := r.root.Stat(childPath)
		if serr != nil {
			continue
		}
		child := Resolved{user: r.user, share: r.share, root: r.root, path: childPath, perms: r.perms}

		if st.Kind.IsDir() {
			if err := visit(WalkEntry{RelPath: childRel, IsDir: true, Readable: true}, nil); err != nil {
				return err
			}
			// Descend only past readable directories, which is the ACL descent
			// rule: an unreadable subtree costs one visit and nothing leaks.
			if c.canRead(child) {
				if err := c.walkArchiveRec(ctx, child, childRel, visit); err != nil {
					return err
				}
			}
			continue
		}

		if !c.canRead(child) {
			if err := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); err != nil {
				return err
			}
			continue
		}
		entry, stream, oerr := c.OpenStream(ctx, child, nil)
		if oerr != nil {
			// Vanished between the stat and the open: report skipped.
			if err := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); err != nil {
				return err
			}
			continue
		}
		werr := visit(WalkEntry{
			RelPath:  childRel,
			Readable: true,
			Size:     entry.Size,
			MTimeNs:  entry.MTime,
		}, stream)
		// The visitor copies what it needs while the stream is open; core owns
		// the descriptor and closes it on the way out, success or not.
		if cerr := stream.Close(); cerr != nil {
			return cerr
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}

// canRead asks the ACL whether this resolved path is readable by its caller.
func (c *Core) canRead(r Resolved) bool {
	return c.acl.Effective(int64(r.user),
		aclVpath(r.share, r.path)).Has(acl.Read)
}

// aclVpath builds the ACL's virtual path from a resolved one.
func aclVpath(share ShareID, p vfs.SafePath) acl.Vpath {
	return acl.Vpath{Share: int64(share), Path: aclPath(p)}
}
