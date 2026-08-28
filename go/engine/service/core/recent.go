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

// This account's writes, newest first.
//
// The journal supplies the answer rather than a filesystem walk, making it both
// exact and inexpensive. A walk cannot answer the question at all: files record
// no writer, and mtime cannot separate this server's writes from anyone else's.
// Only the journal knows.
//
// Rows are revalidated before being returned. A row establishes that the account
// wrote the file, not that it remains visible to them.

// RecentHit is a single write resolved back into a navigable form.

// leafOf extracts the final component of a share-relative path, giving the
// file's own name. SharePath exposes no name accessor, so this stays local.
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

// RecentQuery restricts a listing.
type RecentQuery struct {
	// SinceNs limits the window; zero imposes none.
	SinceNs int64
	Limit   int
	// Scope confines results to one virtual subtree, written as the client
	// spells a path. Empty covers everywhere the account can read.
	Scope string
}

// Recent enumerates this account's writes.
//
// Discarded rows are not replaced: requesting 50 where 10 fail revalidation
// returns 40. The limit constrains journal work rather than the size of the
// answer, and requesting a second page is the client's decision.
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
			// This account can no longer read the share, so the row is no
			// longer theirs to view.
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
			// Written at some point and since removed. Discarding it is what
			// revalidating rather than trusting the row means.
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
