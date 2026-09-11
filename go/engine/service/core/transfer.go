//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"
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

// PartialTransferError reports a transfer whose destination state could not
// be restored automatically. Backup names are server-owned paths retained for
// recovery and must not be discarded by a generic cleanup pass.
type PartialTransferError struct {
	Destination vfs.SafePath
	Backup      vfs.SafePath
	Cause       error
	Rollback    error
}

func (e *PartialTransferError) Error() string {
	return fmt.Sprintf(
		"transfer destination %q requires recovery from backup %q; cause: %v; rollback: %v",
		e.Destination.String(), e.Backup.String(), e.Cause, e.Rollback,
	)
}

func (e *PartialTransferError) Unwrap() []error {
	out := make([]error, 0, 2)
	if e.Cause != nil {
		out = append(out, e.Cause)
	}
	if e.Rollback != nil {
		out = append(out, e.Rollback)
	}
	return out
}

func restoreTransferBackup(
	root vfs.Root, dest, backup vfs.SafePath, cause error,
) error {
	if restoreErr := root.Rename(backup, dest, true); restoreErr != nil {
		return &PartialTransferError{
			Destination: dest,
			Backup:      backup,
			Cause:       cause,
			Rollback:    mapVFSErr(restoreErr),
		}
	}
	return cause
}

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
	if err := RefuseSelfDescendant(from, to); err != nil {
		return MoveResult{}, err
	}

	if overwriting {
		dstSt, serr := to.root.Stat(to.path)
		if serr != nil {
			return MoveResult{}, mapVFSErr(serr)
		}
		if dstSt.Kind.IsDir() {
			if err := c.validateDeleteTree(to, acl.Delete); err != nil {
				return MoveResult{}, err
			}
		}
	}

	res := MoveResult{Created: to.path}
	if crossesDevice(from, to, srcSt.Dev) {
		res.WillCopy = true
		if srcSt.Kind.IsDir() {
			// A cross-device directory move removes the source under Move,
			// so every descendant must be authorized before staging starts.
			if err := c.validateDeleteTree(from, acl.Move); err != nil {
				return MoveResult{}, err
			}
		}
		// Move already authorizes removing the source. Requiring Delete here
		// would make the same operation depend on the storage-device boundary.
		if cerr := c.copyTreeStaged(ctx, from, to, srcSt, acl.Read, nil, overwriting); cerr != nil {
			return MoveResult{}, cerr
		}
		if derr := c.deleteResolved(ctx, from, srcSt, true, acl.Move); derr != nil {
			// The partial completion is reported, never dropped: the caller
			// is told a duplicate exists.
			return MoveResult{}, errf(ErrCrossShare, "the copy completed but removing the source failed")
		}
	} else {
		// The no-replace flag is on unless an existing entry is being
		// replaced, so a race that fills the name between the check and the
		// rename is a refusal rather than a clobber.
		if overwriting {
			removedBytes, rerr := c.replaceRename(to.root, from.path, to.path)
			if rerr != nil {
				return MoveResult{}, rerr
			}
			if removedBytes > 0 {
				c.chargeQuota(ctx, to.user, int64Minus(removedBytes))
			}
		} else if rerr := to.root.Rename(from.path, to.path, true); rerr != nil {
			return MoveResult{}, mapVFSErr(rerr)
		}
		res.Moved = true
	}

	// Both ends: the entry left one listing and joined another.
	c.markDirty(ctx, from.share, from.path)
	c.markDirty(ctx, to.share, to.path)
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
		// Conflict selection is a read-only decision. In particular, never
		// delete an existing directory here: validation and the staged
		// publisher must retain it until a complete replacement is ready.
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

func transferContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// validateDeleteTree performs the authority walk needed before replacing a
// destination or removing a cross-device source. It does not mutate anything.
func (c *Core) validateDeleteTree(r Resolved, authority acl.Perms) error {
	current, err := c.ResolveUnder(r, r.path, authority)
	if err != nil {
		return err
	}
	st, err := current.root.Stat(current.path)
	if err != nil {
		return mapVFSErr(err)
	}
	if !st.Kind.IsDir() {
		return nil
	}
	entries, err := current.root.ReadDir(current.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		child, jerr := current.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		if _, serr := current.root.Stat(child); serr != nil {
			if errors.Is(mapVFSErr(serr), ErrNotFound) {
				continue
			}
			return mapVFSErr(serr)
		}
		childResolved, derr := c.ResolveUnder(current, child, authority)
		if derr != nil {
			return derr
		}
		if derr := c.validateDeleteTree(childResolved, authority); derr != nil {
			return derr
		}
	}
	return nil
}

// validateCopyTree checks every source descendant and every destination name
// before a copy starts. The destination walk is against the final path, not a
// temporary staging path, so a grant cannot be bypassed by copying into a
// control sibling and renaming later.
func (c *Core) validateCopyTree(
	ctx context.Context,
	from, to Resolved,
	srcSt vfs.Stat,
	sourceFileNeed acl.Perms,
	overwriting bool,
) (uint64, error) {
	if err := transferContextErr(ctx); err != nil {
		return 0, err
	}
	sourceNeed := sourceFileNeed
	if srcSt.Kind.IsDir() {
		sourceNeed = acl.Read
	}
	sourceRoot, err := c.ResolveUnder(from, from.path, sourceNeed)
	if err != nil {
		return 0, err
	}
	destinationRoot, err := c.ResolveUnder(to, to.path, acl.Write|acl.Create)
	if err != nil {
		return 0, err
	}
	taken, err := pathExists(destinationRoot.root, destinationRoot.path)
	if err != nil {
		return 0, err
	}
	if taken {
		if !overwriting {
			return 0, ErrExists
		}
		dstSt, serr := destinationRoot.root.Stat(destinationRoot.path)
		if serr != nil {
			return 0, mapVFSErr(serr)
		}
		if dstSt.Kind.IsDir() {
			if err := c.validateDeleteTree(destinationRoot, acl.Delete); err != nil {
				return 0, err
			}
		}
	}
	return c.validateCopyNode(
		ctx, sourceRoot, destinationRoot, srcSt, sourceFileNeed,
	)
}

func (c *Core) validateCopyNode(
	ctx context.Context,
	source, destination Resolved,
	st vfs.Stat,
	sourceFileNeed acl.Perms,
) (uint64, error) {
	if err := transferContextErr(ctx); err != nil {
		return 0, err
	}
	if !st.Kind.IsDir() {
		if err := source.Require(sourceFileNeed); err != nil {
			return 0, err
		}
		if err := destination.Require(acl.Write | acl.Create); err != nil {
			return 0, err
		}
		return st.Size, nil
	}
	if err := source.Require(acl.Read); err != nil {
		return 0, err
	}
	if err := destination.Require(acl.Write | acl.Create); err != nil {
		return 0, err
	}
	entries, err := source.root.ReadDir(source.path, vfs.HideReserved)
	if err != nil {
		return 0, mapVFSErr(err)
	}
	var total uint64
	for _, e := range entries {
		if err := transferContextErr(ctx); err != nil {
			return 0, err
		}
		srcChild, jerr := source.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		childSt, serr := source.root.Stat(srcChild)
		if serr != nil {
			if errors.Is(mapVFSErr(serr), ErrNotFound) {
				continue
			}
			return 0, mapVFSErr(serr)
		}
		dstChild, jerr := destination.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		need := sourceFileNeed
		destinationNeed := acl.Write | acl.Create
		if childSt.Kind.IsDir() {
			need = acl.Read
			destinationNeed = acl.Create
		}
		srcResolved, serr := c.ResolveUnder(source, srcChild, need)
		if serr != nil {
			return 0, serr
		}
		dstResolved, derr := c.ResolveUnder(destination, dstChild, destinationNeed)
		if derr != nil {
			return 0, derr
		}
		size, nerr := c.validateCopyNode(
			ctx, srcResolved, dstResolved, childSt, sourceFileNeed,
		)
		if nerr != nil {
			return 0, nerr
		}
		if ^uint64(0)-total < size {
			total = ^uint64(0)
		} else {
			total += size
		}
	}
	return total, nil
}

