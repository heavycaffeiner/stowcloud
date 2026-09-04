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

// GroupRow pairs a group with its membership.
type GroupRow struct {
	ID      int64
	Name    string
	Members []int64
}

// ListGroups yields all groups along with their members.
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
//
// The row is returned because the screen re-renders the group it just
// renamed, members and all. Answering nothing left the client with no row to
// swap in, and it reported the rename as failed while the name had changed.
func (s *Service) RenameGroup(ctx context.Context, id int64, name string) (GroupRow, error) {
	if err := s.store.RenameGroup(ctx, id, name); err != nil {
		return GroupRow{}, mapGroupErr(err)
	}

	// Read back rather than assembled here: the members are the group's and
	// this call did not touch them, so a row built from the argument alone
	// would render a renamed group as one with nobody in it.
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return GroupRow{}, err
	}
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return GroupRow{}, ErrNotFound
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

// GroupIDsOf yields the groups containing an account.
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
