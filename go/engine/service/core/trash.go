//go:build linux

package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// trashDir is the per-share control directory holding deleted entries. It is
// a control name, so JoinControl is the only thing that can produce it and
// no user input can name it; every user-facing ReadDir hides it.
const trashDir = ".sctrash"

// trashIDBytes is the entropy in an entry id. Random rather than sequential
// so two concurrent deletes of one name cannot collide on an entry name.
const trashIDBytes = 8

// The layout is one flat level: <share>/.sctrash/{id}-{base64url origin}.
// Flat, with the origin path in the name, because a trash mirroring the
// origin tree needs its own directory management, its own empty-parent
// cleanup and a merge story when one path is deleted twice. This shape needs
// none of that and restores with a single rename.

// TrashEntry is one deleted item as the listing reports it.
type TrashEntry struct {
	ID string

	// Name is the leaf of the origin path, or the raw suffix for a legacy
	// entry whose name carries only a basename.
	Name string

	// OrigPath is the share-relative origin, empty for a legacy entry.
	OrigPath string

	IsDir bool
	Size  uint64

	// DeletedAtNs is when the entry was trashed.
	DeletedAtNs int64
}

// trashMove relocates an entry into the trash instead of removing it. Called
// by Delete when the share opted in and the caller did not ask for
// permanent.
//
// A rename, never a copy, so a trashed delete is atomic and costs nothing
// proportional to the data. It runs inline in the request, and it is the
// default delete path on an opted-in share.
func (c *Core) trashMove(ctx context.Context, r Resolved, st vfs.Stat) error {
	dir, err := c.ensureTrashDir(r)
	if err != nil {
		return err
	}
	id, err := newTrashID()
	if err != nil {
		return err
	}
	entry, err := dir.JoinControl(id + "-" + encodeOrigPath(r.path))
	if err != nil {
		return err
	}
	if err := r.root.Rename(r.path, entry, true); err != nil {
		return mapVFSErr(err)
	}

	// Both sides: the origin's listing and aggregates changed, and so did
	// the trash directory's. Marking only the origin left the trash stale.
	c.markDirty(ctx, r.share, r.path)
	c.markDirty(ctx, r.share, entry)
	// No quota movement: the bytes are still on disk, only relocated.
	// Crediting here and again at purge would credit twice.
	return nil
}

// ensureTrashDir creates the control directory if it is not there, treating
// an existing one as success.
func (c *Core) ensureTrashDir(r Resolved) (vfs.SafePath, error) {
	dir, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return vfs.SafePath{}, err
	}
	if err := r.root.Mkdir(dir); err != nil && !errors.Is(err, vfs.ErrExists) {
		return vfs.SafePath{}, mapVFSErr(err)
	}
	return dir, nil
}

// TrashList reports what is in the share's trash.
//
// Read on the share root, because the grant that decides who may see the
// share at all is the grant that decides who may see its trash. A missing
// trash directory is an empty listing: a share nobody deleted from has none,
// and that is not a fault.
func (c *Core) TrashList(ctx context.Context, r Resolved) ([]TrashEntry, error) {
	if err := r.Require(acl.Read); err != nil {
		return nil, err
	}
	dir, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return nil, err
	}
	entries, err := r.root.ReadDir(dir, vfs.HideReserved)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return nil, nil
		}
		return nil, mapVFSErr(err)
	}

	out := make([]TrashEntry, 0, len(entries))
	for _, e := range entries {
		id, rest, ok := splitTrashName(e.Name)
		if !ok {
			continue
		}
		p, jerr := dir.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		st, serr := r.root.Stat(p)
		if serr != nil {
			continue
		}
		row := TrashEntry{
			ID:          id,
			Name:        trashDisplayName(rest),
			IsDir:       st.Kind.IsDir(),
			Size:        st.Size,
			DeletedAtNs: ctimeOrMtime(st),
		}
		if orig, decoded := decodeOrigPath(rest); decoded {
			row.OrigPath = orig.String()
		}
		out = append(out, row)
	}
	return out, nil
}

