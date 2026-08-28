//go:build linux

package core

import (
	"context"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
)

// What this account wrote, newest first.
//
// It reads the journal rather than walking the filesystem, so it is exact and
// cheap: a walk cannot answer "what did this account write" at all, because
// files carry no writer, and mtime cannot tell this server's writes from
// anything else. The journal is the only source that knows.
//
// Every row is re-checked before it is returned. A row records that the
// account wrote the file, not that they may still see it.

// RecentHit is one write, resolved back into something navigable.

// leafOf is the last component of a share-relative path, which is the file's
// own name. SharePath offers no name accessor, so this stays local.
func leafOf(p vfs.SharePath) string {
	s := p.String()
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

type RecentHit struct {
	Vpath   vfs.Vpath
	Share   string
	Subpath vfs.SharePath
	Name    string
	Size    uint64
	MTimeNs int64
	// AtNs is when the write happened, which is not the modification time
	// for a restore or a copy that preserved timestamps. A client sorting
	// "what did I just do" needs this rather than MTimeNs.
	AtNs int64
	Op   journal.Op
}

// RecentQuery narrows a listing.
type RecentQuery struct {
	// SinceNs bounds the window. Zero is no window.
	SinceNs int64
	Limit   int
	// Scope restricts to one virtual subtree, as the client spells a path.
	// Empty is everywhere the account can read.
	Scope string
}

// Recent lists this account's writes.
//
// Dropped rows are not backfilled: a query for 50 of which 10 fail
// revalidation returns 40. The limit bounds journal work rather than the
// answer size, and a second page is the client's request to make.
func (c *Core) Recent(ctx context.Context, user UserID, q RecentQuery) ([]RecentHit, error) {
	if c.journal == nil {
		// A deployment that kept no history, or lost the journal file, is
		// not an error. An empty list is the honest answer.
		return nil, nil
	}
	account, err := num.Narrow[uint32](int64(user))
	if err != nil {
		return nil, err
	}

	events, err := c.journal.RecentSince(ctx, account, q.SinceNs, q.Limit)
	if err != nil {
		return nil, err
	}

	out := make([]RecentHit, 0, len(events))
	for _, e := range events {
		// Each failure below silently drops the row. Reporting the drop would
		// leak exactly the fact a revocation exists to hide.
		vp, verr := c.VpathFor(user, e.Share, e.Path)
		if verr != nil {
			// The share is no longer readable by this account, so the row is
			// not theirs to see any more.
			continue
		}
		if q.Scope != "" && !strings.HasPrefix(vp.String(), q.Scope) {
			continue
		}
		// Re-checked at the path, not at the share: a grant can be revoked on
		// a subtree while the share stays readable, and the resolve gate is
		// the one place that judgment is made.
		r, rerr := c.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		st, serr := r.root.Stat(r.path)
		if serr != nil {
			// Written once and gone since. Dropping it is the row being
			// revalidated rather than trusted.
			continue
		}

		out = append(out, RecentHit{
			Vpath:   vp,
			Share:   vp.Label(),
			Subpath: e.Path,
			Name:    leafOf(e.Path),
			Size:    st.Size,
			MTimeNs: st.MtimeNs,
			AtNs:    e.AtNs,
			Op:      e.Op,
		})
	}
	return out, nil
}
