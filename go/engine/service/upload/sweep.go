//go:build linux

package upload

import (
	"context"
	"errors"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// SweepReport records what a single sweep accomplished, broken down by kind of
// debt.
type SweepReport struct {
	// ExpiredSessions counts rows past their lifetime, along with their part
	// files.
	ExpiredSessions int
	// OrphanParts counts part files lacking a session row and older than the
	// grace period.
	OrphanParts int
	// OrphanSpools counts spool directories in that same state.
	OrphanSpools int
	// OrphanCaches counts cache directories whose session row has disappeared.
	// They are tallied separately because no walk of the shares can locate them:
	// the spool is not a share, and the row is the only thing naming a directory
	// within it.
	OrphanCaches int
}

// Sweep reclaims the two kinds of debt an upload leaves behind: a session row
// whose part file has vanished, and a part file whose session row has vanished.
//
// Neither is deduced from the other's absence within one pass. Both sides are
// read before anything is acted on, so a session created between the two reads
// is not misread as an orphan, and an orphan is additionally only collected once
// it exceeds the session lifetime in age.
func (e *Engine) Sweep(ctx context.Context) (SweepReport, error) {
	var rep SweepReport
	now := e.clk.Nanos()

	// Side one: every session that exists right now, read before a single
	// directory is opened, which is the whole of the race argument.
	live, err := e.state.ListUploadSessions(ctx)
	if err != nil {
		return rep, err
	}

	expired, err := e.state.ListExpiredUploadSessions(ctx, now)
	if err != nil {
		return rep, err
	}
	for _, sess := range expired {
		// A session that is publishing is not abandoned: a long assembly must
		// not be collected halfway through its own publish.
		if SessionState(sess.State) == StateFinalizing {
			continue
		}
		if e.collectExpired(ctx, sess) {
			rep.ExpiredSessions++
		}
	}

	// The second side: every directory in which a part file has ever been
	// created. Walking whole shares would traverse a multi-terabyte tree to
	// locate a handful of control files, while walking only the live sessions'
	// directories would miss the very orphans this exists for, since an orphan
	// is a part file whose session row has already gone.
	dirs, err := e.touchedDirs(ctx)
	if err != nil {
		return rep, err
	}
	for key, dir := range dirs {
		parts, spools := e.sweepDir(key.share, dir, live, now)
		rep.OrphanParts += parts
		rep.OrphanSpools += spools
	}

	rep.OrphanCaches = e.sweepCache(live)
	return rep, nil
}

// sweepCache deletes cache directories claimed by no live session.
func (e *Engine) sweepCache(live []state.UploadSession) int {
	if e.cache == nil {
		return 0
	}
	claimed := map[string]struct{}{}
	for _, sess := range live {
		if sess.CacheDir != "" {
			claimed[sess.CacheDir] = struct{}{}
		}
	}
	taken := 0
	for _, dir := range e.cache.sessionDirs() {
		if _, held := claimed[dir.Name()]; held {
			continue
		}
		e.cache.removeSession(dir)
		taken++
	}
	return taken
}

// shareDir identifies a single directory within one share, the unit the sweep
// traverses.
type shareDir struct {
	share core.ShareID
	dir   string
}

// touchedDirs is every directory a part file has been created in, which is
// the only set that still names the place an orphan is.
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
			// A stored path that fails to parse names nothing openable here. It
			// is skipped rather than failing the sweep, which still has every
			// other directory to process.
			continue
		}
		out[shareDir{share: share, dir: t.Dir}] = dir
	}
	return out, nil
}

// collectExpired deletes an expired session's part file, spool directory and
// row.
//
// A row whose deletion fails is left in place and retried on the next sweep
// rather than aborting the entire pass. One bad row halting every other share's
// cleanup is precisely the failure a periodic sweep is meant to withstand.
func (e *Engine) collectExpired(ctx context.Context, sess state.UploadSession) bool {
	share, ok := shareIDOf(sess.Share)
	if !ok {
		return false
	}
	root, ok := e.core.ShareRoot(share)
	if !ok {
		// The share is unregistered in this process, leaving its files
		// unreachable. The row remains, since deleting it would strand the part
		// file with nothing naming it.
		return false
	}
	dest, err := vfs.ParseSafePath(sess.Dest)
	if err != nil {
		return false
	}

	if part, perr := partPath(dest, sess.PartName); perr == nil {
		if uerr := root.Unlink(part); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("could not remove an expired upload's part file",
				"dest", sess.Dest, "error", uerr)
		}
	}
	if sess.SpoolDir != "" {
		if dir, derr := dest.Parent().JoinControl(sess.SpoolDir); derr == nil {
			e.removeSpoolDir(root, dir)
		}
	}
	// The cache spool is not a share, so no directory walk reaches it. The
	// session row is the only thing that names this directory, and it is about
	// to be deleted.
	id := sessionIDOrZero(sess.ID)
	e.stopMerger(id)
	e.releaseCache(sess.CacheDir)

	if derr := e.state.DeleteUploadSession(ctx, sess.ID); derr != nil {
		e.log.Warn("could not delete an expired upload session; it is retried next sweep",
			"dest", sess.Dest, "error", derr)
		return false
	}
	e.closeHandle(id)
	e.forgetRow(id)
	return true
}

// sweepDir deletes control files in a directory that no live session claims and
// that exceed the grace period in age.
func (e *Engine) sweepDir(
	share core.ShareID, dir vfs.SafePath, live []state.UploadSession, now int64,
) (parts, spools int) {
	root, ok := e.core.ShareRoot(share)
	if !ok {
		return 0, 0
	}
	claimed := claimedNames(live, share, dir)

	var candidates []vfs.DirEntry
	// One of the few callers that has to see control files, and it says so at
	// the call site rather than through an ambient flag: what a directory read
	// returns is a security-relevant answer.
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
		// An unreadable directory is no reason to abandon sweeping the rest; it
		// may have been removed since the session that named it.
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
		// Old enough to be a genuine orphan rather than a session created while
		// this sweep ran. The two reads already exclude most of that case; this
		// covers a session whose row arrives between them.
		if now-st.MtimeNs < int64(sessionTTL()) {
			continue
		}
		if st.Kind.IsDir() {
			e.removeSpoolDir(root, child)
			spools++
			continue
		}
		if uerr := root.Unlink(child); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("could not remove an orphaned upload part file",
				"name", entry.Name, "error", uerr)
			continue
		}
		parts++
	}
	return parts, spools
}

// claimedNames lists the control names live sessions hold within a directory, so
// the sweep never collects a file an in-flight upload is still writing.
//
// Directory comparison runs component by component through the path type rather
// than over strings, because "ab" does not sit inside "a" while a string prefix
// test would claim it does.
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

// removeSpoolDir clears a spool directory and deletes it. Both steps are best
// effort, since anything left behind stays unlistable and resurfaces on the next
// sweep.
func (e *Engine) removeSpoolDir(root vfs.Root, dir vfs.SafePath) {
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
			e.log.Warn("could not remove a spooled upload chunk", "name", name, "error", uerr)
		}
	}
	if rerr := root.Rmdir(dir); rerr != nil && !errors.Is(rerr, vfs.ErrNotFound) {
		e.log.Warn("could not remove an upload spool directory", "error", rerr)
	}
}
