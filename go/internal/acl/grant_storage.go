package acl

import (
	"context"
	"database/sql"
	"fmt"
)

// LoadFromState replaces the evaluator's grants and memberships from the
// store. Membership and grants move as one load so the two can never be read a
// generation apart.
func (e *Evaluator) LoadFromState(ctx context.Context, db readDB) error {
	grants, err := readGrants(ctx, db)
	if err != nil {
		return err
	}
	m, err := readMemberships(ctx, db)
	if err != nil {
		return err
	}
	e.ReplaceGrants(grants)
	e.SetMemberships(m)
	return nil
}

// readDB is the store's read surface.
type readDB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readGrants(ctx context.Context, db readDB) (grants []Grant, err error) {
	rows, err := db.QueryContext(ctx, sqlReadGrants)
	if err != nil {
		return nil, fmt.Errorf("reading grants: %w", err)
	}
	defer func() { err = joinErr(err, rows.Close()) }()
	for rows.Next() {
		var (
			g           Grant
			user, group sql.NullInt64
			subpath     string
			inherit     int
			allow, deny int64
			label       sql.NullString
		)
		if serr := rows.Scan(&g.ID, &user, &group, &g.Share, &subpath,
			&allow, &deny, &inherit, &label); serr != nil {
			return nil, serr
		}
		g.User, g.Group = user.Int64, group.Int64
		g.Subpath = ParsePath(subpath)
		g.Allow = Perms(allow) //nolint:gosec // the stored integer is one of the eight 1<<i bits, far inside uint16.
		g.Deny = Perms(deny)   //nolint:gosec // as above for allow.
		g.Inherit = inherit != 0
		if label.Valid {
			g.Label = label.String
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

func readMemberships(ctx context.Context, db readDB) (ms membership, err error) {
	rows, err := db.QueryContext(ctx, sqlReadMemberships)
	if err != nil {
		return nil, fmt.Errorf("reading memberships: %w", err)
	}
	defer func() { err = joinErr(err, rows.Close()) }()
	ms = membership{}
	for rows.Next() {
		var user, group int64
		if serr := rows.Scan(&user, &group); serr != nil {
			return nil, serr
		}
		ms[user] = append(ms[user], group)
	}
	return ms, rows.Err()
}

// joinErr folds a deferred close error into the named return.
func joinErr(err error, closed error) error {
	if closed != nil {
		return fmt.Errorf("%w; %v", err, closed)
	}
	return err
}
