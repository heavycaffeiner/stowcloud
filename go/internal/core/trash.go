//go:build linux

package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Per-share trash: <share>/.sctrash/{id}-{encoded origin path}, one flat
// level. The .sctrash directory is a control directory, so it never appears in
// a listing and is never indexed. Trash is off by default; the toggle is
// visible in the share definition, and until it is on a delete is a delete
// and the UI says so before the destructive action.
//
// The entry name carries the whole origin path, not just the basename, because
// a flat store has nowhere else to keep the parent: restoring a file trashed
// from docs/2024/report.pdf has to land there, not at the share root.
const trashDir = ".sctrash"

// TrashEntry is one entry of the trash listing.
type TrashEntry struct {
	ID string
	// Name is the display name: the leaf of the origin path, or the raw
	// suffix for a legacy entry that predates the encoding.
	Name string
	// OrigPath is the share-relative path the entry was deleted from, empty
	// when the entry predates that encoding and only its basename was kept.
	OrigPath string
	IsDir    bool
	Size     uint64
	// DeletedAtNs is the inode change time the move into the trash set. It
	// used to carry the file's mtime, which a move does not touch, so a file
	// last edited a year ago and deleted a minute ago was listed as "Deleted"
	// a year ago.
	DeletedAtNs int64
}

// trashMove moves one entry into the trash. The id is random so two
// concurrent deletes of the same name cannot collide on the entry name.
func (c *Core) trashMove(ctx context.Context, r Resolved, st vfs.Stat) error {
	trash, err := c.ensureTrashDir(ctx, r)
	if err != nil {
		return err
	}
	var b [8]byte
	if _, rerr := rand.Read(b[:]); rerr != nil {
		return fmt.Errorf("naming a trash entry: %w", rerr)
	}
	id := hexLower(b[:])
	encoded := encodeOrigPath(r.path)
	entryName, err := trash.Join(id + "-" + encoded)
	if err != nil {
		return err
	}
	if err := r.root.Rename(r.path, entryName, true); err != nil {
		return mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, r.path)
	c.markDirty(ctx, r.share, trash)
	return nil
}

// ensureTrashDir returns the share's trash directory, creating it first.
func (c *Core) ensureTrashDir(ctx context.Context, r Resolved) (vfs.SafePath, error) {
	trash, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return vfs.SafePath{}, err
	}
	if err := r.root.Mkdir(trash); err != nil {
		if !errors.Is(err, vfs.ErrExists) {
			return vfs.SafePath{}, mapVFSErr(err)
		}
	}
	return trash, nil
}

// encodeOrigPath folds a share-relative path into a single filesystem-safe
// component. Base64url is used rather than the path's own slash-joined form
// because a slash inside a single component would be reinterpreted as a
// separator, exactly the character that must survive intact.
func encodeOrigPath(p vfs.SafePath) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.String()))
}

// decodeOrigPath is the inverse, and reports false on anything that does not
// decode to a valid safe path. A pre-encoding trash entry whose suffix is a
// literal basename is exactly that, and the caller treats it as the legacy
// shape: restore to the share root, which is all the information carried.
func decodeOrigPath(encoded string) (vfs.SafePath, bool) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return vfs.SafePath{}, false
	}
	p, err := vfs.ParseSafePath(string(b))
	if err != nil {
		return vfs.SafePath{}, false
	}
	return p, true
}

// splitTrashName separates the id from the encoded origin path. The id is
// lowercase hex and never contains a dash, so splitting on the first dash
// always separates the two even though the encoded half may itself contain
// dashes.
func splitTrashName(name string) (id, rest string, ok bool) {
	id, rest, ok = strings.Cut(name, "-")
	return
}

// trashDisplayName is the leaf of the decoded origin path for the listing,
// or the raw suffix for a legacy entry.
func trashDisplayName(rest string) string {
	if p, ok := decodeOrigPath(rest); ok {
		return p.Name()
	}
	return rest
}

// TrashList returns the share's trash. It requires READ on the share root,
// which is the grant that decides who may see the share at all.
func (c *Core) TrashList(ctx context.Context, r Resolved) ([]TrashEntry, error) {
	if err := r.Require(acl.Read); err != nil {
		return nil, err
	}
	trash, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return nil, err
	}
	entries, err := r.root.ReadDir(trash, vfs.HideReserved)
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
		entryPath, jerr := trash.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		st, serr := r.root.Stat(entryPath)
		if serr != nil {
			continue
		}
		te := TrashEntry{
			ID:          id,
			Name:        trashDisplayName(rest),
			IsDir:       st.Kind.IsDir(),
			Size:        st.Size,
			DeletedAtNs: ctimeOrMtime(st),
		}
		if p, ok := decodeOrigPath(rest); ok {
			te.OrigPath = p.String()
		}
		out = append(out, te)
	}
	return out, nil
}

// ctimeOrMtime is the deletion time: the entry moved into the trash is an
// inode change, which is what records when that happened. A filesystem with no
// change time falls back to the file's own mtime rather than showing nothing.
func ctimeOrMtime(st vfs.Stat) int64 {
	if st.CtimeNs != nil {
		return *st.CtimeNs
	}
	return st.MtimeNs
}

