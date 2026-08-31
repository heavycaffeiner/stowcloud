//go:build linux

package core

import (
	"context"
	"errors"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
)

// OnConflict is what a transfer does with a destination that is already
// taken. It is a typed value rather than a string the protocol layer
// compares: an unrecognised spelling used to fall through to "fail", so
// choosing overwrite in the conflict dialogue asked the same conflict again
// forever.
type OnConflict uint8

const (
	// ConflictFail returns ErrConflict, which is what starts the conversation.
	ConflictFail OnConflict = iota
	// ConflictRename keeps both, landing at the next free "name (2).ext".
	ConflictRename
	// ConflictOverwrite overwrites the destination.
	ConflictOverwrite
	// ConflictSkip leaves the destination alone and reports done.
	ConflictSkip
)

// ParseOnConflict maps a wire spelling to its policy.
//
// Case-insensitive after trimming, because the two ends disagreed about case
// once already: the client sent "Overwrite", the exact comparison against
// "overwrite" fell through to the default, and the dialogue looped. The false
// return is load-bearing: a client asking for a policy this build does not
// have is refused by the caller, never silently given a different one.
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
	default:
		return ConflictFail, false
	}
}

// MoveOpts is how a caller asks for a move.
type MoveOpts struct {
	// Overwrite is the two-value legacy form of the policy. Older callers
	// speak it, and when set it wins over OnConflict.
	Overwrite bool

	// OnConflict is the full policy.
	OnConflict OnConflict

	// IfMatch validates the destination. Every token this system mints is
	// weak, so a supplied validator is always refused.
	IfMatch *Token
}

// policy folds the two conflict fields into one, so Move runs exactly one
// switch over the answer.
func (o MoveOpts) policy() OnConflict {
	if o.Overwrite {
		return ConflictOverwrite
	}
	return o.OnConflict
}

// MoveResult reports what the move actually did, which is not always what was
// asked for.
type MoveResult struct {
	// WillCopy is a move that had to become a copy and then a delete.
	WillCopy bool

	// Created is where the entry landed. Under ConflictRename this is the
	// suffixed name, so the caller reports it back instead of echoing its
	// own request.
	Created vfs.SafePath

	// Moved is a plain rename.
	Moved bool

	// Skipped is ConflictSkip leaving a taken destination alone. Distinct
	// from both success and refusal: nothing was written.
	Skipped bool
}

// Move relocates an entry, by rename where it can and by copy-then-delete
// where it must.
func (c *Core) Move(ctx context.Context, from, to Resolved, opt MoveOpts) (MoveResult, error) {
	if err := from.Require(acl.Move); err != nil {
		return MoveResult{}, err
	}
	if err := to.Require(acl.Create); err != nil {
		return MoveResult{}, err
	}
	// A share root is not movable and not a legal destination name.
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

	dest, overwriting, done, err := c.applyConflict(ctx, to, opt.policy(), opt.IfMatch)
	if err != nil {
		return MoveResult{}, err
	}
	if done {
		return MoveResult{Created: dest.path, Skipped: true}, nil
	}
	to = dest

	res := MoveResult{Created: to.path}
	if crossesDevice(from, to, srcSt.Dev) {
		res.WillCopy = true
		// A nil gate: a move answers inline, so there is no job row for
		// anybody to mark cancelled.
		if cerr := c.copyRecursive(ctx, from, to, srcSt, nil); cerr != nil {
			return MoveResult{}, cerr
		}
		// Crediting off: the bytes were charged at the destination copy, and
		// crediting here would count the move as a shrink.
		if derr := c.deleteResolved(ctx, from, srcSt, false); derr != nil {
			// The partial completion is reported, never dropped: the caller
			// is told a duplicate exists.
			return MoveResult{}, errf(ErrCrossShare, "the copy completed but removing the source failed")
		}
	} else {
		// The no-replace flag is on unless an existing entry is being
		// replaced, so a race that fills the name between the check and the
		// rename is a refusal rather than a clobber.
		if rerr := to.root.Rename(from.path, to.path, !overwriting); rerr != nil {
			return MoveResult{}, mapVFSErr(rerr)
		}
		res.Moved = true
	}

	// Both ends: the entry left one listing and joined another.
	c.markDirty(ctx, from.share, from.path)
	c.markDirty(ctx, to.share, to.path)
	// One move row against the source, even on the copy leg, where copyFile
	// has already recorded its own copy rows per file. That split is the
	// existing observable behavior.
	c.record(ctx, from, journal.OpMove)
	return res, nil
}

// applyConflict runs the policy switch that Move and StartCopy share against
// a destination that may already be taken.
//
// It returns the destination to use, re-pathed rather than rebuilt under
// rename so the permission set travels with it; whether an existing entry is
// being replaced; and whether the caller is already finished, which is the
// skip case. A destination that was free returns overwriting false whatever
// the policy says, so the rename below keeps its no-replace flag and a race
// that fills the name is refused rather than clobbered.
func (c *Core) applyConflict(
	ctx context.Context, to Resolved, policy OnConflict, ifMatch *Token,
) (dest Resolved, overwriting, done bool, err error) {
	taken, err := pathExists(to.root, to.path)
	if err != nil {
		return Resolved{}, false, false, err
	}
	if !taken {
		return to, false, false, nil
	}

	switch policy {
	case ConflictSkip:
		return to, false, true, nil
	case ConflictRename:
		free, ferr := c.uniqueSiblingName(to.root, to.path)
		if ferr != nil {
			return Resolved{}, false, false, ferr
		}
		to.path = free
		return to, false, false, nil
	case ConflictOverwrite:
		dstSt, serr := to.root.Stat(to.path)
		if serr != nil {
			return Resolved{}, false, false, mapVFSErr(serr)
		}
		if perr := precondition(ifMatch, dstSt); perr != nil {
			return Resolved{}, false, false, perr
		}
		if dstSt.Kind.IsDir() {
			// An overwrite replaces the destination, and a rename cannot do
			// that to a directory holding anything: the kernel answers
			// ENOTEMPTY, which used to surface as a conflict on every
			// collection move onto an existing collection. A copy has the
			// same rule for a different reason, since copying into an
			// existing directory merges the two and a member only the
			// destination had would survive a replace. Crediting is off
			// because the transfer accounts its own bytes.
			if derr := c.deleteResolved(ctx, to, dstSt, false); derr != nil {
				return Resolved{}, false, false, derr
			}
		}
		return to, true, false, nil
	default:
		// ConflictFail, and anything a caller invented. Refusing an unknown
		// policy is what keeps a bad value from silently becoming a clobber.
		return Resolved{}, false, false, ErrConflict
	}
}