// TrashRestore puts one entry back where it was deleted from.
//
// Create rather than Delete or a trash-specific bit: a restore brings an
// entry into the live tree, and that is the capability it should demand.
func (c *Core) TrashRestore(ctx context.Context, r Resolved, id string) (vfs.SafePath, error) {
	if err := r.Require(acl.Create); err != nil {
		return vfs.SafePath{}, err
	}
	if err := c.requireTrash(r); err != nil {
		return vfs.SafePath{}, err
	}
	dir, entry, rest, err := c.findTrashEntry(r, id)
	if err != nil {
		return vfs.SafePath{}, err
	}

	// The origin is resolved again against the share root as it is now.
	// That re-resolution is what makes restore safe: a path that no longer
	// resolves the same way produces a conflict or a parse failure rather
	// than a silent overwrite of whatever sits there today.
	dest, ok := decodeOrigPath(rest)
	if !ok {
		// A legacy entry carries only a basename, so the share root is all
		// the information its name holds.
		dest, err = vfs.ParseSafePath(rest)
		if err != nil {
			return vfs.SafePath{}, errf(ErrNotFound, "a trash entry whose origin cannot be read")
		}
	}

	// The origin's ancestors may themselves have been deleted since, and
	// the entry has to land where it was trashed from.
	if derr := c.ensureDirRecursive(r, dest.Parent()); derr != nil {
		return vfs.SafePath{}, derr
	}
	exists, err := pathExists(r.root, dest)
	if err != nil {
		return vfs.SafePath{}, err
	}
	if exists {
		// Never a silent replace: overwriting would destroy newer data with
		// older data on the path a user thinks of as undo, so the decision
		// goes back to the only party that can make it.
		return vfs.SafePath{}, errf(ErrConflict, "something is already at the restored path")
	}
	if err := r.root.Rename(entry, dest, true); err != nil {
		return vfs.SafePath{}, mapVFSErr(err)
	}

	c.markDirty(ctx, r.share, dest)
	c.markDirty(ctx, r.share, dir)
	return dest, nil
}

// ensureDirRecursive recreates a chain of directories, shallowest first,
// tolerating levels that already exist. ShareRoot.Mkdir is one level at a
// time, hence the walk; losing a race with something else creating an
// ancestor is fine, since the directory existing is all that mattered.
func (c *Core) ensureDirRecursive(r Resolved, dir vfs.SafePath) error {
	cur := vfs.RootPath()
	for _, comp := range dir.Components() {
		next, err := cur.JoinExisting(comp)
		if err != nil {
			return err
		}
		cur = next
		if err := r.root.Mkdir(cur); err != nil && !errors.Is(err, vfs.ErrExists) {
			return mapVFSErr(err)
		}
	}
	return nil
}

// TrashPurge permanently removes trashed entries: one when id names it, all
// of them when id is nil. A missing trash directory is success, since there
// is nothing to purge.
//
// This is where the ledger is credited, not trashMove: purge is where the
// bytes are actually freed.
func (c *Core) TrashPurge(ctx context.Context, r Resolved, id *string) error {
	if err := r.Require(acl.Delete); err != nil {
		return err
	}
	if err := c.requireTrash(r); err != nil {
		return err
	}
	dir, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return err
	}
	entries, err := r.root.ReadDir(dir, vfs.HideReserved)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return nil
		}
		return mapVFSErr(err)
	}

	// Errors are joined, not skipped: one bad entry must not stop the
	// others, and a purge that removed nothing must not answer success. It
	// used to, and the screen said the item was gone while the next listing
	// showed it again.
	var failures error
	for _, e := range entries {
		if id != nil && !strings.HasPrefix(e.Name, *id+"-") {
			continue
		}
		p, jerr := dir.JoinExisting(e.Name)
		if jerr != nil {
			failures = errors.Join(failures, jerr)
			continue
		}
		st, serr := r.root.Stat(p)
		if serr != nil {
			failures = errors.Join(failures, mapVFSErr(serr))
			continue
		}

		var freed uint64
		if st.Kind.IsDir() {
			// Best-effort by design: the rollup refuses a path under a
			// control prefix, and treating that refusal as fatal stopped
			// every directory purge before the delete, leaving the entry on
			// disk while the caller was told it was gone. Losing the ledger
			// credit is worth strictly less than losing the delete.
			if agg, aerr := c.Aggregate(ctx, r.share, p); aerr == nil {
				freed = agg.RSize
			}
			under := Resolved{user: r.user, share: r.share, root: r.root, path: p, perms: r.perms}
			if derr := c.deleteRecursive(ctx, under); derr != nil {
				failures = errors.Join(failures, derr)
				continue
			}
		} else {
			freed = st.Size
			if uerr := r.root.Unlink(p); uerr != nil {
				failures = errors.Join(failures, mapVFSErr(uerr))
				continue
			}
		}
		if freed > 0 {
			c.chargeQuota(ctx, r.user, int64Minus(freed))
		}
	}

	c.markDirty(ctx, r.share, dir)
	return failures
}

