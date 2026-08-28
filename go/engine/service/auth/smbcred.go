package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // MD4 is fixed by the SMB protocol; the value is sealed at rest and only that algorithm matches.

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The file-sharing credential and the sink every credential change calls.
//
// Deleting a row closes the protocol in the database and nowhere else: the
// daemon authenticates against the last file that was published, so a
// revocation stopping at the database leaves the sidecar serving the revoked
// credential. This is the one place in the product where a committed
// transaction is not yet a completed security decision.

// SMBBaseUid is the offset applied to an account's row id to yield the uid
// carried by its entry.
//
// The credential file and the adjacent account file must use the same value.
// The import tool matches an entry to an account via the uid, and when none
// matches it imports nothing whatsoever: no error, no log line, a zero exit
// status, an empty credential database, and every login rejected as an unknown
// account.
const SMBBaseUid = 30000

// TOTPPolicy governs what an account with a second factor may do over the
// file-sharing protocol.
//
// It controls publication only and never storage, so reverting it restores
// access without anyone setting a password again.
type TOTPPolicy uint8

const (
	// TOTPRequireSeparate is the default: an enrolled account reaches the
	// protocol with the dedicated password it was told to set.
	TOTPRequireSeparate TOTPPolicy = iota
	// TOTPBlock excludes every enrolled account, whatever credential it
	// holds.
	TOTPBlock
)

// SetSMBTOTPPolicy sets the policy, from the stored settings at startup. It
// is not moved per request.
func (s *Service) SetSMBTOTPPolicy(p TOTPPolicy) {
	s.policyMu.Lock()
	s.smbTOTPPolicy = p
	s.policyMu.Unlock()
}

func (s *Service) totpPolicy() TOTPPolicy {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.smbTOTPPolicy
}

// SMBCredentialKind identifies what an account uses to reach the protocol.
//
// No "dedicated" value exists, and that gap belongs to the schema rather than
// being an oversight: a single row stores the hash whether it originated from
// the account password or a separate one, leaving the two indistinguishable
// afterwards. Reporting "account" in both cases answers honestly the question
// the screen actually poses, which is whether the protocol works at all.
type SMBCredentialKind string

const (
	// SMBCredentialNone means nothing works over the protocol.
	SMBCredentialNone SMBCredentialKind = "none"
	// SMBCredentialAccount indicates a stored credential provides it.
	SMBCredentialAccount SMBCredentialKind = "account"
)

// SMBUnavailableReason says why nothing works, for the case that is not
// obvious from the switches a screen already draws.
type SMBUnavailableReason string

const (
	SMBUnavailableNotSet      SMBUnavailableReason = "not_set"
	SMBUnavailableTOTPBlocked SMBUnavailableReason = "totp_blocked"
	SMBUnavailableOptedOut    SMBUnavailableReason = "opted_out"
)

// SMBState is how one account stands with the protocol right now, with the
// deployment's policy folded in: a line reading "it uses the password you
// set" is something a person can only disprove by failing to connect.
type SMBState struct {
	OptOut     bool
	Enabled    bool
	Credential SMBCredentialKind
	// Reason carries a value only where Credential is none.
	Reason SMBUnavailableReason
}

// SMBStateOf reports that state for one account.
func (s *Service) SMBStateOf(ctx context.Context, userID int64) (SMBState, error) {
	acct, err := s.store.AccountByID(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return SMBState{}, ErrCredentials
	}
	if err != nil {
		return SMBState{}, err
	}
	hasSecret := true
	if _, serr := s.store.SMBSecretOf(ctx, userID); serr != nil {
		if !errors.Is(serr, state.ErrNoSMBSecret) {
			return SMBState{}, serr
		}
		hasSecret = false
	}

	out := SMBState{OptOut: acct.SMBOptOut, Enabled: acct.SMBEnabled, Credential: SMBCredentialNone}
	switch {
	case acct.SMBOptOut:
		out.Reason = SMBUnavailableOptedOut
	case acct.TOTPEnrolled && s.totpPolicy() == TOTPBlock:
		out.Reason = SMBUnavailableTOTPBlocked
	case !hasSecret:
		out.Reason = SMBUnavailableNotSet
	default:
		out.Credential = SMBCredentialAccount
	}
	return out, nil
}

