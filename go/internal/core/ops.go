//go:build linux

package core

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The mutation primitives, every one of which is: resolve, check permissions,
// check the precondition, act through the named VFS operation, invalidate,
// then record one journal row. Nothing here parses a path, and nothing here
// touches the raw rename syscall, which internal/vfs owns.

// Mkdir creates one directory.
func (c *Core) Mkdir(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Create); err != nil {
		return Entry{}, err
	}
	if err := requireCreatableLeaf(r.path); err != nil {
		return Entry{}, err
	}
	if err := r.root.Mkdir(r.path); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// CreateFile publishes new content under p. It is the upload finalization
// path and the content-replacement path, both of which go through
// WriteDurable: a truncate-and-write replace is neither atomic nor
// mode-preserving, which is principle 3 broken.
//
// ifMatch is the caller's validator. A weak precondition is refused here
// rather than presented as proof; an explicit unconditional retry is the one
// way past it.
func (c *Core) CreateFile(
	ctx context.Context, r Resolved, mode vfs.DurableOpts, ifMatch *Token, write func(*vfs.File) error,
) (Entry, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Entry{}, err
	}

	st, statErr := r.root.Stat(r.path)
	switch {
	case statErr == nil:
		if err := precondition(ifMatch, st); err != nil {
			return Entry{}, err
		}
	case errors.Is(statErr, vfs.ErrNotFound):
		if ifMatch != nil {
			return Entry{}, &PreconditionError{Current: ""}
		}
		if err := requireCreatableLeaf(r.path); err != nil {
			return Entry{}, err
		}
	default:
		return Entry{}, mapVFSErr(statErr)
	}

	if _, err := r.root.WriteDurable(r.path, mode, write); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// Rename changes the name within the same directory. It goes through
// ShareRoot.Rename, never a raw syscall.
func (c *Core) Rename(ctx context.Context, r Resolved, newName string, ifMatch *Token) (Entry, error) {
	if err := r.Require(acl.Rename); err != nil {
		return Entry{}, err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if err := precondition(ifMatch, st); err != nil {
		return Entry{}, err
	}

	dest, jerr := r.path.Parent().Join(newName)
	if jerr != nil {
		return Entry{}, jerr
	}
	if err := r.root.Rename(r.path, dest, true); err != nil {
		return Entry{}, mapVFSErr(err)
	}

	c.markDirty(ctx, r.share, r.path)
	c.markDirty(ctx, r.share, dest)
	c.record(ctx, r, journal.OpMove)
	destResolved := Resolved{user: r.user, share: r.share, root: r.root, path: dest, perms: r.perms}
	return c.buildEntry(destResolved, newName, dest), nil
}

// WouldCopy reports whether moving from into to would have to become a copy,
// without moving anything.
//
// It is what a destination picker asks before it lets somebody commit: a move
// across a device is a copy and a delete, which takes time proportional to the
// data and is worth warning about first.
//
// A source that cannot be stat'd answers false rather than an error: the move
// itself reports what is wrong with it, and a preflight that refuses is a
// picker that cannot open.
func (c *Core) WouldCopy(from, to Resolved) bool {
	st, err := from.root.Stat(from.path)
	if err != nil {
		return false
	}
	return crossesDevice(from, to, st.Dev)
}

// crossesDevice is the rule the move and its preflight share. Two shares are
// two trees whatever the filesystem says, and one rename cannot cross a device
// even within a share.
//
// The comparison is against the destination's own parent directory, not the
// destination share root. A volume mounted below the root (a RAID array under
// media/) is a different device with different numbers, so answering from the
// root calls a real boundary same-device and attempts a rename the kernel
// refuses with EXDEV.
//
// A destination whose device cannot be read answers true: a copy across a
// boundary that was not there is slow, and a rename across one that was is a
// failed move.
func crossesDevice(from, to Resolved, srcDev uint64) bool {
	if from.share != to.share {
		return true
	}
	dstDev, err := to.root.DirDev(to.path.Parent())
	if err != nil {
		return true
	}
	return dstDev != srcDev
}

// OnConflict is what a transfer does when the destination name is taken.
//
// It is a typed value rather than a string compared inside the protocol layer,
// which is where it used to live: a spelling the comparison did not recognise
// silently became "fail", so choosing overwrite in the conflict dialogue asked
// again for the same conflict forever.
type OnConflict uint8

const (
	// ConflictFail returns ErrConflict, which is what opens the dialogue.
	ConflictFail OnConflict = iota
	// ConflictRename keeps both, giving the copy the next free "name (2).ext".
	ConflictRename
	// ConflictOverwrite replaces the destination.
	ConflictOverwrite
	// ConflictSkip leaves the destination alone and reports the item as done.
	ConflictSkip
)

// ParseOnConflict reads the wire spelling, reporting whether it is one this
// build has.
//
// Case-insensitive because the two ends of this wire disagreed about the case
// once already: the client sent "Overwrite" and an exact comparison against
// "overwrite" read it as the default, so choosing overwrite in the conflict
// dialogue asked again for the same conflict forever. An unrecognised value is
// reported rather than folded into one of the four, because a client asking
// for a policy this build does not have must not silently get a different one.
func ParseOnConflict(s string) (OnConflict, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "fail":
		return ConflictFail, true
	case "rename":
		return ConflictRename, true
	case "overwrite":
		return ConflictOverwrite, true
	case "skip":
		return ConflictSkip, true
	}
	return ConflictFail, false
}

