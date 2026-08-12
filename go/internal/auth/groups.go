package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Groups hold memberships with no ACL knowledge; grants may name a group
// instead of a user. There is exactly one crossing into the live permission
// engine, and it is this package's one export on the subject: after a
// membership change lands, the callback wired via Config pushes it into the
// ACL engine. Two places writing membership would mean two places that
// disagree with what the evaluator currently believes.
func (s *Service) SetMembership(ctx context.Context, userID int64, groupIDs []int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteMemberships, userID); err != nil {
			return err
		}
		for _, g := range groupIDs {
			if _, err := tx.ExecContext(ctx, sqlInsertMembership, userID, g); err != nil {
				return fmt.Errorf("adding membership in group %d: %w", g, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	if s.onMembership != nil {
		s.onMembership()
	}
	return nil
}

// CreateGroup creates a group and returns its id.
func (s *Service) CreateGroup(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlInsertGroup, nil, name)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// GroupIDsOf returns the groups an account belongs to.
func (s *Service) GroupIDsOf(ctx context.Context, userID int64) (out []int64, err error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlMembershipsOfUser, userID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var g int64
		if serr := rows.Scan(&g); serr != nil {
			return nil, serr
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
