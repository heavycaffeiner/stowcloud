package core

import (
	"context"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// What this account wrote, newest first.
//
// It reads the journal rather than walking, so it is exact and cheap: the rows
// are the writes that actually went through this server, and there is nothing
// to truncate.
//
// Every row is re-checked before it is returned. A row records that the account
// wrote the file, not that they may still read it: a grant revoked since then
// has to hide it, and the file may be gone entirely.

// leafOf is the last component of a share-relative path, which is the file's
// own name.
func leafOf(p vfs.SharePath) string {
	s := p.String()
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// RecentHit is one write, resolved back into something navigable.
type RecentHit struct {
	Vpath   vfs.Vpath
	Share   string
	Subpath vfs.SharePath
	Name    string
	Size    uint64
	MTimeNs int64
	// AtNs is when the write happened, which is not the modification time for
	// a restore or a copy that preserved timestamps.
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
func (c *Core) Recent(ctx context.Context, user UserID, q RecentQuery) ([]RecentHit, error) {
	if c.journal == nil {
		// The journal is off, which is a deployment that kept no history
		// rather than an error. An empty list is the honest answer.
		return nil, nil
	}
	acc, nerr := num.Narrow[uint32](int64(user))
	if nerr != nil {
		return nil, nerr
	}

	events, err := c.journal.RecentSince(ctx, acc, q.SinceNs, q.Limit)
	if err != nil {
		return nil, err
	}

	out := make([]RecentHit, 0, len(events))
	for _, e := range events {
		vp, verr := c.VpathFor(user, e.Share, e.Path)
		if verr != nil {
			// The share is no longer readable by this account, so the row is
			// not theirs to see any more.
			continue
		}
		if q.Scope != "" && !strings.HasPrefix(vp.String(), q.Scope) {
			continue
		}

		// The permission is re-checked at the path, not at the share: a grant
		// can be revoked on a subtree while the share stays readable.
		resolved, rerr := c.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		st, serr := resolved.root.Stat(resolved.path)
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
