package auth

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Groups hold memberships and no permission knowledge; a grant may name a
// group instead of an account. There is exactly one crossing into the live
// permission engine, and it is the callback this package was wired with:
// without it a membership change is live in the database and stale in the
// process answering requests.

// GroupRow is one group with its members.
type GroupRow struct {
	ID      int64
	Name    string
	Members []int64
}

// ListGroups returns every group and who is in it.
func (s *Service) ListGroups(ctx context.Context) ([]GroupRow, error) {
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	memberships, err := s.store.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[int64][]int64, len(groups))
	for _, m := range memberships {
		byGroup[m.Group] = append(byGroup[m.Group], m.User)
	}
	out := make([]GroupRow, 0, len(groups))
	for _, g := range groups {
		members := byGroup[g.ID]
		if members == nil {
			// An empty list rather than a null one: a group with no members
			// is a group, and a client that renders a list should not have to
			// tell the two apart.
			members = []int64{}
		}
		out = append(out, GroupRow{ID: g.ID, Name: g.Name, Members: members})
	}
	return out, nil
}

// CreateGroup makes a group and returns its id.
func (s *Service) CreateGroup(ctx context.Context, name string) (int64, error) {
	id, err := s.store.CreateGroup(ctx, name)
	if err != nil {
		return 0, mapAccountErr(err)
	}
	return id, nil
}

// RenameGroup moves the label and touches no permission, which is the whole
// point of a grant naming an id rather than a name.
func (s *Service) RenameGroup(ctx context.Context, id int64, name string) error {
	if err := s.store.RenameGroup(ctx, id, name); err != nil {
		return mapGroupErr(err)
	}
	return nil
}

// DeleteGroup removes a group; its memberships and grants cascade with it.
func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	if err := s.store.DeleteGroup(ctx, id); err != nil {
		return mapGroupErr(err)
	}
	s.membershipChanged()
	return nil
}

// AddToGroup puts an account in a group.
func (s *Service) AddToGroup(ctx context.Context, userID, groupID int64) error {
	if err := s.store.AddMembership(ctx, userID, groupID); err != nil {
		return err
	}
	s.membershipChanged()
	return nil
}

// RemoveFromGroup takes an account out of one.
func (s *Service) RemoveFromGroup(ctx context.Context, userID, groupID int64) error {
	if err := s.store.RemoveMembership(ctx, userID, groupID); err != nil {
		return err
	}
	s.membershipChanged()
	return nil
}

// SetMembership replaces the whole set an account belongs to.
func (s *Service) SetMembership(ctx context.Context, userID int64, groupIDs []int64) error {
	if err := s.store.SetMemberships(ctx, userID, groupIDs); err != nil {
		return err
	}
	s.membershipChanged()
	return nil
}

// GroupIDsOf returns the groups an account belongs to.
func (s *Service) GroupIDsOf(ctx context.Context, userID int64) ([]int64, error) {
	return s.store.GroupIDsOf(ctx, userID)
}

// membershipChanged bumps the generation and tells whoever holds an
// evaluator that the grants it resolved against have moved.
func (s *Service) membershipChanged() {
	s.bumpGeneration()
	if s.onMembership != nil {
		s.onMembership()
	}
}

func mapGroupErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, state.ErrNoSuchGroup):
		return ErrNotFound
	case errors.Is(err, state.ErrNameTaken):
		return ErrNameTaken
	default:
		return err
	}
}
