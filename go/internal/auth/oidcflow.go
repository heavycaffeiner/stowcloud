package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The state a link flow carries across the round trip to the provider.
//
// The state and the binding rest only as digests. Both go to the browser, so
// storing them whole would mean a read of this table is enough to complete
// somebody else's link, and what the callback checks is equality rather than
// the value.
//
// The nonce and the code verifier are stored whole, because both have to be
// handed back out: the verifier goes to the provider in the exchange, and the
// nonce goes to the token verifier, which checks it beside the issuer, the
// audience and the validity window. Neither authenticates anything on its own.

// ErrNoOIDCFlow means the state names no flow, or names one that expired.
//
// The two are one answer deliberately: telling a caller which would say whether
// a state value was ever real.
var ErrNoOIDCFlow = errors.New("auth: no such single-sign-on flow")

const (
	sqlInsertOIDCFlow = `
INSERT INTO oidc_flow(state_digest, user, nonce, binding_digest, code_verifier, redirect_uri, return_to, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	sqlTakeOIDCFlow = `
SELECT user, nonce, binding_digest, code_verifier, redirect_uri, return_to
FROM oidc_flow WHERE state_digest = ? AND created_ns >= ?`

	sqlDeleteOIDCFlow = `DELETE FROM oidc_flow WHERE state_digest = ?`

	sqlSweepOIDCFlows = `DELETE FROM oidc_flow WHERE created_ns < ?`
)

// OIDCFlow is one link in progress.
type OIDCFlow struct {
	User int64
	// Nonce is handed to the token verifier, which refuses a token that does
	// not carry it.
	Nonce        string
	CodeVerifier string
	RedirectURI  string
	ReturnTo     string
}

// StartOIDCFlow records a flow and returns nothing: what the caller needs is
// already in the secrets it passed in.
//
// Sweeping happens here rather than on a timer, because this is the only path
// that creates one, so a deployment nobody links on accumulates nothing.
func (s *Service) StartOIDCFlow(ctx context.Context, userID int64, state, nonce, binding, verifier, redirectURI, returnTo string) error {
	now := s.clk.Nanos()
	cutoff := now - int64(limits.OIDCFlowLifetime)

	stateD := sha256.Sum256([]byte(state))
	bindD := sha256.Sum256([]byte(binding))

	if err := s.write(ctx, func(tx *sql.Tx) error {
		if _, derr := tx.ExecContext(ctx, sqlSweepOIDCFlows, cutoff); derr != nil {
			return derr
		}
		_, ierr := tx.ExecContext(ctx, sqlInsertOIDCFlow,
			stateD[:], userID, nonce, bindD[:], verifier, redirectURI, returnTo, now)
		return ierr
	}); err != nil {
		return fmt.Errorf("recording the single-sign-on flow: %w", err)
	}
	return nil
}

// TakeOIDCFlow consumes a flow, checking the binding the browser presents.
//
// Consumed rather than read: a flow that can be redeemed twice is a code that
// can be replayed, and the exchange is the only thing it is for.
//
// The binding is a value held in a cookie rather than in the redirect, so a
// state value lifted out of a log or a referrer is not enough on its own.
func (s *Service) TakeOIDCFlow(ctx context.Context, state, binding string) (OIDCFlow, error) {
	stateD := sha256.Sum256([]byte(state))
	cutoff := s.clk.Nanos() - int64(limits.OIDCFlowLifetime)

	var (
		flow      OIDCFlow
		storedBnd []byte
	)
	row := s.st.SQL().QueryRowContext(ctx, sqlTakeOIDCFlow, stateD[:], cutoff)
	switch err := row.Scan(&flow.User, &flow.Nonce, &storedBnd,
		&flow.CodeVerifier, &flow.RedirectURI, &flow.ReturnTo); {
	case errors.Is(err, sql.ErrNoRows):
		return OIDCFlow{}, ErrNoOIDCFlow
	case err != nil:
		return OIDCFlow{}, fmt.Errorf("reading the single-sign-on flow: %w", err)
	}

	// Deleted whether or not the binding matches: a flow whose binding failed
	// is not one to leave available for another attempt.
	if derr := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteOIDCFlow, stateD[:])
		return err
	}); derr != nil {
		return OIDCFlow{}, fmt.Errorf("consuming the single-sign-on flow: %w", derr)
	}

	presented := sha256.Sum256([]byte(binding))
	if subtle.ConstantTimeCompare(storedBnd, presented[:]) != 1 {
		return OIDCFlow{}, ErrNoOIDCFlow
	}
	return flow, nil
}