// MoveOpts carries what a move needs beyond the two ends.
type MoveOpts struct {
	// Overwrite replaces the destination. Off means a conflict is returned.
	Overwrite bool
	// OnConflict is the full policy, which Overwrite is the two-value form of.
	// Set it and Overwrite is ignored.
	OnConflict OnConflict
	// IfMatch validates the destination when it exists. Weak is refused.
	IfMatch *Token
}

// policy is the effective conflict policy for a move, folding the older
// two-value Overwrite into the four-value one.
func (o MoveOpts) policy() OnConflict {
	if o.Overwrite {
		return ConflictOverwrite
	}
	return o.OnConflict
}

// MoveResult says what a move did, which is not always exactly a move.
type MoveResult struct {
	// WillCopy reports that a same-name destination across a device or a
	// share boundary had to be copied and deleted rather than renamed, which
	// the UI warns about before the user commits.
	WillCopy bool
	// Created is the path the entry landed at. Under ConflictRename this is
	// the suffixed name, not the one the request asked for, which is why the
	// caller reports it back rather than echoing its own request.
	Created vfs.SafePath
	// Moved reports a plain rename happened.
	Moved bool
	// Skipped reports the destination was taken and ConflictSkip left it
	// alone. Nothing was written, which is a different outcome from both a
	// completed move and a refusal.
	Skipped bool
}

// Move moves from to to within a share, or copies and deletes across shares.
// Across shares it is not atomic, and it says so in the return rather than
// pretending otherwise.
func (c *Core) Move(ctx context.Context, from, to Resolved, opt MoveOpts) (MoveResult, error) {
	if err := from.Require(acl.Move); err != nil {
		return MoveResult{}, err
	}
	if err := to.Require(acl.Create); err != nil {
		return MoveResult{}, err
	}
	if from.path.IsRoot() {
		return MoveResult{}, errf(ErrDenied, "move a share root")
	}
	if to.path.IsRoot() {
		return MoveResult{}, errf(ErrDenied, "move onto a share root")
	}
	if from.share == to.share && from.path.Equal(to.path) {
		return MoveResult{Created: to.path}, nil
	}

	srcSt, err := from.root.Stat(from.path)
	if err != nil {
		return MoveResult{}, mapVFSErr(err)
	}

	policy := opt.policy()
	destExists, derr := pathExists(to.root, to.path)
	if derr != nil {
		return MoveResult{}, derr
	}
	overwriting := false
	if destExists {
		switch policy {
		case ConflictFail:
			return MoveResult{}, ErrConflict
		case ConflictSkip:
			return MoveResult{Created: to.path, Skipped: true}, nil
		case ConflictRename:
			// The next free suffixed name in the same directory, which is what
			// "keep both" means. Re-resolved rather than mutated in place so
			// the permission set travels with it.
			free, ferr := c.uniqueSiblingName(to.root, to.path)
			if ferr != nil {
				return MoveResult{}, ferr
			}
			to.path = free
		case ConflictOverwrite:
			overwriting = true
			dstSt, serr := to.root.Stat(to.path)
			if serr != nil {
				return MoveResult{}, mapVFSErr(serr)
			}
			if err := precondition(opt.IfMatch, dstSt); err != nil {
				return MoveResult{}, err
			}
			// An overwrite replaces the destination, and rename cannot do that
			// when the destination is a directory with anything in it: the
			// kernel answers ENOTEMPTY, which surfaced as a conflict on every
			// collection move onto an existing collection. RFC 4918 9.9.4
			// deletes it first, which is the same thing the specification says
			// a copy does.
			if dstSt.Kind.IsDir() {
				if err := c.deleteResolved(ctx, to, dstSt, false); err != nil {
					return MoveResult{}, err
				}
			}
		}
	}

	willCopy := crossesDevice(from, to, srcSt.Dev)
	res := MoveResult{WillCopy: willCopy}

	if !willCopy {
		if err := to.root.Rename(from.path, to.path, !overwriting); err != nil {
			return MoveResult{}, mapVFSErr(err)
		}
		res.Moved = true
	} else {
		// No cancellation gate: a move answers inline, so there is no job row
		// for anybody to mark while this runs.
		if err := c.copyRecursive(ctx, from, to, srcSt, nil); err != nil {
			return MoveResult{}, err
		}
		if err := c.deleteResolved(ctx, from, srcSt, false); err != nil {
			// The destination is complete and the copy has committed; a
			// failure to remove the source leaves a duplicate, which is
			// reported rather than silently dropped.
			return MoveResult{}, errf(ErrCrossShare, "the copy completed but removing the source failed")
		}
	}

	c.markDirty(ctx, from.share, from.path)
	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, from, journal.OpMove)
	res.Created = to.path
	return res, nil
}