// TrashRestore restores one entry to the path it was trashed from, recreating
// the ancestor chain if the original directories were themselves deleted. A
// conflict is produced rather than an overwrite: something is at the origin
// path now, and the caller has to act on that.
//
// The origin path is resolved again at restore time, which is the rule that
// makes restore safe: a path that no longer resolves the same way produces a
// conflict rather than a silent overwrite.
func (c *Core) TrashRestore(ctx context.Context, r Resolved, id string) (vfs.SafePath, error) {
	if err := r.Require(acl.Create); err != nil {
		return vfs.SafePath{}, err
	}
	trash, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return vfs.SafePath{}, err
	}
	entries, err := r.root.ReadDir(trash, vfs.HideReserved)
	if err != nil {
		return vfs.SafePath{}, mapVFSErr(err)
	}
	var entryName string
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name, id+"-") {
			entryName = e.Name
			found = true
			break
		}
	}
	if !found {
		return vfs.SafePath{}, ErrNotFound
	}
	_, rest, _ := splitTrashName(entryName)

	var dest vfs.SafePath
	if p, ok := decodeOrigPath(rest); ok {
		dest = p
	} else {
		// Legacy entry: the suffix is a bare basename and was always restored
		// to the share root. That is the documented old behavior, applied only
		// where the new information does not exist.
		p, perr := vfs.ParseSafePath(rest)
		if perr != nil {
			return vfs.SafePath{}, ErrNotFound
		}
		dest = p
	}

	if err := ensureDirRecursive(ctx, r, dest.Parent()); err != nil {
		return vfs.SafePath{}, err
	}
	if p, err := pathExists(r.root, dest); err != nil {
		return vfs.SafePath{}, err
	} else if p {
		return vfs.SafePath{}, ErrConflict
	}

	trashPath, jerr := trash.JoinExisting(entryName)
	if jerr != nil {
		return vfs.SafePath{}, jerr
	}
	if err := r.root.Rename(trashPath, dest, true); err != nil {
		return vfs.SafePath{}, mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, dest)
	c.markDirty(ctx, r.share, trash)
	return dest, nil
}

// ensureDirRecursive creates every ancestor of dir that does not exist,
// shallowest first. ShareRoot.Mkdir is one level at a time, so a directory
// that was itself deleted has to be rebuilt for the restore to land where it
// was trashed from.
func ensureDirRecursive(ctx context.Context, r Resolved, dir vfs.SafePath) error {
	cur := vfs.RootPath()
	for _, comp := range dir.Components() {
		var jerr error
		cur, jerr = cur.JoinExisting(comp)
		if jerr != nil {
			return jerr
		}
		if p, err := pathExists(r.root, cur); err != nil {
			return err
		} else if p {
			continue
		}
		if err := r.root.Mkdir(cur); err != nil {
			// Lost a race with something else creating the same ancestor,
			// which is fine: the directory existing is all that mattered.
			if !errors.Is(err, vfs.ErrExists) {
				return mapVFSErr(err)
			}
		}
	}
	return nil
}

// TrashPurge deletes trash entries, one by name or all of them. It is where
// trashed bytes are actually freed, so the quota ledger is credited here and
// not in trashMove, which only relocated them.
func (c *Core) TrashPurge(ctx context.Context, r Resolved, id *string) error {
	if err := r.Require(acl.Delete); err != nil {
		return err
	}
	trash, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return err
	}
	entries, err := r.root.ReadDir(trash, vfs.HideReserved)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return nil
		}
		return mapVFSErr(err)
	}
	// A failure to remove one entry is reported rather than skipped. Every
	// branch below used to `continue`, so a purge that deleted nothing still
	// answered success: the screen said the item was gone and the next listing
	// showed it again.
	var failed error
	for _, e := range entries {
		if id != nil && !strings.HasPrefix(e.Name, *id+"-") {
			continue
		}
		entryPath, jerr := trash.JoinExisting(e.Name)
		if jerr != nil {
			failed = errors.Join(failed, jerr)
			continue
		}
		st, serr := r.root.Stat(entryPath)
		if serr != nil {
			failed = errors.Join(failed, mapVFSErr(serr))
			continue
		}
		var freed uint64
		if st.Kind.IsDir() {
			// What the quota is credited, and nothing else. The rollup refuses
			// a path under the trash directory because that name is a control
			// prefix, which made every directory purge stop here: the entry
			// stayed on disk and the caller was told it was deleted. Losing the
			// credit is worth strictly less than losing the delete, so a
			// refusal here costs the ledger and not the operation.
			if agg, aerr := c.Aggregate(ctx, r.share, entryPath); aerr == nil {
				freed = agg.RSize
			}
			sub := Resolved{user: r.user, share: r.share, root: r.root, path: entryPath, perms: r.perms}
			if err := c.deleteRecursive(ctx, sub); err != nil {
				failed = errors.Join(failed, err)
				continue
			}
		} else {
			freed = st.Size
			if err := r.root.Unlink(entryPath); err != nil {
				failed = errors.Join(failed, mapVFSErr(err))
				continue
			}
		}
		if freed > 0 {
			c.chargeQuota(ctx, r.user, int64Minus(freed))
		}
	}
	c.markDirty(ctx, r.share, trash)
	return failed
}

func hexLower(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, hex[c>>4], hex[c&0xf])
	}
	return string(out)
}

// int64Minus negates a size for the ledger, saturating at the most negative
// value that fits: a freed size cannot exceed the signed range, and a size
// that would is clamped rather than wrapped.
func int64Minus(v uint64) int64 {
	const max = ^uint64(0) >> 1
	if v > max {
		return -int64(max)
	}
	return -int64(v) //nolint:gosec // v was clamped to the signed range above.
}