// copyTreeStaged writes an entire copy into a hidden sibling, then publishes
// that completed tree in one rename sequence. Existing destination content is
// moved to a backup only after the staged tree and the second authority walk
// have succeeded.
func (c *Core) copyTreeStaged(
	ctx context.Context,
	from, to Resolved,
	srcSt vfs.Stat,
	sourceFileNeed acl.Perms,
	cancelled func() bool,
	overwriting bool,
) error {
	if err := transferContextErr(ctx); err != nil {
		return err
	}
	if cancelled != nil && cancelled() {
		return errOpCancelled
	}
	srcBytes, err := c.validateCopyTree(ctx, from, to, srcSt, sourceFileNeed, overwriting)
	if err != nil {
		return err
	}

	oldBytes := uint64(0)
	taken, err := pathExists(to.root, to.path)
	if err != nil {
		return err
	}
	if taken {
		if !overwriting {
			return ErrExists
		}
		oldBytes, err = treeBytes(to.root, to.path, vfs.HideReserved)
		if err != nil {
			return err
		}
	}
	delta := deltaOf(srcBytes, oldBytes)
	var hold quotaReservation
	if delta > 0 {
		hold, err = c.reserveQuota(ctx, to.user, uint64(delta))
		if err != nil {
			return err
		}
	}

	stage, err := c.newControlSibling(to.root, to.path.Parent(), ".scmeta-copy-")
	if err != nil {
		c.releaseQuota(ctx, to.user, &hold)
		return err
	}
	cleanupStage := func() {
		if cerr := removeTree(to.root, stage, vfs.IncludeReserved); cerr != nil &&
			!errors.Is(cerr, ErrNotFound) {
			c.warn("cleaning a failed transfer stage failed",
				"path", stage.String(), "error", cerr)
		}
	}

	sourceRoot, err := c.ResolveUnder(from, from.path, func() acl.Perms {
		if srcSt.Kind.IsDir() {
			return acl.Read
		}
		return sourceFileNeed
	}())
	if err != nil {
		c.releaseQuota(ctx, to.user, &hold)
		return err
	}
	destinationRoot, err := c.ResolveUnder(to, to.path, acl.Write|acl.Create)
	if err != nil {
		c.releaseQuota(ctx, to.user, &hold)
		return err
	}
	stageRoot := Resolved{
		user: to.user, share: to.share, root: to.root, path: stage, perms: to.perms,
	}
	if srcSt.Kind.IsDir() {
		if merr := to.root.Mkdir(stage); merr != nil {
			c.releaseQuota(ctx, to.user, &hold)
			return mapVFSErr(merr)
		}
	} else if cerr := c.copyFileRaw(ctx, sourceRoot, stageRoot); cerr != nil {
		cleanupStage()
		c.releaseQuota(ctx, to.user, &hold)
		return cerr
	}
	if srcSt.Kind.IsDir() {
		if serr := c.copyIntoStage(
			ctx, sourceRoot, destinationRoot, stageRoot, srcSt, sourceFileNeed, cancelled,
		); serr != nil {
			cleanupStage()
			c.releaseQuota(ctx, to.user, &hold)
			return serr
		}
	}

	stagedBytes, err := treeBytes(to.root, stage, vfs.HideReserved)
	if err != nil {
		cleanupStage()
		c.releaseQuota(ctx, to.user, &hold)
		return err
	}
	if _, verr := c.validateCopyTree(ctx, from, to, srcSt, sourceFileNeed, overwriting); verr != nil {
		cleanupStage()
		c.releaseQuota(ctx, to.user, &hold)
		return verr
	}

	removedBytes, err := c.publishStage(
		to.root, stage, to.path, overwriting,
		func(currentOld uint64) error {
			finalDelta := deltaOf(stagedBytes, currentOld)
			targetHold := uint64(0)
			if finalDelta > 0 {
				targetHold = uint64(finalDelta)
			}
			return c.resizeQuota(ctx, to.user, &hold, targetHold)
		},
	)
	if err != nil {
		cleanupStage()
		c.releaseQuota(ctx, to.user, &hold)
		return err
	}
	c.settleQuota(ctx, to.user, deltaOf(stagedBytes, removedBytes), &hold)
	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, to, journal.OpCopy)
	return nil
}