// copyRecursive duplicates a subtree into a destination path. Files go
// through the VFS copy-range helper: a reflink on btrfs and XFS when aligned,
// an in-kernel copy otherwise.
//
// cancelled is polled at every item boundary and may be nil for a copy nobody
// can cancel (the inline cross-device leg of a move, which finishes inside the
// request that asked for it).
func (c *Core) copyRecursive(ctx context.Context, from, to Resolved, srcSt vfs.Stat, cancelled func() bool) error {
	if cancelled != nil && cancelled() {
		return errOpCancelled
	}
	if srcSt.Kind.IsDir() {
		if _, err := c.Mkdir(ctx, to); err != nil {
			if !errors.Is(err, ErrExists) {
				return err
			}
		}
		entries, err := from.root.ReadDir(from.path, vfs.HideReserved)
		if err != nil {
			return mapVFSErr(err)
		}
		for _, e := range entries {
			childFromPath, jerr := from.path.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			childToPath, jerr := to.path.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			cst, serr := from.root.Stat(childFromPath)
			if serr != nil {
				continue
			}
			childFrom := Resolved{user: from.user, share: from.share, root: from.root, path: childFromPath, perms: from.perms}
			childTo := Resolved{user: to.user, share: to.share, root: to.root, path: childToPath, perms: to.perms}
			if err := c.copyRecursive(ctx, childFrom, childTo, cst, cancelled); err != nil {
				return err
			}
		}
		return nil
	}
	return c.copyFile(ctx, from, to)
}

// copyFile copies one file onto the destination. The destination may
// pre-exist, in which case it is replaced atomically through WriteDurable.
func (c *Core) copyFile(ctx context.Context, from, to Resolved) error {
	src, err := from.root.OpenRead(from.path, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			c.logger.Warn("closing a source handle failed", slog.Any("error", cerr))
		}
	}()
	srcSt, err := src.Stat()
	if err != nil {
		return mapVFSErr(err)
	}

	mode := to.root.Policy().ModeFile
	if _, werr := to.root.WriteDurable(to.path, vfs.DurableOpts{Mode: mode}, func(dst *vfs.File) error {
		_, cerr := vfs.CopyRange(src, 0, dst, 0, srcSt.Size)
		return cerr
	}); werr != nil {
		return mapVFSErr(werr)
	}
	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, to, journal.OpCopy)
	return nil
}

// Delete removes one entry. When trash is enabled on the share it moves into
// the trash instead; permanent is the caller spelling it out.
func (c *Core) Delete(ctx context.Context, r Resolved, permanent bool) error {
	if err := r.Require(acl.Delete); err != nil {
		return err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return mapVFSErr(err)
	}

	if e, ok := c.Share(r.share); ok && e.TrashEnabled && !permanent {
		return c.trashMove(ctx, r, st)
	}
	return c.deleteResolved(ctx, r, st, true)
}

// deleteResolved performs a permanent delete, crediting the quota ledger for
// the freed bytes. A directory's size comes from the recursive aggregate, so
// the ledger gets what was actually on disk, read before the delete.
func (c *Core) deleteResolved(ctx context.Context, r Resolved, st vfs.Stat, charge bool) error {
	freed := uint64(0)
	if st.Kind.IsDir() {
		agg, aerr := c.Aggregate(ctx, r.share, r.path)
		if aerr != nil {
			return aerr
		}
		freed = agg.RSize
		if err := c.deleteRecursive(ctx, r); err != nil {
			return err
		}
	} else {
		freed = st.Size
		if err := r.root.Unlink(r.path); err != nil {
			return mapVFSErr(err)
		}
	}
	if charge && freed > 0 {
		c.chargeQuota(ctx, r.user, int64Minus(freed))
	}
	c.markDirty(ctx, r.share, r.path)
	return nil
}

