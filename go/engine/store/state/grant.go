package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
)

// The grant table's write half. Reading is the evaluator's hot path and is
// served from memory once loaded; this aggregate is what loads it and what
// every write goes through, so nothing above this layer needs a
// database/sql handle to change a permission.

// ErrNoSuchGrant reports a grant id matching nothing.
var ErrNoSuchGrant = errors.New("no such grant")

// ErrGrantMalformed reports a grant the caller described wrongly: one that
// names no subject, no share, or neither an allow nor a deny.
//
// Separate from ErrNoSuchGrant because the two are different answers. A
// missing row is not found; a request that could never be a grant is the
// caller's to correct, and reporting it as an internal error left an
// administrator with a screen that failed for no stated reason.
var ErrGrantMalformed = errors.New("a grant that describes nothing")

// ErrGrantAlreadyExists reports a second grant naming the same subject over
// the same share and subpath. The unique index catches it; this names what
// the index means so the caller who submitted twice, or the screen that
// raced a double click, gets a refusal it can render instead of a driver's
// constraint message.
var ErrGrantAlreadyExists = errors.New("that subject already holds a grant over this share and subpath")

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
// a group would apply to nobody, and one that neither allows nor denies
// anything says nothing at all, so both are refused rather than stored.
//
// A grant that only denies is stored. Under default-deny an allow is what
// grants access, and a deny is how an inherited allow is carved back out for
// one subtree: refusing those made the exception unexpressible, so a folder
// inside a granted tree could not be closed off.
//
// It does not reload the evaluator. That stays the caller's next explicit
// step, so a caller writing several grants reloads once rather than once per
// grant.
func (d *DB) PersistGrant(ctx context.Context, g GrantRow, nowNs int64) (int64, error) {
	switch {
	case (g.User == nil) == (g.Group == nil):
		return 0, fmt.Errorf(
			"%w: a grant names exactly one of an account and a group", ErrGrantMalformed)
	case g.Share == 0:
		return 0, fmt.Errorf("%w: a grant names no share", ErrGrantMalformed)
	case g.Allow == 0 && g.Deny == 0:
		return 0, fmt.Errorf("%w: a grant neither allows nor denies anything", ErrGrantMalformed)
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
			if dbfile.IsUniqueViolation(ierr) {
				return ErrGrantAlreadyExists
			}
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		if errors.Is(err, ErrGrantAlreadyExists) {
			return 0, err
		}
		return 0, fmt.Errorf("storing a grant: %w", err)
	}
	return id, nil
}

