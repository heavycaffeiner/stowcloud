package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The grant table's write half. Reading is the evaluator's hot path and is
// served from memory once loaded; this aggregate is what loads it and what
// every write goes through, so nothing above this layer needs a
// database/sql handle to change a permission.

// ErrNoSuchGrant reports a grant id matching nothing.
var ErrNoSuchGrant = errors.New("no such grant")

// GrantRow is one stored grant, in this store's own row shape. The ACL
// package owns the domain shape and the core converts between the two: here
// an unset principal is a nil pointer, because the column means "not this
// kind of principal" rather than "principal zero".
type GrantRow struct {
	ID    int64
	User  *int64
	Group *int64
	Share int64
	// Subpath is the ACL package's path, string-spelled.
	Subpath   string
	Allow     uint16
	Deny      uint16
	Inherit   bool
	Label     string
	CreatedNs int64
}

// GrantFilter restricts a listing. Fields left at zero impose no restriction.
type GrantFilter struct {
	User  int64
	Group int64
	Share int64
}

// MembershipRow is one (user, group) pairing.
type MembershipRow struct {
	User  int64
	Group int64
}

// PersistGrant writes one grant and returns the id it was given.
//
// The validation runs here rather than being left to the schema's own CHECK:
// a constraint failure names the constraint, not the caller's mistake, and
// the caller is what needs correcting. A grant naming neither an account nor
// a group would apply to nobody, and one with an empty allow set grants
// nothing, so both are refused rather than stored.
//
// It does not reload the evaluator. That stays the caller's next explicit
// step, so a caller writing several grants reloads once rather than once per
// grant.
func (d *DB) PersistGrant(ctx context.Context, g GrantRow, nowNs int64) (int64, error) {
	switch {
	case (g.User == nil) == (g.Group == nil):
		return 0, fmt.Errorf(
			"%w: a grant names exactly one of an account and a group", ErrNoSuchGrant)
	case g.Share == 0:
		return 0, fmt.Errorf("%w: a grant names no share", ErrNoSuchGrant)
	case g.Allow == 0:
		return 0, fmt.Errorf("%w: a grant allows nothing", ErrNoSuchGrant)
	}
	// This is the aggregate's one insert path, so it is where the guard sits.
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}

	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertGrant,
			idArg(g.User), idArg(g.Group), g.Share, g.Subpath,
			int64(g.Allow), int64(g.Deny), g.Inherit, labelArg(g.Label), nowNs)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return 0, fmt.Errorf("storing a grant: %w", err)
	}
	return id, nil
}

// ListGrants yields the stored grants, restricted when asked.
func (d *DB) ListGrants(ctx context.Context, filter GrantFilter) (out []GrantRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlReadGrants)
	if err != nil {
		return nil, fmt.Errorf("reading grants: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			g           GrantRow
			user, group *int64
			allow, deny int64
			label       *string
		)
		if serr := rows.Scan(&g.ID, &user, &group, &g.Share, &g.Subpath,
			&allow, &deny, &g.Inherit, &label, &g.CreatedNs); serr != nil {
			return nil, fmt.Errorf("reading a grant: %w", serr)
		}
		g.User, g.Group = user, group
		// A stored bit set that no longer fits the permission width is a
		// corrupt row, which is worth saying rather than truncating into a
		// different set of permissions.
		if g.Allow, err = num.Narrow[uint16](allow); err != nil {
			return nil, fmt.Errorf("grant %d carries allow bits %d: %w", g.ID, allow, err)
		}
		if g.Deny, err = num.Narrow[uint16](deny); err != nil {
			return nil, fmt.Errorf("grant %d carries deny bits %d: %w", g.ID, deny, err)
		}
		if label != nil {
			g.Label = *label
		}
		if !keepGrant(g, filter) {
			continue
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading grants: %w", err)
	}
	return out, nil
}

// keepGrant applies the filter in Go. The table is small enough to hold in
// memory for the evaluator anyway, and a WHERE clause built from optional
// parts is what every statement here being a constant prevents.
func keepGrant(g GrantRow, filter GrantFilter) bool {
	if filter.User != 0 && (g.User == nil || *g.User != filter.User) {
		return false
	}
	if filter.Group != 0 && (g.Group == nil || *g.Group != filter.Group) {
		return false
	}
	if filter.Share != 0 && g.Share != filter.Share {
		return false
	}
	return true
}

// UpdateGrant substitutes a grant's permission bits, inheritance and label. An
// empty label removes it.
//
// It cannot alter the grant's subject or the share it applies to. Those identify
// the grant, and changing them under a single id would make an audit trail
// appear to show a permission moving when in fact a different rule replaced
// it.
func (d *DB) UpdateGrant(
	ctx context.Context, id int64, allow, deny uint16, inherit bool, label string,
) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlUpdateGrant,
			int64(allow), int64(deny), inherit, labelArg(label), id)
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
	})
}

// DeleteGrant deletes a single grant.
func (d *DB) DeleteGrant(ctx context.Context, id int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlDeleteGrant, id)
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
	})
}

// Memberships returns every (user, group) pairing, for the evaluator's
// reload. It answers a flat slice rather than a map keyed by user: grouping
// is a shape the evaluator's own representation wants, not one this
// row-shaped surface should impose.
func (d *DB) Memberships(ctx context.Context) (out []MembershipRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlReadMemberships)
	if err != nil {
		return nil, fmt.Errorf("reading memberships: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var m MembershipRow
		if serr := rows.Scan(&m.User, &m.Group); serr != nil {
			return nil, fmt.Errorf("reading a membership: %w", serr)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading memberships: %w", err)
	}
	return out, nil
}

// idArg stores an unset principal as SQL NULL.
func idArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// labelArg stores an empty label as SQL NULL, so "no label" is one value on
// disk rather than two.
func labelArg(label string) any {
	if label == "" {
		return nil
	}
	return label
}
