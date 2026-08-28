package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The durable halves of single sign-on. The protocol half talks to the
// internet and holds no state; this holds state and talks to nobody.
//
// The position is link-only: the provider authenticates and never creates an
// account, so authority over who has one stays in this database. That is what
// makes a revocation here total.

// The refusals this surface answers with, re-exported from the store so a
// caller matches on this package's vocabulary rather than persistence's.
var (
	ErrNoOIDCFlow    = state.ErrNoOIDCFlow
	ErrNoOIDCLink    = state.ErrNoOIDCLink
	ErrOIDCLinkTaken = state.ErrOIDCLinkTaken
)

// OIDCFlow is one link in progress, as the caller needs it back.
type OIDCFlow struct {
	User int64
	// Nonce goes to the token verifier, which refuses a token not carrying
	// it.
	Nonce        string
	CodeVerifier string
	RedirectURI  string
	ReturnTo     string
}

// OIDCLink is one account's link.
type OIDCLink struct {
	Issuer   string
	Subject  string
	LinkedNs int64
	// LastLoginNs is nil for a link that exists and was never used to sign
	// in.
	LastLoginNs *int64
}

// StartOIDCFlow records a flow. What the caller needs afterwards is already
// in the values it passed in, so nothing comes back.
//
// The state and the browser binding are stored only as digests: both go to
// the browser, so keeping them whole would make a read of that table enough
// to complete somebody else's link, and what the callback checks is equality.
func (s *Service) StartOIDCFlow(
	ctx context.Context, userID int64, oidcState, nonce, binding, verifier, redirectURI, returnTo string,
) error {
	now := s.now()
	stateDigest := sha256.Sum256([]byte(oidcState))
	bindDigest := sha256.Sum256([]byte(binding))
	return s.store.StartOIDCFlow(ctx, state.NewOIDCFlow{
		StateDigest:   stateDigest[:],
		User:          userID,
		Nonce:         nonce,
		BindingDigest: bindDigest[:],
		CodeVerifier:  verifier,
		RedirectURI:   redirectURI,
		ReturnTo:      returnTo,
		CreatedNs:     now,
	}, now-int64(limits.OIDCFlowLifetime))
}

// TakeOIDCFlow consumes a flow and checks the binding the browser presents.
//
// The row is deleted whether or not the binding matches, because a flow whose
// binding failed is not one to leave redeemable for another attempt. The
// binding is held in a cookie, so a state value lifted from a log or a
// referrer is not enough on its own.
func (s *Service) TakeOIDCFlow(ctx context.Context, oidcState, binding string) (OIDCFlow, error) {
	stateDigest := sha256.Sum256([]byte(oidcState))
	row, err := s.store.TakeOIDCFlow(ctx, stateDigest[:], s.now()-int64(limits.OIDCFlowLifetime))
	if err != nil {
		return OIDCFlow{}, err
	}
	presented := sha256.Sum256([]byte(binding))
	if subtle.ConstantTimeCompare(row.BindingDigest, presented[:]) != 1 {
		return OIDCFlow{}, ErrNoOIDCFlow
	}
	return OIDCFlow{
		User:         row.User,
		Nonce:        row.Nonce,
		CodeVerifier: row.CodeVerifier,
		RedirectURI:  row.RedirectURI,
		ReturnTo:     row.ReturnTo,
	}, nil
}

// CreateOIDCLink attaches an identity to an account and disables local
// password login for it.
//
// The password credential goes because it is the factor being replaced:
// leaving it means the account still answers to the password the provider was
// just put in front of.
func (s *Service) CreateOIDCLink(ctx context.Context, userID int64, issuer, subject string) error {
	if err := s.store.CreateOIDCLink(ctx, userID, issuer, subject, s.now()); err != nil {
		return err
	}
	return s.LinkOIDC(ctx, userID)
}

// OIDCLinkOf reads an account's link.
func (s *Service) OIDCLinkOf(ctx context.Context, userID int64) (OIDCLink, error) {
	row, err := s.store.OIDCLinkOf(ctx, userID)
	if err != nil {
		return OIDCLink{}, err
	}
	return OIDCLink(row), nil
}

// UserForOIDCIdentity resolves a provider identity to the account it names.
func (s *Service) UserForOIDCIdentity(ctx context.Context, issuer, subject string) (int64, error) {
	return s.store.UserForOIDCIdentity(ctx, issuer, subject)
}

// RemoveOIDCLink detaches an identity and restores local password login,
// because removing the link alone would leave an account with no way in.
func (s *Service) RemoveOIDCLink(ctx context.Context, userID int64) error {
	if err := s.store.DeleteOIDCLink(ctx, userID); err != nil {
		return err
	}
	return s.UnlinkOIDC(ctx, userID)
}

// TouchOIDCLink records that an identity was just used to sign in.
func (s *Service) TouchOIDCLink(ctx context.Context, issuer, subject string) error {
	return s.store.TouchOIDCLink(ctx, issuer, subject, s.now())
}

// LinkOIDC turns local password login off for an account and republishes,
// because the account's file-sharing eligibility just changed.
func (s *Service) LinkOIDC(ctx context.Context, userID int64) error {
	return s.setSMBEnabled(ctx, userID, false)
}

// UnlinkOIDC turns it back on, the same way.
func (s *Service) UnlinkOIDC(ctx context.Context, userID int64) error {
	return s.setSMBEnabled(ctx, userID, true)
}

func (s *Service) setSMBEnabled(ctx context.Context, userID int64, enabled bool) error {
	if err := s.store.SetAccountSMBEnabled(ctx, userID, enabled); err != nil {
		if errors.Is(err, state.ErrNoSuchAccount) {
			return ErrCredentials
		}
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}