// ListGrants yields the stored grants, restricted when asked.
func (d *DB) GrantByID(ctx context.Context, id int64) (GrantRow, error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlReadGrantByID, id)
	if err != nil {
		return GrantRow{}, fmt.Errorf("reading grant %d: %w", id, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	grants, err := scanGrantRows(rows)
	if err != nil {
		return GrantRow{}, err
	}
	if len(grants) == 0 {
		return GrantRow{}, ErrNoSuchGrant
	}
	return grants[0], nil
}

// ListGrants yields the stored grants, restricted when asked.
func (d *DB) ListGrants(ctx context.Context, filter GrantFilter) (out []GrantRow, err error) {
	query := sqlReadGrants
	var args []any
	switch {
	case filter.User > 0:
		query = sqlReadGrantsByUser
		args = []any{filter.User}
	case filter.Group > 0:
		query = sqlReadGrantsByGroup
		args = []any{filter.Group}
	case filter.Share > 0:
		query = sqlReadGrantsByShare
		args = []any{filter.Share}
	}

	rows, err := d.f.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading grants: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	grants, err := scanGrantRows(rows)
	if err != nil {
		return nil, err
	}
	out = make([]GrantRow, 0, len(grants))
	for _, g := range grants {
		if !keepGrant(g, filter) {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func scanGrantRows(rows *sql.Rows) ([]GrantRow, error) {
	var out []GrantRow
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
		var err error
		if g.Allow, err = num.Narrow[uint16](allow); err != nil {
			return nil, fmt.Errorf("grant %d carries allow bits %d: %w", g.ID, allow, err)
		}
		if g.Deny, err = num.Narrow[uint16](deny); err != nil {
			return nil, fmt.Errorf("grant %d carries deny bits %d: %w", g.ID, deny, err)
		}
		if label != nil {
			g.Label = *label
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
//
// Changing the reach can collide, because reach is part of what makes a grant
// unique: a grant over one folder cannot be widened to the whole subtree while
// the subject already holds a subtree grant there. That is the same refusal a
// second insert gets, and it is reported the same way rather than as a fault.
func (d *DB) UpdateGrant(
	ctx context.Context, id int64, allow, deny uint16, inherit bool, label string,
) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlUpdateGrant,
			int64(allow), int64(deny), inherit, labelArg(label), id)
		if err != nil {
			if dbfile.IsUniqueViolation(err) {
				return ErrGrantAlreadyExists
			}
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

// foldDuplicateGrants collapses grants naming one subject over one share,
// subpath and reach onto the earliest of them. Step 13's unique index cannot
// be built while any of them stand.
//
// A repair rather than a refusal, because the duplicates are this server's own
// doing: nothing stopped them until that step, so refusing to start would
// punish an operator for a bug they had no way to avoid.
//
// Nobody's access changes. The evaluator answers from every grant that matches
// at once, so the union of the allows and the union of the denies over rows
// that already agree on subject, path and reach is the decision those rows
// were producing together. Reach is part of the group for that reason: a grant
// over one folder and a grant over its whole subtree are different grants, and
// merging them would widen the first one.
//
// The earliest id survives because it is the grant the operator made first,
// and the label follows the first row that carries one.
func foldDuplicateGrants(ctx context.Context, tx *sql.Tx) error {
	folds, err := duplicateGrantFolds(ctx, tx)
	if err != nil {
		return err
	}
	for _, f := range folds {
		if _, uerr := tx.ExecContext(ctx, sqlUpdateGrant,
			f.allow, f.deny, f.inherit, labelArg(f.label), f.keep); uerr != nil {
			return fmt.Errorf("folding onto grant %d: %w", f.keep, uerr)
		}
		for _, id := range f.drop {
			if _, derr := tx.ExecContext(ctx, sqlDeleteGrant, id); derr != nil {
				return fmt.Errorf("removing grant %d, folded onto %d: %w", id, f.keep, derr)
			}
		}
	}
	return nil
}

// grantFold is one group of duplicates reduced to the row that will stand and
// the rows that will go.
type grantFold struct {
	keep    int64
	allow   int64
	deny    int64
	inherit int64
	label   string
	drop    []int64
}

// duplicateGrantFolds reads the duplicate rows and reduces each group.
//
// The reduction happens here rather than in the statement because SQLite has
// no aggregate for a bitwise union, and spelling one out bit by bit would put
// the permission model's width into a schema step where a new bit would be
// forgotten.
func duplicateGrantFolds(ctx context.Context, tx *sql.Tx) (out []grantFold, err error) {
	rows, err := tx.QueryContext(ctx, sqlDuplicateGrantRows)
	if err != nil {
		return nil, fmt.Errorf("reading duplicate grants: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	type key struct {
		user, group, share int64
		subpath            string
		inherit            int64
	}
	var at key
	for rows.Next() {
		var (
			id, allow, deny int64
			label           sql.NullString
			k               key
		)
		if serr := rows.Scan(&id, &allow, &deny, &label,
			&k.user, &k.group, &k.share, &k.subpath, &k.inherit); serr != nil {
			return nil, fmt.Errorf("reading a duplicate grant: %w", serr)
		}
		// The statement orders by the group and then by id, so a new key is a
		// new group and the first row of one is the grant that survives it.
		if len(out) == 0 || k != at {
			at = k
			out = append(out, grantFold{
				keep: id, allow: allow, deny: deny, inherit: k.inherit, label: label.String,
			})
			continue
		}
		f := &out[len(out)-1]
		f.allow |= allow
		f.deny |= deny
		if f.label == "" {
			f.label = label.String
		}
		f.drop = append(f.drop, id)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("reading duplicate grants: %w", rerr)
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