// deleteRecursive walks from the root down, deleting children before parents.
func (c *Core) deleteRecursive(ctx context.Context, r Resolved) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		childPath, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		child := Resolved{user: r.user, share: r.share, root: r.root, path: childPath, perms: r.perms}
		st, serr := r.root.Stat(childPath)
		if serr != nil {
			continue
		}
		if st.Kind.IsDir() {
			if err := c.deleteRecursive(ctx, child); err != nil {
				return err
			}
		} else {
			if err := r.root.Unlink(childPath); err != nil {
				return mapVFSErr(err)
			}
		}
	}
	if err := r.root.Rmdir(r.path); err != nil {
		return mapVFSErr(err)
	}
	return nil
}

// Stat returns the entry for one path. A single named path is the bounded
// case where a stable id is worth minting, which is what a share-link target
// needs.
func (c *Core) Stat(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Read); err != nil {
		return Entry{}, err
	}
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// requireCreatableLeaf applies the creation table to a leaf about to be
// brought into existence. Anything already on the share stays fully usable;
// nothing typed through this server adds a name a Windows or SMB client could
// never open.
func requireCreatableLeaf(p vfs.SafePath) error {
	if p.IsRoot() {
		return nil
	}
	_, err := p.Parent().Join(p.Name())
	return err
}

// uniqueSiblingName picks the next free "name (2).ext" beside a taken path.
//
// It is what "keep both" resolves to, and what a drop link does with a
// colliding upload: one rule, so the suffix a person sees is the same wherever
// this server had to invent a name.
func (c *Core) uniqueSiblingName(root *vfs.ShareRoot, taken vfs.SafePath) (vfs.SafePath, error) {
	dir := taken.Parent()
	name := taken.Name()
	stem, ext := name, ""
	if i := lastDot(name); i > 0 {
		// i > 0 rather than i >= 0: a leading dot is a hidden file's name, not
		// an extension, so ".bashrc" becomes ".bashrc (2)" and not " (2).bashrc".
		stem, ext = name[:i], name[i:]
	}
	for n := 2; n < uniqueNameBound; n++ {
		candidate, jerr := dir.Join(stem + " (" + strconv.Itoa(n) + ")" + ext)
		if jerr != nil {
			continue
		}
		exists, err := pathExists(root, candidate)
		if err != nil {
			return vfs.SafePath{}, err
		}
		if !exists {
			return candidate, nil
		}
	}
	return vfs.SafePath{}, ErrConflict
}

// uniqueNameBound is where the search above gives up. A directory holding this
// many collisions of one name is one where the caller wanted a different
// answer than a longer suffix.
const uniqueNameBound = 10_000

// pathExists stats a path and reports whether it is there, folding the
// missing answer to false. It is the one way a mutation asks "is the
// destination occupied" without converting a refusal.
func pathExists(root *vfs.ShareRoot, p vfs.SafePath) (bool, error) {
	_, err := root.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, vfs.ErrNotFound):
		return false, nil
	default:
		return false, mapVFSErr(err)
	}
}

// Token is a caller-supplied validator: the current ETag the client last saw,
// sent to prove nothing changed in between.
type Token string

// precondition is the F11 gate. RFC 9110 requires strong comparison for an
// If-Match validator, and a file ETag is always weak, so a supplied validator
// can never match and is always refused, carrying the current token so a
// conflict screen can show it. Requiring an explicit unconditional retry is
// the only way past.
func precondition(ifMatch *Token, st vfs.Stat) error {
	if ifMatch == nil {
		return nil
	}
	cur, _ := FileETag(st)
	return &PreconditionError{Current: cur}
}

// record writes one journal row after a successful mutation. It happens after
// the write already succeeded, so a failure here is logged and dropped and
// nothing may treat a missing row as evidence.
func (c *Core) record(ctx context.Context, r Resolved, op journal.Op) {
	if c.journal == nil {
		return
	}
	acc, nerr := num.Narrow[uint32](int64(r.user))
	if nerr != nil {
		c.warn("a user id does not fit the journal's account column; skipping the row",
			"user", int64(r.user))
		return
	}
	ev := journal.Event{
		Account: acc,
		Share:   r.share,
		Path:    r.path.Share(),
		Op:      op,
	}
	if err := c.journal.Record(ctx, ev); err != nil {
		c.warn("recording a write to the journal failed; the write itself has committed",
			"error", err)
	}
}
