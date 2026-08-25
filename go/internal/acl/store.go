package acl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The write half of the grant table.
//
// Reading is the evaluator's hot path and is served from memory. Writing goes
// to the database and then reloads, because a grant that is live in one and
// not the other is a permission decision that depends on which half answered.

// ErrNoSuchGrant is a grant id that names nothing.
var ErrNoSuchGrant = errors.New("acl: no such grant")

// writeDB is the write surface, kept narrow so this package cannot reach the
// rest of the store.
type writeDB interface {
	readDB
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ListGrants returns the stored grants, optionally narrowed.
//
// The filters are applied here rather than in SQL because the table is small
// enough to hold in memory for the evaluator anyway, and a statement built
// from optional parts is the thing this package's statements are constants to
// avoid.
func ListGrants(ctx context.Context, db readDB, filter GrantFilter) ([]Grant, error) {
	all, err := readGrants(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]Grant, 0, len(all))
	for _, g := range all {
		if filter.User != 0 && g.User != filter.User {
			continue
		}
		if filter.Group != 0 && g.Group != filter.Group {
			continue
		}
		if filter.Share != 0 && g.Share != filter.Share {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

// GrantFilter narrows a listing. A zero field is not a filter.
type GrantFilter struct {
	User  int64
	Group int64
	Share int64
}

// CreateGrant stores one grant and returns it with the id it was given.
//
// A grant naming neither an account nor a group is refused: it would apply to
// nobody, and a rule that applies to nobody in a table read as "who may do
// what" is a rule somebody meant to attach to someone.
func CreateGrant(ctx context.Context, db writeDB, g Grant, nowNs int64) (Grant, error) {
	if g.User == 0 && g.Group == 0 {
		return Grant{}, fmt.Errorf("%w: a grant names neither an account nor a group", ErrNoSuchGrant)
	}
	if g.Share == 0 {
		return Grant{}, fmt.Errorf("%w: a grant names no share", ErrNoSuchGrant)
	}
	res, err := db.ExecContext(ctx, sqlInsertGrant,
		nullable(g.User), nullable(g.Group), g.Share, g.Subpath.String(),
		int64(g.Allow), int64(g.Deny), g.Inherit, g.Label, nowNs)
	if err != nil {
		return Grant{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Grant{}, err
	}
	g.ID = id
	return g, nil
}

// UpdateGrant replaces the permission bits, the inheritance and the label of
// one grant. An empty label clears it.
//
// What it deliberately cannot change is who the grant is for or which share it
// covers. Those identify the grant; changing them is deleting one rule and
// writing another, and doing that under one id makes an audit trail read as
// though a permission moved when a different rule replaced it.
func UpdateGrant(ctx context.Context, db writeDB, id int64, allow, deny Perms, inherit bool, label string) error {
	var stored any
	if label != "" {
		stored = label
	}
	res, err := db.ExecContext(ctx, sqlUpdateGrant, int64(allow), int64(deny), inherit, stored, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoSuchGrant
	}
	return nil
}

// DeleteGrant removes one grant.
func DeleteGrant(ctx context.Context, db writeDB, id int64) error {
	res, err := db.ExecContext(ctx, sqlDeleteGrant, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoSuchGrant
	}
	return nil
}

// nullable turns an unset id into a stored null, because the column means
// "not this kind of principal" rather than "principal zero".
func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
