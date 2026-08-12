package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// copyLocks carries every WebDAV lock that has not expired.
//
// The tree this replaces makes locks durable on purpose, so that a restart
// cannot erase an exclusion a client is relying on. A cutover is a restart with
// a different binary on the other side of it, and it is not permission to erase
// one either: an editor holding an exclusive lock over a document has no way to
// know the server forgot it, and the next writer wins silently.
//
// So an active lock that cannot be carried across stops the import. Dropping it
// would open exactly the write window the lock exists to close, at the one
// moment nobody is watching.
func (s *sources) copyLocks(
	ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool, clk clock.Clock,
) (err error) {
	if s.locks == nil {
		return nil
	}
	rows, err := s.locks.QueryContext(ctx, selDavLock)
	if err != nil {
		return fmt.Errorf("importing dav_lock: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	now := clk.Nanos()
	var kept, expired int
	for rows.Next() {
		var (
			token, path, owner, expiresText string
			fileid, share, principal        int64
			depth, scope, timeout           int64
		)
		if serr := rows.Scan(&token, &fileid, &share, &path, &principal,
			&owner, &depth, &scope, &expiresText, &timeout); serr != nil {
			return fmt.Errorf("importing dav_lock: %w", serr)
		}

		// The old column is text holding a number. A value that will not parse,
		// or one outside what the destination column holds, is not something to
		// guess an expiry for.
		expires, perr := strconv.ParseInt(expiresText, 10, 64)
		if perr != nil {
			return fmt.Errorf("dav_lock %s carries the expiry %q, which is not a 64-bit number: %w",
				token, expiresText, perr)
		}
		if expires <= now {
			expired++
			continue
		}

		if !users[principal] {
			return fmt.Errorf("dav_lock %s is held by account %d, which no longer exists: "+
				"an active lock cannot be dropped without opening the write window it closes",
				token, principal)
		}
		identity, resolved, ierr := s.identOf(ctx, fileid)
		if ierr != nil {
			return fmt.Errorf("importing dav_lock %s: %w", token, ierr)
		}
		if !resolved {
			return fmt.Errorf("dav_lock %s names file id %d, which %s cannot resolve: "+
				"an active lock cannot be dropped without opening the write window it closes",
				token, fileid, metaFile)
		}

		if _, err := tx.ExecContext(ctx, insDavLock, token, share,
			identity.dev, identity.ino, identity.present, identity.btime,
			path, principal, owner, depth, scope, expires, timeout); err != nil {
			return fmt.Errorf("importing dav_lock %s: %w", token, err)
		}
		kept++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("importing dav_lock: %w", err)
	}
	record(rep, "dav_lock", kept, map[Reason]int{ReasonExpired: expired})
	return nil
}