// WouldCopy is the preflight a destination picker runs before allowing a commit.
// A cross-device move amounts to a copy followed by a delete, taking time
// proportional to the data, which merits a warning beforehand.
//
// A source that cannot be stat'd yields false rather than an error. The move
// itself reports whatever is wrong, and a preflight that rejects produces a
// picker that will not open.
func (c *Core) WouldCopy(from, to Resolved) bool {
	st, err := from.root.Stat(from.path)
	if err != nil {
		return false
	}
	return crossesDevice(from, to, st.Dev)
}

// crossesDevice holds the single rule shared by the move and its preflight.
//
// Comparison targets the destination's immediate parent directory rather than
// the destination share root. A volume mounted below that root, such as a RAID
// array under media/, is a separate device with separate numbers, and answering
// from the root would treat a genuine boundary as same-device and attempt a
// rename the kernel rejects with EXDEV.
func crossesDevice(from, to Resolved, srcDev uint64) bool {
	// Two shares are two trees whatever the filesystem says.
	if from.share != to.share {
		return true
	}
	dstDev, err := to.root.DirDev(to.path.Parent())
	if err != nil {
		// A copy across a boundary that was not there is slow; a rename
		// across one that was is a failed move.
		return true
	}
	return srcDev != dstDev
}

// copyRecursive duplicates a subtree.
//
// cancelled is polled once at the top of every call, which makes the poll an
// item-boundary check: once per directory and once per file, never inside a
// file. It may be nil for a copy nobody can cancel, which is the inline
// cross-device leg of Move.
func (c *Core) copyRecursive(
	ctx context.Context, from, to Resolved, srcSt vfs.Stat, cancelled func() bool,
) error {
	if cancelled != nil && cancelled() {
		return errOpCancelled
	}
	if !srcSt.Kind.IsDir() {
		return c.copyFile(ctx, from, to)
	}

	if _, err := c.Mkdir(ctx, to); err != nil && !errors.Is(err, ErrExists) {
		return err
	}
	// HideReserved, so a part file in progress is never copied.
	entries, err := from.root.ReadDir(from.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		srcChild, jerr := from.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		dstChild, jerr := to.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		// The child vanished under the walk; skipping is not fatal.
		childSt, serr := from.root.Stat(srcChild)
		if serr != nil {
			continue
		}
		srcRes := Resolved{user: from.user, share: from.share, root: from.root, path: srcChild, perms: from.perms}
		dstRes := Resolved{user: to.user, share: to.share, root: to.root, path: dstChild, perms: to.perms}
		if cerr := c.copyRecursive(ctx, srcRes, dstRes, childSt, cancelled); cerr != nil {
			return cerr
		}
	}
	return nil
}

// copyFile duplicates one file's content.
//
// Going through WriteDurable means a pre-existing destination is replaced
// atomically; there is no window with neither version present. CopyRange
// inside it is a reflink on btrfs and XFS when aligned, an in-kernel copy
// otherwise.
func (c *Core) copyFile(ctx context.Context, from, to Resolved) error {
	src, err := from.root.OpenRead(from.path, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			// The copy already committed; a close failure is not its answer.
			c.warn("closing a copy source failed", "path", from.path.String(), "error", cerr)
		}
	}()

	st, err := src.Stat()
	if err != nil {
		return mapVFSErr(err)
	}
	opts := vfs.DurableOpts{Mode: to.root.Policy().ModeFile}
	if _, err := to.root.WriteDurable(to.path, opts, func(dst *vfs.File) error {
		_, cerr := vfs.CopyRange(src, 0, dst, 0, st.Size)
		return cerr
	}); err != nil {
		return mapVFSErr(err)
	}

	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, to, journal.OpCopy)
	return nil
}

// RefuseSelfDescendant rejects a transfer whose destination is the source itself
// or lies within it.
//
// Absent this check, copying a directory into its own subtree produces a walk
// that never terminates: every pass copies what the previous pass wrote until
// the disk fills. RFC 4918 specifies 403 for WebDAV and the native surface wants
// the same answer, so the DAV layer calls this for its own preflight.
//
// Comparison proceeds component by component and never over request strings,
// since a string prefix test would make "/a/bc" appear to be a child of
// "/a/b".
func RefuseSelfDescendant(from, to Resolved) error {
	if from.share != to.share {
		return nil
	}
	src := from.path.Components()
	dst := to.path.Components()
	// Fewer components than the source means it cannot be inside it.
	if len(dst) < len(src) {
		return nil
	}
	for i, comp := range src {
		if dst[i] != comp {
			return nil
		}
	}
	// Equal length is the source itself; longer is a descendant.
	return errf(ErrDenied, "the destination is inside the source")
}