// SetSMBPassword records a protocol credential distinct from the account
// password.
//
// Keeping them separate means the account password ceases to work over a
// protocol whose authentication cannot be strengthened to match, without denying
// the account access to that protocol.
func (s *Service) SetSMBPassword(ctx context.Context, userID int64, pw secret.Secret) error {
	if pw.Len() < MinPasswordLen {
		return ErrWeakPassword
	}
	sealed, err := s.sealNTFor(userID, pw)
	if err != nil {
		return err
	}
	if err := s.store.PutSMBSecretAndClearOptOut(ctx, userID, sealed); err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// ClearSMBPassword removes a separate password and reports whether the
// account password takes over.
//
// It does not for an account enrolled in a second factor under a blocking
// policy, linked to a provider, or opted out: for those the separate password
// was the only thing making the protocol work, and clearing it means losing
// access. Saying so beats reporting a success that reads as "nothing
// changed".
func (s *Service) ClearSMBPassword(ctx context.Context, userID int64) (revertible bool, err error) {
	facts, err := s.store.SMBRevertibleOf(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return false, ErrCredentials
	}
	if err != nil {
		return false, err
	}
	if derr := s.store.DeleteSMBSecret(ctx, userID); derr != nil {
		return false, derr
	}
	s.bumpGeneration()
	// Republished regardless: the credential no longer exists in the database,
	// and a rendered file still holding it amounts to a revoked password that
	// continues to work.
	if perr := s.republishCredentials(ctx); perr != nil {
		return false, perr
	}
	blocked := facts.TOTPEnrolled && s.totpPolicy() == TOTPBlock
	return !facts.OptOut && !facts.ProviderLink && !blocked, nil
}

// SetSMBAccess writes both self-service switches. Opting out is the stronger
// statement and forces the other off: a credential that is not stored cannot
// be live.
func (s *Service) SetSMBAccess(ctx context.Context, userID int64, optOut, enabled bool) error {
	if optOut {
		enabled = false
	}
	if err := s.store.SetAccountSMBAccess(ctx, userID, optOut, enabled); err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// SMBCredentials is every account eligible to appear in the credential file,
// as facts, with its hash opened.
//
// Eligibility is applied here because it is policy over facts, and the
// renderer that receives this list holds no opinion about it.
func (s *Service) SMBCredentials(ctx context.Context) ([]SMBCredential, error) {
	rows, err := s.store.PassdbRows(ctx)
	if err != nil {
		return nil, err
	}
	policy := s.totpPolicy()
	out := make([]SMBCredential, 0, len(rows))
	for _, r := range rows {
		if !publishable(r, policy) {
			continue
		}
		key, kerr := s.keyAt(r.Secret.KeyVer)
		if kerr != nil {
			// A hash under a key this ring no longer holds is skipped rather
			// than failing the render: one unopenable row must not keep every
			// other account's revocation from being published.
			s.warn("an SMB credential names a key version the ring does not hold", kerr)
			continue
		}
		nt, oerr := openNT(key, r.Secret.Ciphertext, r.User, r.Secret.KeyVer)
		if oerr != nil {
			s.warn("an SMB credential could not be opened and is not published", oerr)
			continue
		}
		uid, uerr := smbUID(r.User)
		if uerr != nil {
			return nil, uerr
		}
		out = append(out, SMBCredential{Name: r.Name, UID: uid, NTHash: nt})
	}
	return out, nil
}

// publishable is the eligibility rule: not opted out, not disabled, not
// blocked by the policy, and holding a credential at all. An ineligible
// account is left out entirely rather than written with a disabled marker,
// because a marker is a line the import tool still reads and the absence is
// what actually revokes.
func publishable(r state.PassdbRow, policy TOTPPolicy) bool {
	if r.Secret == nil || r.Disabled || !r.SMBEnabled {
		return false
	}
	return !r.TOTPEnrolled || policy != TOTPBlock
}

// smbUID converts a row id into the uid an entry carries by applying the offset.
//
// Row ids that do not fit are rejected rather than wrapped. A wrapped value
// would collide with some other account's uid, and the import would retain
// whichever of the two it encountered last.
func smbUID(rowID int64) (uint32, error) {
	const maxRowID = int64(^uint32(0)) - SMBBaseUid
	if rowID <= 0 || rowID > maxRowID {
		return 0, fmt.Errorf("account %d has no representable uid", rowID)
	}
	offset, err := num.Narrow[uint32](rowID)
	if err != nil {
		return 0, fmt.Errorf("account %d has no representable uid: %w", rowID, err)
	}
	return SMBBaseUid + offset, nil
}

// PublishPassdb renders the credential file now, without asking the sink to
// push. It is for the publisher, which is what does the pushing: calling the
// sink here would be the publisher asking this service for the file and this
// service asking the publisher to publish.
func (s *Service) PublishPassdb(ctx context.Context) error {
	return s.renderPassdbFile(ctx)
}

// republishCredentials is the sink every credential-changing path calls.
//
// It re-renders the whole file from the database and then tells the publisher,
// because the rendered file is not what the daemon authenticates against: the
// sidecar imports it, and a file written with nobody told is a revocation that
// lands whenever something else happens to publish.
//
// It never fails the write that called it. The account change has committed;
// reporting it as failed because a sidecar is down would be a change that
// happened reported as one that did not.
func (s *Service) republishCredentials(ctx context.Context) error {
	if err := s.renderPassdbFile(ctx); err != nil {
		s.warn("the SMB credential file could not be written", err)
	}
	if sink := s.accessSink(); sink != nil {
		sink.AccessChanged(ctx)
	}
	return nil
}

// renderPassdbFile writes the credential file, when this deployment has one.
func (s *Service) renderPassdbFile(ctx context.Context) error {
	if s.renderPassdb == nil || s.passdbPath == "" {
		return nil
	}
	creds, err := s.SMBCredentials(ctx)
	if err != nil {
		return err
	}
	b, err := s.renderPassdb(creds)
	if err != nil {
		return err
	}
	return fsatomic.ReplaceFileDurable(s.passdbPath, 0o600, func(f *os.File) error {
		_, werr := f.Write(b)
		return werr
	})
}

// sealNTFor derives the protocol's hash from a plaintext and seals it under
// the active key, bound to the account and that key's version.
func (s *Service) sealNTFor(userID int64, pw secret.Secret) (state.SMBSecret, error) {
	key, ver, err := s.activeKey()
	if err != nil {
		return state.SMBSecret{}, err
	}
	nt, err := ntHash(pw)
	if err != nil {
		return state.SMBSecret{}, err
	}
	ct, err := sealNT(key, nt, userID, ver)
	if err != nil {
		return state.SMBSecret{}, err
	}
	return state.SMBSecret{Ciphertext: ct, KeyVer: ver}, nil
}

// ntHash is MD4 of the UTF-16LE encoding of the password, which is the value
// the protocol derives its authentication from. The algorithm is fixed by the
// protocol; the mitigation is that the result is only ever sealed at rest and
// handed to the sidecar.
func ntHash(pw secret.Secret) ([ntHashLen]byte, error) {
	var out [ntHashLen]byte
	units := utf16.Encode([]rune(string(pw.Reveal())))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		// The encoding is the low byte then the high byte of each unit, which
		// is what the mask says rather than a conversion that could lose one.
		b = append(b, byte(u&0xff), byte((u>>8)&0xff))
	}
	defer zero(b)
	h := md4.New() //nolint:gosec // the protocol fixes the algorithm; see the import note.
	if _, err := h.Write(b); err != nil {
		return out, fmt.Errorf("deriving the SMB credential: %w", err)
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}
