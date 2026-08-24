//go:build linux && compat_nc

package nc

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The mobile surfaces: favourites, the trash listing and the recency query.

// Favorites lists what a user has starred.
//
// Keyed by the file's identity rather than by its path, so a star follows a
// file through a rename and a new file at the old name does not inherit one.
// The stored path is what was last seen, which is where the client is sent.
func (l *Layer) Favorites(ctx context.Context, user ncport.UserID) ([]ncport.Favorite, error) {
	return l.deps.State.Favorites(ctx, user)
}

// SetFavorite stars or unstars an entry.
func (l *Layer) SetFavorite(ctx context.Context, user ncport.UserID, f ncport.Favorite, on bool) error {
	return l.deps.State.SetFavorite(ctx, user, f, on)
}

// RecentQuery is the recency request the mobile clients send.
//
// It carries one recorded defect worth keeping fixed. A bare date literal made
// both apps' request fail: the comparison has to be a full timestamp, so the
// bound is rendered in ISO 8601 with a timezone rather than as a date. The
// collector behind it is unchanged; only the query shape moved.
type RecentQuery struct {
	// Since bounds how far back to look.
	Since time.Time
	// Limit caps the result.
	Limit int
}

// RecentSince renders the lower bound the way the query needs it.
//
// A bare date is what broke this: the value is compared against a timestamp,
// and a date literal is not one. The zone is explicit for the same reason: a
// naive local time means something different on each side of the comparison.
func RecentSince(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// ParseRecentQuery reads the request's parameters.
func ParseRecentQuery(rawLimit, rawSince string, now time.Time) (RecentQuery, error) {
	q := RecentQuery{Limit: 30, Since: now.Add(-14 * 24 * time.Hour)}

	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n <= 0 {
			return RecentQuery{}, fmt.Errorf("%w: a limit of %q", ErrChunkBadRequest, rawLimit)
		}
		// Capped rather than refused: a client asking for more than the server
		// will produce gets what there is, which is how the reference behaves.
		if n > 200 {
			n = 200
		}
		q.Limit = n
	}

	if rawSince != "" {
		// Both spellings a client sends: a full timestamp, and the seconds
		// count some versions send instead.
		if t, err := time.Parse(time.RFC3339, rawSince); err == nil {
			q.Since = t
		} else if secs, serr := strconv.ParseInt(rawSince, 10, 64); serr == nil {
			q.Since = time.Unix(secs, 0)
		} else {
			return RecentQuery{}, fmt.Errorf("%w: a timestamp of %q", ErrChunkBadRequest, rawSince)
		}
	}
	return q, nil
}
