package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is fixed by RFC 6238; it is not used as a collision-resistant hash.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The second factor: HMAC-SHA1 over a thirty-second counter with one step of
// drift either side, and a replay guard that records the steps it accepted,
// so a code captured in transit cannot be used again inside its window. The
// secret is sealed at rest.

const (
	totpStepSeconds = 30
	totpWindow      = 1
	totpDigits      = 6
	// totpSecretLen is 160 bits, which is what authenticator applications
	// expect and what the specification's reference implementation uses.
	totpSecretLen = 20
)

// totpEncoding is the unpadded Base32 an authenticator application expects.
// It is a function rather than a package variable so nothing can reassign the
// encoding every enrolment and verification depends on.
func totpEncoding() *base32.Encoding {
	return base32.StdEncoding.WithPadding(base32.NoPadding)
}

// GenerateTOTPSecret returns a fresh secret for a person to enrol.
func (s *Service) GenerateTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a second-factor secret: %w", err)
	}
	return totpEncoding().EncodeToString(raw), nil
}

// EnrollTOTP stores a sealed secret.
//
// The stored file-sharing credential is dropped in the same transaction, and
// the deployment's credential file is republished, because the policy that
// decides what is published may now exclude this account.
func (s *Service) EnrollTOTP(ctx context.Context, userID int64, secretB32 string) error {
	raw, err := totpEncoding().DecodeString(secretB32)
	if err != nil {
		return fmt.Errorf("the second-factor secret is not valid Base32: %w", err)
	}
	if len(raw) == 0 {
		return errors.New("the second-factor secret is empty")
	}
	key, ver, err := s.activeKey()
	if err != nil {
		return err
	}
	ct, err := sealTOTP(key, raw, userID, ver)
	if err != nil {
		return err
	}
	if err := s.store.EnrollTOTP(ctx,
		userID, state.TOTPSecret{Ciphertext: ct, KeyVer: ver}, s.now()); err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// DisableTOTP removes the factor and its replay window, and republishes.
func (s *Service) DisableTOTP(ctx context.Context, userID int64) error {
	if err := s.store.DisableTOTP(ctx, userID); err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// HasTOTP reports whether an account has enrolled a second factor.
func (s *Service) HasTOTP(ctx context.Context, userID int64) (bool, error) {
	acct, err := s.store.AccountByID(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return false, ErrCredentials
	}
	if err != nil {
		return false, err
	}
	return acct.TOTPEnrolled, nil
}

// VerifyTOTP checks a presented code against the enrolled secret, inside the
// drift window and against the replay guard.
//
// nowNs is passed in rather than read here, because the login path has
// already stamped its own moment and the two must agree.
func (s *Service) VerifyTOTP(ctx context.Context, userID int64, code string, nowNs int64) (bool, error) {
	if len(code) != totpDigits {
		return false, nil
	}
	stored, err := s.store.TOTPSecretOf(ctx, userID)
	if errors.Is(err, state.ErrNoTOTPSecret) {
		return false, ErrSecondFactor
	}
	if err != nil {
		return false, err
	}
	key, err := s.keyAt(stored.KeyVer)
	if err != nil {
		return false, err
	}
	secretBytes, err := openTOTP(key, stored.Ciphertext, userID, stored.KeyVer)
	if err != nil {
		return false, err
	}
	defer zero(secretBytes)

	nowStep := nowNs / (totpStepSeconds * 1e9)
	matched := int64(-1)
	for off := int64(-totpWindow); off <= totpWindow; off++ {
		step := nowStep + off
		if step < 0 {
			// A clock before the epoch is not a state to authenticate in,
			// and the counter has no representation for it.
			continue
		}
		want, cerr := totpAt(secretBytes, step)
		if cerr != nil {
			return false, cerr
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			matched = step
			break
		}
	}
	if matched < 0 {
		return false, nil
	}

	// The claim is what accepts the code: recording the step and refusing a
	// step already recorded happen in one transaction, so a code presented
	// twice inside its window is refused the second time even when the two
	// presentations race.
	claimed, err := s.store.ClaimTOTPStep(ctx, userID, matched, nowStep-totpWindow, s.now())
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// VerifyCandidateTOTP checks whether a candidate un-enrolled secret produces the
// presented code without altering the stored factor.
func (s *Service) VerifyCandidateTOTP(secretB32, code string, nowNs int64) (bool, error) {
	if len(code) != totpDigits {
		return false, nil
	}
	raw, err := totpEncoding().DecodeString(secretB32)
	if err != nil {
		return false, fmt.Errorf("the second-factor secret is not valid Base32: %w", err)
	}
	if len(raw) == 0 {
		return false, errors.New("the second-factor secret is empty")
	}
	defer zero(raw)

	nowStep := nowNs / (totpStepSeconds * 1e9)
	for off := int64(-totpWindow); off <= totpWindow; off++ {
		step := nowStep + off
		if step < 0 {
			continue
		}
		want, cerr := totpAt(raw, step)
		if cerr != nil {
			return false, cerr
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// totpAt computes the code for a secret at one step.
func totpAt(secretBytes []byte, step int64) (string, error) {
	counterValue, err := num.Narrow[uint64](step)
	if err != nil {
		return "", fmt.Errorf("a second-factor step of %d has no counter: %w", step, err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], counterValue)
	mac := hmac.New(sha1.New, secretBytes)
	if _, err := mac.Write(counter[:]); err != nil {
		return "", fmt.Errorf("computing a second-factor code: %w", err)
	}
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", val%1_000_000), nil
}
