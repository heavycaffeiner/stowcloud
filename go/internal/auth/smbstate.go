package auth

import (
	"context"
	"fmt"
)

// What the settings screen says about SMB for one account.
//
// It reports what actually works right now rather than which row exists: the
// deployment's second-factor policy is folded in here, because the two can
// disagree and a line reading "SMB uses the password you set" is something the
// user can only disprove by failing to connect.

// SMBCredential names what an account reaches SMB with.
//
// There is no "dedicated" value, and the absence is the schema's rather than
// an omission here: one row holds the NT hash whether it came from the account
// password or from a separate one, so the two cannot be told apart after the
// fact. Reporting "account" for both is the honest answer to the question the
// screen asks, which is whether SMB works at all.
type SMBCredential string

const (
	// SMBCredentialNone means nothing works over SMB for this account.
	SMBCredentialNone SMBCredential = "none"
	// SMBCredentialAccount means a stored credential does.
	SMBCredentialAccount SMBCredential = "account"
)

// SMBUnavailableReason says why nothing works, for the one case that is not
// obvious from the switches the screen already draws.
type SMBUnavailableReason string

const (
	// SMBUnavailableNotSet is an account with no stored credential.
	SMBUnavailableNotSet SMBUnavailableReason = "not_set"
	// SMBUnavailableTOTPBlocked is a second factor the policy refuses.
	SMBUnavailableTOTPBlocked SMBUnavailableReason = "totp_blocked"
	// SMBUnavailableOptedOut is the account's own withdrawal.
	SMBUnavailableOptedOut SMBUnavailableReason = "opted_out"
)

// SMBState is what one account's SMB access looks like now.
type SMBState struct {
	OptOut     bool
	Enabled    bool
	Credential SMBCredential
	// Reason is set only when Credential is none.
	Reason SMBUnavailableReason
}

// SMBStateOf reports how one account stands with the file-sharing protocol.
func (s *Service) SMBStateOf(ctx context.Context, userID int64) (SMBState, error) {
	var optOut, enabled, hasSecret, has2fa bool
	if err := s.st.SQL().QueryRowContext(ctx, sqlSMBState, userID).
		Scan(&optOut, &enabled, &hasSecret, &has2fa); err != nil {
		return SMBState{}, fmt.Errorf("reading the SMB state: %w", err)
	}

	out := SMBState{OptOut: optOut, Enabled: enabled, Credential: SMBCredentialNone}
	switch {
	case optOut:
		out.Reason = SMBUnavailableOptedOut
	case has2fa && s.smbTOTPPolicy == TOTPBlock:
		// The policy decides what is published, never what is stored, so this
		// is a state the deployment can move back without anyone setting a
		// password again.
		out.Reason = SMBUnavailableTOTPBlocked
	case !hasSecret:
		out.Reason = SMBUnavailableNotSet
	default:
		out.Credential = SMBCredentialAccount
	}
	return out, nil
}
