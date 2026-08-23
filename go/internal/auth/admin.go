package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// What the administrator's screens read and write.
//
// These live here for the same reason the self-service ones do: they touch
// credential rows, and a package that hands those out is a package every
// caller has to be careful in.

// UserRow is one account as an administrator sees it. It carries no hash: the
// admin screen never needs one, and a shape that could carry one is a shape
// somebody eventually fills in.
type UserRow struct {
	ID          int64
	Name        string
	Display     string
	IsAdmin     bool
	Disabled    bool
	TOTPEnabled bool
	SMBEnabled  bool
	CreatedNs   int64
	QuotaBytes  *int64
	UsageBytes  uint64
}

// UserByID returns one account.
//
// It reads the same row shape ListUsers does rather than a narrower one, so a
// surface that shows an account is looking at the same fields whether it asked
// for one or for all of them.
func (s *Service) UserByID(ctx context.Context, id int64) (UserRow, error) {
	rows, err := s.ListUsers(ctx)
	if err != nil {
		return UserRow{}, err
	}
	for _, u := range rows {
		if u.ID == id {
			return u, nil
		}
	}
	return UserRow{}, ErrCredentials
}

// ListUsers returns every account.
func (s *Service) ListUsers(ctx context.Context) (out []UserRow, err error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlListUsers)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			u     UserRow
			quota sql.NullInt64
			usage sql.NullInt64
			role  int64
		)
		if serr := rows.Scan(&u.ID, &u.Name, &u.Display, &u.Disabled, &role,
			&u.SMBEnabled, &u.CreatedNs, &quota, &usage, &u.TOTPEnabled); serr != nil {
			return nil, serr
		}
		u.IsAdmin = role == roleAdmin
		if quota.Valid {
			q := quota.Int64
			u.QuotaBytes = &q
		}
		if usage.Valid && usage.Int64 > 0 {
			u.UsageBytes = uint64(usage.Int64)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account and everything that belonged to it.
//
// The rows that reference it cascade, which is what makes this a deletion
// rather than an account nobody can sign into with its grants still standing.
func (s *Service) DeleteUser(ctx context.Context, userID int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, eerr := tx.ExecContext(ctx, sqlDeleteUser, userID)
		if eerr != nil {
			return eerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrCredentials
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	// The account's credential has to leave the published file too, or the
	// deleted account keeps working over the older protocol.
	return s.republishPassdb(ctx)
}

// SetQuota sets or clears an account's storage cap. A nil cap is unlimited.
func (s *Service) SetQuota(ctx context.Context, userID int64, bytes *int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var v any
		if bytes != nil {
			v = *bytes
		}
		_, err := tx.ExecContext(ctx, sqlSetQuota, v, userID)
		return err
	})
}

// ErrNameTaken is a name another row already holds.
//
// The database refuses it as a constraint, which is correct and unreadable: a
// constraint failure reaching a client as a server error tells whoever typed
// the name that something broke rather than that the name is taken.
var ErrNameTaken = errors.New("that name is already taken")

// GroupRow is one group with its members.
type GroupRow struct {
	ID      int64
	Name    string
	Members []int64
}

// ListGroups returns every group and who is in it.
func (s *Service) ListGroups(ctx context.Context) (out []GroupRow, err error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlListGroups)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	byID := map[int64]*GroupRow{}
	var order []int64
	for rows.Next() {
		var g GroupRow
		if serr := rows.Scan(&g.ID, &g.Name); serr != nil {
			return nil, serr
		}
		g.Members = []int64{}
		byID[g.ID] = &g
		order = append(order, g.ID)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}

	mrows, merr := s.st.SQL().QueryContext(ctx, sqlListMemberships)
	if merr != nil {
		return nil, merr
	}
	defer func() { err = errors.Join(err, mrows.Close()) }()
	for mrows.Next() {
		var user, group int64
		if serr := mrows.Scan(&user, &group); serr != nil {
			return nil, serr
		}
		if g, ok := byID[group]; ok {
			g.Members = append(g.Members, user)
		}
	}
	if rerr := mrows.Err(); rerr != nil {
		return nil, rerr
	}

	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// DeleteGroup removes a group. Its memberships and grants cascade with it.
func (s *Service) DeleteGroup(ctx context.Context, groupID int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, eerr := tx.ExecContext(ctx, sqlDeleteGroup, groupID)
		if eerr != nil {
			return eerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notifyMembership()
	return nil
}

// AddToGroup puts an account in a group. Adding twice is not an error: the
// caller asked for a state, and the state is reached either way.
func (s *Service) AddToGroup(ctx context.Context, userID, groupID int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, eerr := tx.ExecContext(ctx, sqlAddMembership, userID, groupID)
		return eerr
	})
	if err != nil {
		return err
	}
	s.notifyMembership()
	return nil
}

// RemoveFromGroup takes an account out of a group.
func (s *Service) RemoveFromGroup(ctx context.Context, userID, groupID int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, eerr := tx.ExecContext(ctx, sqlRemoveMembership, userID, groupID)
		return eerr
	})
	if err != nil {
		return err
	}
	s.notifyMembership()
	return nil
}

// notifyMembership tells whoever is holding an evaluator that the grants it
// resolved against have moved. Without it a group change is live in the
// database and stale in the process answering requests.
func (s *Service) notifyMembership() {
	s.bumpGeneration()
	if s.onMembership != nil {
		s.onMembership()
	}
}

// isUniqueViolation reports whether an error is the database refusing a
// duplicate value.
//
// Matched on the message because the driver reports it as one. That is
// fragile, and the alternative is worse: a constraint failure reaching a
// client as a server error tells whoever typed the name that the server broke.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