func (c *Core) copyIntoStage(
	ctx context.Context,
	source, destination, stage Resolved,
	st vfs.Stat,
	sourceFileNeed acl.Perms,
	cancelled func() bool,
) error {
	if err := transferContextErr(ctx); err != nil {
		return err
	}
	if cancelled != nil && cancelled() {
		return errOpCancelled
	}
	if !st.Kind.IsDir() {
		return c.copyFileRaw(ctx, source, stage)
	}
	entries, err := source.root.ReadDir(source.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		if err := transferContextErr(ctx); err != nil {
			return err
		}
		if cancelled != nil && cancelled() {
			return errOpCancelled
		}
		srcChild, jerr := source.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		childSt, serr := source.root.Stat(srcChild)
		if serr != nil {
			if errors.Is(mapVFSErr(serr), ErrNotFound) {
				continue
			}
			return mapVFSErr(serr)
		}
		dstChild, jerr := destination.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		stageChild, jerr := stage.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		need := sourceFileNeed
		destinationNeed := acl.Write | acl.Create
		if childSt.Kind.IsDir() {
			need = acl.Read
			destinationNeed = acl.Create
		}
		srcResolved, serr := c.ResolveUnder(source, srcChild, need)
		if serr != nil {
			return serr
		}
		dstResolved, derr := c.ResolveUnder(destination, dstChild, destinationNeed)
		if derr != nil {
			return derr
		}
		stageResolved := Resolved{
			user: stage.user, share: stage.share, root: stage.root,
			path: stageChild, perms: stage.perms,
		}
		if childSt.Kind.IsDir() {
			if merr := stage.root.Mkdir(stageChild); merr != nil {
				return mapVFSErr(merr)
			}
		}
		if err := c.copyIntoStage(
			ctx, srcResolved, dstResolved, stageResolved,
			childSt, sourceFileNeed, cancelled,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) newControlSibling(
	root vfs.Root, parent vfs.SafePath, prefix string,
) (vfs.SafePath, error) {
	for range 8 {
		id, err := newTrashID()
		if err != nil {
			return vfs.SafePath{}, err
		}
		p, err := parent.JoinControl(prefix + id)
		if err != nil {
			return vfs.SafePath{}, err
		}
		taken, err := pathExists(root, p)
		if err != nil {
			return vfs.SafePath{}, err
		}
		if !taken {
			return p, nil
		}
	}
	return vfs.SafePath{}, ErrExists
}

// removeTree cleans hidden transfer stages and backups. Its caller chooses
// whether this operation owns reserved descendants or must preserve them.
func removeTree(root vfs.Root, p vfs.SafePath, policy vfs.ReservedPolicy) error {
	st, err := root.Stat(p)
	if err != nil {
		if errors.Is(mapVFSErr(err), ErrNotFound) {
			return nil
		}
		return mapVFSErr(err)
	}
	if st.Kind.IsDir() {
		entries, err := root.ReadDir(p, policy)
		if err != nil {
			return mapVFSErr(err)
		}
		for _, e := range entries {
			child, jerr := joinTreeChild(p, e.Name)
			if jerr != nil {
				continue
			}
			if err := removeTree(root, child, policy); err != nil {
				return err
			}
		}
		return mapVFSErr(root.Rmdir(p))
	}
	return mapVFSErr(root.Unlink(p))
}

// treeContainsReserved detects control data before cleanup touches any visible
// child. Once a destination has been moved under a hidden backup name, no new
// path-based upload can enter it, so this check remains valid through removal.
func treeContainsReserved(root vfs.Root, p vfs.SafePath) (bool, error) {
	st, err := root.Stat(p)
	if err != nil {
		return false, mapVFSErr(err)
	}
	if !st.Kind.IsDir() {
		return false, nil
	}
	entries, err := root.ReadDir(p, vfs.IncludeReserved)
	if err != nil {
		return false, mapVFSErr(err)
	}
	for _, entry := range entries {
		if vfs.IsReservedName(entry.Name) {
			return true, nil
		}
		child, jerr := joinTreeChild(p, entry.Name)
		if jerr != nil {
			return false, mapVFSErr(jerr)
		}
		found, rerr := treeContainsReserved(root, child)
		if rerr != nil {
			return false, rerr
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// publishStage performs the final atomic replacement. The destination remains
// untouched while the stage is built and checked; if preparation or the second
// rename fails, the backup is restored before the error reaches the caller.
func (c *Core) publishStage(
	root vfs.Root,
	stage, dest vfs.SafePath,
	overwriting bool,
	prepare func(removedBytes uint64) error,
) (uint64, error) {
	taken, err := pathExists(root, dest)
	if err != nil {
		return 0, err
	}
	if taken && !overwriting {
		return 0, ErrExists
	}
	var backup vfs.SafePath
	removedBytes := uint64(0)
	if taken {
		backup, err = c.newControlSibling(root, dest.Parent(), ".scmeta-replace-")
		if err != nil {
			return 0, err
		}
		if rerr := root.Rename(dest, backup, true); rerr != nil {
			return 0, mapVFSErr(rerr)
		}
		hasReserved, rerr := treeContainsReserved(root, backup)
		if rerr != nil || hasReserved {
			if hasReserved {
				rerr = errf(ErrConflict, "the destination contains active control data")
			}
			return 0, restoreTransferBackup(root, dest, backup, rerr)
		}
		removedBytes, err = treeBytes(root, backup, vfs.HideReserved)
		if err != nil {
			return 0, restoreTransferBackup(root, dest, backup, err)
		}
	}
	if err := prepare(removedBytes); err != nil {
		if backup.String() != "" {
			return 0, restoreTransferBackup(root, dest, backup, err)
		}
		return 0, err
	}
	if err := root.Rename(stage, dest, true); err != nil {
		if backup.String() != "" {
			if rerr := root.Rename(backup, dest, true); rerr != nil {
				return 0, &PartialTransferError{
					Destination: dest, Backup: backup,
					Cause: mapVFSErr(err), Rollback: mapVFSErr(rerr),
				}
			}
		}
		return 0, mapVFSErr(err)
	}
	if backup.String() == "" {
		return 0, nil
	}
	// Reserved children may belong to an active upload. Leaving one makes the
	// backup non-empty and rolls the replacement back instead of deleting it.
	if err := removeTree(root, backup, vfs.HideReserved); err != nil {
		rerr := c.rollbackPublished(root, dest, backup)
		if rerr != nil {
			return 0, &PartialTransferError{
				Destination: dest, Backup: backup,
				Cause: mapVFSErr(err), Rollback: rerr,
			}
		}
		return 0, fmt.Errorf(
			"transfer replacement cleanup failed and was rolled back: %w", err,
		)
	}
	return removedBytes, nil
}

// rollbackPublished restores the old destination after the new tree was
// published but removing the backup failed. The new tree is quarantined first,
// so restoration never overwrites either version.
func (c *Core) rollbackPublished(
	root vfs.Root, dest, backup vfs.SafePath,
) error {
	quarantine, err := c.newControlSibling(root, dest.Parent(), ".scmeta-failed-")
	if err != nil {
		return err
	}
	if err := root.Rename(dest, quarantine, true); err != nil {
		return mapVFSErr(err)
	}
	if err := root.Rename(backup, dest, true); err != nil {
		restoreErr := root.Rename(quarantine, dest, true)
		return errors.Join(mapVFSErr(err), mapVFSErr(restoreErr))
	}
	return removeTree(root, quarantine, vfs.HideReserved)
}

// rollbackMoved restores both ends of a same-device move after replacement
// cleanup failed. The original source is not considered gone until this
// routine has put the new tree back there.
func (c *Core) rollbackMoved(
	root vfs.Root, source, dest, backup vfs.SafePath,
) error {
	quarantine, err := c.newControlSibling(root, source.Parent(), ".scmeta-failed-")
	if err != nil {
		return err
	}
	if err := root.Rename(dest, quarantine, true); err != nil {
		return mapVFSErr(err)
	}
	if err := root.Rename(backup, dest, true); err != nil {
		restoreErr := root.Rename(quarantine, dest, true)
		return errors.Join(mapVFSErr(err), mapVFSErr(restoreErr))
	}
	if err := root.Rename(quarantine, source, true); err != nil {
		return mapVFSErr(err)
	}
	return nil
}

// replaceRename is the same backup protocol for a same-device move, where the
// source itself is the staged tree and must remain available until the final
// rename succeeds. It returns the visible bytes actually removed from the
// destination after the destination is isolated under its backup name.
func (c *Core) replaceRename(
	root vfs.Root, source, dest vfs.SafePath,
) (uint64, error) {
	taken, err := pathExists(root, dest)
	if err != nil {
		return 0, err
	}
	if !taken {
		if rerr := root.Rename(source, dest, true); rerr != nil {
			return 0, mapVFSErr(rerr)
		}
		return 0, nil
	}
	backup, err := c.newControlSibling(root, dest.Parent(), ".scmeta-replace-")
	if err != nil {
		return 0, err
	}
	if rerr := root.Rename(dest, backup, true); rerr != nil {
		return 0, mapVFSErr(rerr)
	}
	hasReserved, rerr := treeContainsReserved(root, backup)
	if rerr != nil || hasReserved {
		if hasReserved {
			rerr = errf(ErrConflict, "the destination contains active control data")
		}
		return 0, restoreTransferBackup(root, dest, backup, rerr)
	}
	removedBytes, err := treeBytes(root, backup, vfs.HideReserved)
	if err != nil {
		return 0, restoreTransferBackup(root, dest, backup, err)
	}
	if err := root.Rename(source, dest, true); err != nil {
		rerr := root.Rename(backup, dest, true)
		if rerr != nil {
			return 0, &PartialTransferError{
				Destination: dest, Backup: backup,
				Cause: mapVFSErr(err), Rollback: mapVFSErr(rerr),
			}
		}
		return 0, mapVFSErr(err)
	}
	// A reserved child can be a live upload control. It must make replacement
	// fail and roll back, never be deleted with the old destination.
	if err := removeTree(root, backup, vfs.HideReserved); err != nil {
		rerr := c.rollbackMoved(root, source, dest, backup)
		if rerr != nil {
			return 0, &PartialTransferError{
				Destination: dest, Backup: backup,
				Cause: mapVFSErr(err), Rollback: rerr,
			}
		}
		return 0, fmt.Errorf(
			"move replacement cleanup failed and was rolled back: %w", err,
		)
	}
	return removedBytes, nil
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
// copyFile duplicates one file's content for the direct, single-file path.
// Its durable boundary books positive growth before publication and credits a
// replacement shrink after publication.
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
	priorSt, perr := to.root.Stat(to.path)
	var prior *vfs.Stat
	switch {
	case perr == nil:
		prior = &priorSt
	case errors.Is(mapVFSErr(perr), ErrNotFound):
	default:
		return mapVFSErr(perr)
	}
	opts := vfs.DurableOpts{Mode: to.root.Policy().ModeFile}
	if _, _, err := c.writeDurableQuota(ctx, to, opts, prior, func(dst *vfs.File) error {
		_, cerr := vfs.CopyRange(src, 0, dst, 0, st.Size)
		return cerr
	}); err != nil {
		return mapVFSErr(err)
	}

	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, to, journal.OpCopy)
	return nil
}

// copyFileRaw writes a file into a fresh hidden stage. It deliberately has no
// quota or journal side effects; the enclosing tree operation settles one
// reservation and one operation row at the final publication boundary.
func (c *Core) copyFileRaw(ctx context.Context, from, to Resolved) error {
	src, err := from.root.OpenRead(from.path, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			c.warn("closing a staged copy source failed",
				"path", from.path.String(), "error", cerr)
		}
	}()
	st, err := src.Stat()
	if err != nil {
		return mapVFSErr(err)
	}
	opts := vfs.DurableOpts{Mode: to.root.Policy().ModeFile, NoClobber: true}
	if _, err := to.root.WriteDurable(to.path, opts, func(dst *vfs.File) error {
		_, cerr := vfs.CopyRange(src, 0, dst, 0, st.Size)
		return cerr
	}); err != nil {
		return mapVFSErr(err)
	}
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
