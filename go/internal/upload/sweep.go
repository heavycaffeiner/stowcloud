//go:build linux

package upload

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// SweepReport is what one sweep did, per kind of debt.
type SweepReport struct {
	// ExpiredSessions is rows whose lifetime ran out, with their part files.
	ExpiredSessions int
	// OrphanParts is part files with no session row, older than the grace
	// period.
	OrphanParts int
	// OrphanSpools is spool directories in the same position.
	OrphanSpools int
}

// Sweep collects the two kinds of debt an upload leaves: a session row whose
// part file is gone, and a part file whose session row is gone.
//
// Neither is inferred from the other's absence in a single pass. Both sides
// are read first and then acted on, so a session created between the two reads
// is not mistaken for an orphan, and an orphan is additionally only taken once
// it is older than the session lifetime.
func (e *Engine) Sweep(ctx context.Context) (SweepReport, error) {
	var rep SweepReport
	now := e.clk.Nanos()

	// Side one: every session that exists right now. This is read before a
	// single directory is opened, which is the whole of the race argument.
	live, err := e.state.ListUploadSessions(ctx)
	if err != nil {
		return rep, err
	}

	expired, err := e.state.ListExpiredUploadSessions(ctx, now)
	if err != nil {
		return rep, err
	}
	for _, sess := range expired {
		if e.collectExpired(ctx, sess) {
			rep.ExpiredSessions++
		}
	}

	// Side two: the directories a part file has ever been created in. A sweep
	// that walked whole shares would read a 12 TB tree to find a handful of
	// control files, and one that walked only the live sessions' directories
	// could not see the orphan it exists for: an orphan is a part file whose
	// session row is already gone.
	dirs, err := e.touchedDirs(ctx)
	if err != nil {
		return rep, err
	}
	for key, dir := range dirs {
		parts, spools := e.sweepDir(key.share, dir, live, now)
		rep.OrphanParts += parts
		rep.OrphanSpools += spools
	}
	return rep, nil
}

// shareDir names one directory inside one share, which is the unit the sweep
// walks.
type shareDir struct {
	share core.ShareID
	dir   string
}

// touchedDirs is every directory a part file has been created in, which is the
// only set that still names the place an orphan is.
func (e *Engine) touchedDirs(ctx context.Context) (map[shareDir]vfs.SafePath, error) {
	rows, err := e.state.ListUploadTouchedDirs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[shareDir]vfs.SafePath, len(rows))
	for _, t := range rows {
		share, ok := shareIDOf(t.Share)
		if !ok {
			continue
		}
		dir, perr := vfs.ParseSafePath(t.Dir)
		if perr != nil {
			// A stored path that will not parse names nothing this can open.
			// It is skipped rather than failing the sweep, which still has
			// every other directory to get through.
			continue
		}
		out[shareDir{share: share, dir: t.Dir}] = dir
	}
	return out, nil
}

// collectExpired removes an expired session's part file, its spool directory
// and its row.
//
// A row whose delete fails is left alone and retried next sweep rather than
// aborting the whole pass: one bad row stopping every other share's cleanup is
// exactly the failure a periodic sweep exists to be resilient against.
func (e *Engine) collectExpired(ctx context.Context, sess state.UploadSession) bool {
	share, ok := shareIDOf(sess.Share)
	if !ok {
		return false
	}
	root, ok := e.core.ShareRoot(share)
	if !ok {
		// The share is not registered in this process, so its files are not
		// reachable. The row stays: deleting it would strand the part file
		// with nothing left naming it.
		return false
	}
	dest, err := vfs.ParseSafePath(sess.Dest)
	if err != nil {
		return false
	}

	if part, perr := partPath(dest, sess.PartName); perr == nil {
		if uerr := root.Unlink(part); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("could not remove an expired upload's part file",
				slog.String("dest", sess.Dest), slog.Any("error", uerr))
		}
	}
	if sess.SpoolDir != "" {
		if dir, derr := dest.Parent().JoinControl(sess.SpoolDir); derr == nil {
			e.removeSpoolDir(root, dir)
		}
	}

	if derr := e.state.DeleteUploadSession(ctx, sess.ID); derr != nil {
		e.log.Warn("could not delete an expired upload session; it is retried next sweep",
			slog.String("dest", sess.Dest), slog.Any("error", derr))
		return false
	}
	if id, ierr := sessionIDFromBytes(sess.ID); ierr == nil {
		e.closeHandle(id)
		e.forgetRow(id)
	}
	return true
}

