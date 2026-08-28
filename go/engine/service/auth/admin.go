package auth

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// What the administrative screens read and write, and the neutral account
// projections every protocol asks for.
//
// They live here for the same reason the self-service ones do: they touch
// credential rows, and a package that hands those out is a package every
// caller has to be careful in.

// UserRow is one account as an administrator sees it. It carries no hash: the
// screen never needs one, and a shape that could carry one is a shape
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

func userRowOf(a state.Account) UserRow {
	return UserRow{
		ID:          a.ID,
		Name:        a.Name,
		Display:     a.Display,
		IsAdmin:     a.IsAdmin(),
		Disabled:    a.Disabled,
		TOTPEnabled: a.TOTPEnrolled,
		SMBEnabled:  a.SMBEnabled,
		CreatedNs:   a.CreatedNs,
		QuotaBytes:  a.QuotaBytes,
		UsageBytes:  a.UsageBytes,
	}
}

// UserByID returns one account, in the same shape as the listing, so a screen
// showing one account sees the same fields whether it asked for one or all.
func (s *Service) UserByID(ctx context.Context, id int64) (UserRow, error) {
	acct, err := s.store.AccountByID(ctx, id)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return UserRow{}, ErrCredentials
	}
	if err != nil {
		return UserRow{}, err
	}
	return userRowOf(acct), nil
}

// ListUsers returns every account.
func (s *Service) ListUsers(ctx context.Context) ([]UserRow, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserRow, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, userRowOf(a))
	}
	return out, nil
}

// UserIDByName resolves a login name to an account id.
//
// It exists for the sign-in flow's second step, which has to name the account
// whose password was just accepted: the call that accepted it deliberately
// returns nothing about who it was refusing.
func (s *Service) UserIDByName(ctx context.Context, name string) (int64, error) {
	acct, err := s.store.AccountByName(ctx, name)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return 0, ErrCredentials
	}
	if err != nil {
		return 0, err
	}
	return acct.ID, nil
}

// NameOf is an account's login name, which the second-factor screen needs to
// label the entry an authenticator application stores.
func (s *Service) NameOf(ctx context.Context, userID int64) (string, error) {
	acct, err := s.store.AccountByID(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return "", ErrCredentials
	}
	if err != nil {
		return "", err
	}
	return acct.Name, nil
}

// IsAdmin reports whether an account holds the administrator role. The
// administrative surface reads it at the top of every route it guards.
func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	acct, err := s.store.AccountByID(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return false, ErrCredentials
	}
	if err != nil {
		return false, err
	}
	return acct.IsAdmin(), nil
}

// HasAdmin reports whether an administrator exists. It is the setup gate's
// read: the gate closes permanently the moment one does, so a token recovered
// from a log or a backup afterwards is worth nothing.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	return s.store.AdminExists(ctx)
}

// CountUsers is how many accounts exist.
func (s *Service) CountUsers(ctx context.Context) (int64, error) {
	return s.store.CountAccounts(ctx)
}

// SetQuota sets or clears an account's storage cap. A nil cap is unlimited,
// which is a different fact from a cap of zero: zero would leave the account
// unable to write anything, which is what disabling it is for.
func (s *Service) SetQuota(ctx context.Context, userID int64, bytes *int64) error {
	if bytes != nil && *bytes <= 0 {
		return ErrInvalidQuota
	}
	return s.store.SetAccountQuota(ctx, userID, bytes)
}

// AccountInfo is the neutral account projection every protocol renders from.
//
// It exists so a compatibility surface can answer "who is this account"
// without importing persistence or rebuilding visibility policy in a wire
// layer. The storage numbers a client compares an upload against are a
// storage projection and are combined above this package, not guessed here.
type AccountInfo struct {
	ID          int64
	LoginName   string
	DisplayName string
	Enabled     bool
	Groups      []string
	QuotaBytes  *int64
	UsageBytes  uint64
}

// AccountInfo reads one account's projection, with its group names resolved.
func (s *Service) AccountInfo(ctx context.Context, id int64) (AccountInfo, error) {
	acct, err := s.store.AccountByID(ctx, id)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return AccountInfo{}, ErrCredentials
	}
	if err != nil {
		return AccountInfo{}, err
	}
	groups, err := s.groupNamesOf(ctx, id)
	if err != nil {
		return AccountInfo{}, err
	}
	return AccountInfo{
		ID:          acct.ID,
		LoginName:   acct.Name,
		DisplayName: acct.Display,
		Enabled:     !acct.Disabled,
		Groups:      groups,
		QuotaBytes:  acct.QuotaBytes,
		UsageBytes:  acct.UsageBytes,
	}, nil
}

// AccountInfoByLogin resolves another account by name, applying visibility.
//
// Out of scope and absent are one answer, because telling them apart is a
// directory a stranger can walk. The caller's own account is always visible,
// and an administrator sees every account.
func (s *Service) AccountInfoByLogin(
	ctx context.Context, caller int64, login string,
) (AccountInfo, bool, error) {
	acct, err := s.store.AccountByName(ctx, login)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return AccountInfo{}, false, nil
	}
	if err != nil {
		return AccountInfo{}, false, err
	}
	visible, err := s.visibleTo(ctx, caller, acct.ID)
	if err != nil {
		return AccountInfo{}, false, err
	}
	if !visible {
		return AccountInfo{}, false, nil
	}
	info, err := s.AccountInfo(ctx, acct.ID)
	if err != nil {
		return AccountInfo{}, false, err
	}
	return info, true, nil
}

// visibleTo is the directory rule: yourself, anybody when you administer, and
// anybody you share a group with. Two accounts with no group in common are
// not in each other's directory.
func (s *Service) visibleTo(ctx context.Context, caller, target int64) (bool, error) {
	if caller == target {
		return true, nil
	}
	admin, err := s.IsAdmin(ctx, caller)
	if err != nil {
		return false, err
	}
	if admin {
		return true, nil
	}
	mine, err := s.store.GroupIDsOf(ctx, caller)
	if err != nil {
		return false, err
	}
	if len(mine) == 0 {
		return false, nil
	}
	theirs, err := s.store.GroupIDsOf(ctx, target)
	if err != nil {
		return false, err
	}
	in := make(map[int64]struct{}, len(mine))
	for _, g := range mine {
		in[g] = struct{}{}
	}
	for _, g := range theirs {
		if _, shared := in[g]; shared {
			return true, nil
		}
	}
	return false, nil
}

// ResolveAccount resolves a login name to an id. Absence is reported rather
// than raised: asking whether an account exists is an ordinary question for a
// caller that has already been authorized to ask it.
func (s *Service) ResolveAccount(ctx context.Context, login string) (int64, bool, error) {
	acct, err := s.store.AccountByName(ctx, login)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return acct.ID, true, nil
}

// ResolveGroup resolves a group name to an id, the same way.
func (s *Service) ResolveGroup(ctx context.Context, name string) (int64, bool, error) {
	return s.store.GroupByName(ctx, name)
}

// groupNamesOf is the account's groups, by name, which is what a protocol
// renders rather than the ids.
func (s *Service) groupNamesOf(ctx context.Context, userID int64) ([]string, error) {
	ids, err := s.store.GroupIDsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	nameOf := make(map[int64]string, len(groups))
	for _, g := range groups {
		nameOf[g.ID] = g.Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := nameOf[id]; ok {
			out = append(out, name)
		}
	}
	return out, nil
}
