package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is fixed by RFC 6238; it is not used as a collision-resistant hash.
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
)

// TOTP is HMAC-SHA1 over a 30-second counter with a one-step drift window
// either side and a replay guard that records the accepted steps, so a
// captured code cannot be reused inside its window. The secret is encrypted
// at rest under the master key.

// totpStepSeconds is the RFC 6238 time step.
const totpStepSeconds = 30

// totpWindow is how many steps either side of the current one are accepted.
const totpWindow = 1

// totpDigits is the length of the presented code.
const totpDigits = 6

// totpSecretLen is the size of a generated secret, 160 bits of entropy.
const totpSecretLen = 20

// GenerateTOTPSecret returns a fresh Base32 secret for a user to enrol in
// their authenticator app.
func (s *Service) GenerateTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// EnrollTOTP stores a sealed secret for an account. It is one of the six
// passdb paths: the SMB policy may block an account that now has a second
// factor, so the passdb is republished.
func (s *Service) EnrollTOTP(ctx context.Context, userID int64, secretB32 string) error {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		return fmt.Errorf("the TOTP secret is not valid Base32: %w", err)
	}
	active, ver := s.mk.Active()
	ct, err := sealTOTP(active, raw, userID, ver)
	if err != nil {
		return err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlUpsertTOTP, userID, ct, ver, s.now()); uerr != nil {
			return uerr
		}
		// Turning a second factor on drops the NT hash derived from the
		// account password. There is no point keeping a credential that can no
		// longer be used, and leaving it means the account password keeps
		// working over SMB, which is exactly the factor the user just added
		// being bypassed by the older protocol.
		_, derr := tx.ExecContext(ctx, sqlDeleteSMBSecret, userID)
		return derr
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}

// DisableTOTP removes the second factor and the replay window. The SMB policy
// unblocks the account, so the passdb is republished.
func (s *Service) DisableTOTP(ctx context.Context, userID int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteTOTP, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sqlDeleteTotpReplay, userID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}

// VerifyTOTP checks a presented code against the account's enrolled secret,
// within the drift window and the replay guard. It answers whether the code
// was accepted.
func (s *Service) VerifyTOTP(ctx context.Context, userID int64, code string, nowNs int64) (bool, error) {
	if len(code) != totpDigits {
		return false, ErrCredentials
	}
	accepted, err := s.verifyTOTP(ctx, userID, code, nowNs)
	if err != nil {
		if errors.Is(err, errTOTPMissing) {
			return false, ErrSecondFactor
		}
		return false, err
	}
	return accepted, nil
}

var errTOTPMissing = errors.New("no TOTP secret enrolled")

func (s *Service) verifyTOTP(ctx context.Context, userID int64, code string, nowNs int64) (bool, error) {
	var ct []byte
	var keyVer uint32
	err := s.st.SQL().QueryRowContext(ctx, sqlReadTOTP, userID).Scan(&ct, &keyVer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errTOTPMissing
	}
	if err != nil {
		return false, err
	}
	active, _ := s.mk.Active()
	secret, err := openTOTP(active, ct, userID, keyVer)
	if err != nil {
		return false, err
	}

	nowStep := nowNs / (totpStepSeconds * 1e9)
	// The set of steps already accepted, which a presented code inside the
	// window is refused against.
	used, err := s.totpUsedSteps(ctx, userID)
	if err != nil {
		return false, err
	}

	var matchedStep int64 = -1
	for off := int64(-totpWindow); off <= totpWindow; off++ {
		step := nowStep + off
		want := totpAt(secret, step)
		if subtle.ConstantTimeCompare([]byte(printCode(want)), []byte(code)) != 1 {
			continue
		}
		if used[step] {
			return false, ErrCredentials
		}
		matchedStep = step
		break
	}
	if matchedStep < 0 {
		return false, ErrCredentials
	}

	// Record the accepted step and prune anything the window has left behind,
	// in the same transaction, so a concurrent second use of the same code
	// finds nothing.
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlInsertTotpUsed, userID, matchedStep, s.now()); uerr != nil {
			return uerr
		}
		_, uerr := tx.ExecContext(ctx, sqlDeleteTotpUsed, userID, nowStep-totpWindow)
		return uerr
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) totpUsedSteps(ctx context.Context, userID int64) (out map[int64]bool, err error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlReadTotpUsed, userID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = map[int64]bool{}
	for rows.Next() {
		var step int64
		if serr := rows.Scan(&step); serr != nil {
			return nil, serr
		}
		out[step] = true
	}
	return out, rows.Err()
}

// totpAt computes the six-digit code for a secret at a given step.
func totpAt(secret []byte, step int64) uint32 {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step)) //nolint:gosec // a TOTP step is time-derived and never negative; the cast is the encoding.
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:]) //nolint:errcheck // hmac.Write never fails.
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := uint32(sum[off])&0x7f<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return val % 1_000_000
}

func printCode(c uint32) string {
	return fmt.Sprintf("%06d", c)
}