// sweepDir removes the control files in one directory that no live session
// claims and that are older than the grace period.
func (e *Engine) sweepDir(
	share core.ShareID, dir vfs.SafePath, live []state.UploadSession, now int64,
) (parts, spools int) {
	root, ok := e.core.ShareRoot(share)
	if !ok {
		return 0, 0
	}
	claimed := claimedNames(live, share, dir)

	var candidates []vfs.DirEntry
	// This is one of the two callers that has to see control files, and it
	// says so at the call site rather than through an ambient flag: what a
	// directory read returns is a security-relevant answer.
	err := root.ReadDirFunc(dir, vfs.IncludeReserved, func(entry vfs.DirEntry) bool {
		if !strings.HasPrefix(entry.Name, ".scpart-") {
			return true
		}
		if _, held := claimed[entry.Name]; held {
			return true
		}
		candidates = append(candidates, entry)
		return true
	})
	if err != nil {
		// A directory that cannot be read is not a reason to stop sweeping the
		// others: it may have been deleted since the session that named it.
		return 0, 0
	}

	for _, entry := range candidates {
		child, jerr := dir.JoinControl(entry.Name)
		if jerr != nil {
			continue
		}
		st, serr := root.Stat(child)
		if serr != nil {
			continue
		}
		// Old enough to be an orphan rather than a session created while this
		// sweep was running. The two reads already rule out most of that; this
		// is the belt for a session whose row lands between them.
		if now-st.MtimeNs < int64(limits.UploadSessionTTL) {
			continue
		}
		if st.Kind.IsDir() {
			e.removeSpoolDir(root, child)
			spools++
			continue
		}
		if uerr := root.Unlink(child); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("could not remove an orphaned upload part file",
				slog.String("name", entry.Name), slog.Any("error", uerr))
			continue
		}
		parts++
	}
	return parts, spools
}

// claimedNames is the control names live sessions hold in one directory, so
// the sweep never takes a file an upload in flight is still writing to.
//
// The directory comparison is component-wise through the path type rather than
// on the strings: "ab" is not inside "a", and a string prefix test says it is.
func claimedNames(live []state.UploadSession, share core.ShareID, dir vfs.SafePath) map[string]struct{} {
	out := map[string]struct{}{}
	for _, sess := range live {
		id, ok := shareIDOf(sess.Share)
		if !ok || id != share {
			continue
		}
		dest, err := vfs.ParseSafePath(sess.Dest)
		if err != nil || !dest.Parent().Equal(dir) {
			continue
		}
		out[sess.PartName] = struct{}{}
		if sess.SpoolDir != "" {
			out[sess.SpoolDir] = struct{}{}
		}
	}
	return out
}

// removeSpoolDir empties a spool directory and removes it. Both steps are
// best-effort: what is left behind is unlistable and is found again next
// sweep.
func (e *Engine) removeSpoolDir(root *vfs.ShareRoot, dir vfs.SafePath) {
	var names []string
	if err := root.ReadDirFunc(dir, vfs.IncludeReserved, func(entry vfs.DirEntry) bool {
		names = append(names, entry.Name)
		return true
	}); err != nil {
		return
	}
	for _, name := range names {
		child, jerr := dir.JoinControl(name)
		if jerr != nil {
			continue
		}
		if uerr := root.Unlink(child); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("could not remove a spooled upload chunk",
				slog.String("name", name), slog.Any("error", uerr))
		}
	}
	if rerr := root.Rmdir(dir); rerr != nil && !errors.Is(rerr, vfs.ErrNotFound) {
		e.log.Warn("could not remove an upload spool directory", slog.Any("error", rerr))
	}
}

func shareIDOf(v int64) (core.ShareID, bool) {
	if v < 0 || v > int64(^uint32(0)) {
		return 0, false
	}
	return core.ShareID(uint32(v)), true //nolint:gosec // G115 reads the conversion: the bound above is the check.
}