// requireTrash refuses restore and purge against a share whose trash is off:
// those name a facility the share does not have. Delete is different, and
// simply takes the permanent path.
func (c *Core) requireTrash(r Resolved) error {
	def, ok := c.Share(r.share)
	if !ok || !def.TrashEnabled {
		return ErrTrashDisabled
	}
	return nil
}

// findTrashEntry locates the entry whose name carries the id prefix.
func (c *Core) findTrashEntry(r Resolved, id string) (dir, entry vfs.SafePath, rest string, err error) {
	dir, err = vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return vfs.SafePath{}, vfs.SafePath{}, "", err
	}
	entries, err := r.root.ReadDir(dir, vfs.HideReserved)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return vfs.SafePath{}, vfs.SafePath{}, "", ErrNotFound
		}
		return vfs.SafePath{}, vfs.SafePath{}, "", mapVFSErr(err)
	}
	for _, e := range entries {
		got, suffix, ok := splitTrashName(e.Name)
		if !ok || got != id {
			continue
		}
		p, jerr := dir.JoinExisting(e.Name)
		if jerr != nil {
			return vfs.SafePath{}, vfs.SafePath{}, "", mapVFSErr(jerr)
		}
		return dir, p, suffix, nil
	}
	return vfs.SafePath{}, vfs.SafePath{}, "", ErrNotFound
}

// encodeOrigPath renders a path into one entry-name component.
//
// Base64url rather than the path's own slash-joined form, because a slash is
// exactly the character that must survive intact and a raw one would be
// reinterpreted as a separator. Raw (unpadded) also keeps '=' out of the
// component.
func encodeOrigPath(p vfs.SafePath) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.String()))
}

// decodeOrigPath reads an entry name's suffix back into a path, reporting
// false for anything that is not one. That false is how a legacy entry is
// recognised: entries written before this encoding existed carry a bare
// basename after the dash.
func decodeOrigPath(encoded string) (vfs.SafePath, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return vfs.SafePath{}, false
	}
	p, err := vfs.ParseSafePath(string(raw))
	if err != nil {
		return vfs.SafePath{}, false
	}
	return p, true
}

// splitTrashName cuts an entry name into its id and its suffix on the first
// dash.
//
// The first dash is always the right one: the id half is lowercase hex and
// can never hold a dash, while base64url's alphabet includes one, so the
// suffix may. A name with no dash was not written by this file.
func splitTrashName(name string) (id, rest string, ok bool) {
	id, rest, ok = strings.Cut(name, "-")
	if !ok || id == "" {
		return "", "", false
	}
	return id, rest, true
}

// trashDisplayName is what a screen shows for an entry: the leaf of the
// decoded origin, or the raw suffix when there is nothing to decode.
func trashDisplayName(rest string) string {
	if p, ok := decodeOrigPath(rest); ok {
		return p.Name()
	}
	return rest
}

// ctimeOrMtime is when the delete happened. The move into the trash is an
// inode change, so the change time is exactly the deletion time; the mtime a
// rename does not touch, which used to list a file edited a year ago and
// deleted a minute ago as deleted a year ago. A filesystem reporting no
// change time falls back to the mtime rather than to nothing.
func ctimeOrMtime(st vfs.Stat) int64 {
	if st.CtimeNs != nil {
		return *st.CtimeNs
	}
	return st.MtimeNs
}

// newTrashID mints the random half of an entry name. A random source failure
// fails the delete: a trash entry is never given a guessable or reused name.
func newTrashID() (string, error) {
	buf := make([]byte, trashIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("minting a trash entry id: %w", err)
	}
	return hexLower(buf), nil
}

// hexLower renders bytes as lowercase hex. It is spelled out here so the id
// alphabet is a guarantee of this file (no dash, ever) rather than a
// property borrowed from a formatting package.
func hexLower(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&0x0f])
	}
	return string(out)
}
