package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// Params is a full Argon2id parameter set. Raising the cost protects existing
// accounts only when a successful login rehashes under the new one; see
// Stale and Service.Verify.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	KeyLen      uint32
}

// CurrentParams is what this build hashes every new password under. It is a
// function so the value cannot be reassigned by anything importing the
// package; memory cost × GateConcurrency is the real product S10 chose.
func CurrentParams() Params {
	return Params{MemoryKiB: 49152, Iterations: 3, Parallelism: 1, KeyLen: 32}
}

// saltLen is the Argon2 salt length. Short enough to fit the PHC string, long
// enough that a collision across accounts is a ciphertext-level event.
const saltLen = 16

// encodePHC renders the standard PHC form the Rust tree writes, so a hash it
// produced is readable here and a password never needs resetting on migrate.
func encodePHC(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// parsedPHC is one decoded password hash.
type parsedPHC struct {
	params Params
	salt   []byte
	key    []byte
}

// parsePHC decodes a PHC string. A malformed hash is a rejection, not an
// error, mirroring the Rust verifier's never-panic stance: a corrupt row must
// fail that login, not the process.
func parsePHC(s string) (parsedPHC, bool) {
	fields := strings.Split(s, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, key]
	if len(fields) != 6 || fields[1] != "argon2id" || fields[2] != "v=19" {
		return parsedPHC{}, false
	}
	var p Params
	for _, kv := range strings.Split(fields[3], ",") {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			return parsedPHC{}, false
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			return parsedPHC{}, false
		}
		switch name {
		case "m":
			if u, ok := phcU32(n); ok {
				p.MemoryKiB = u
			} else {
				return parsedPHC{}, false
			}
		case "t":
			if u, ok := phcU32(n); ok {
				p.Iterations = u
			} else {
				return parsedPHC{}, false
			}
		case "p":
			if u, ok := phcU8(n); ok {
				p.Parallelism = u
			} else {
				return parsedPHC{}, false
			}
		default:
			return parsedPHC{}, false
		}
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(fields[4])
	if err != nil {
		return parsedPHC{}, false
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(fields[5])
	if err != nil {
		return parsedPHC{}, false
	}
	if len(key) > 64 {
		return parsedPHC{}, false
	}
	p.KeyLen = uint32(len(key)) //nolint:gosec // key length is bound above at 64, well inside uint32.
	return parsedPHC{params: p, salt: salt, key: key}, true
}

// phcU32 bounds a parsed PHC number before narrowing it, so a corrupt or
// hostile stored hash cannot truncate an out-of-range cost into something
// that allocates unpredictably.
func phcU32(n int) (uint32, bool) {
	if n < 0 || uint64(n) > uint64(^uint32(0)) {
		return 0, false
	}
	return uint32(n), true //nolint:gosec // range is proven above.
}

func phcU8(n int) (uint8, bool) {
	if n < 0 || n > 255 {
		return 0, false
	}
	return uint8(n), true //nolint:gosec // range is proven above.
}

// Hash derives a fresh Argon2id hash under CurrentParams. It acquires the
// gate first: peak memory is memory cost times concurrent invocations, so an
// ungated call is a memory-exhaustion vector reachable by anyone who can
// submit a password, including account creation and TOTP enrolment.
func (s *Service) Hash(ctx context.Context, pw secret.Secret) (string, error) {
	release, err := s.gate.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salting the password: %w", err)
	}
	p := CurrentParams()
	key := argon2.IDKey(pw.Reveal(), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	defer zero(key)
	return encodePHC(p, salt, key), nil
}

// Verify checks pw against enc under the parameters the stored hash names
// (so a hash made with older costs still validates), comparing the derived
// key in constant time with respect to the password. stale is true when the
// stored parameters differ from CurrentParams, which is the caller's signal
// to rehash so that raising the cost protects existing accounts too. A
// malformed hash answers false rather than an error, so a corrupt row fails
// its login and nothing else.
func (s *Service) Verify(ctx context.Context, enc string, pw secret.Secret) (ok, stale bool, err error) {
	stored, valid := parsePHC(enc)
	if !valid {
		return false, true, nil
	}
	release, gerr := s.gate.Acquire(ctx)
	if gerr != nil {
		return false, false, gerr
	}
	defer release()

	cur := CurrentParams()
	derived := argon2.IDKey(pw.Reveal(), stored.salt,
		stored.params.Iterations, stored.params.MemoryKiB, stored.params.Parallelism, stored.params.KeyLen)
	defer zero(derived)
	matches := subtle.ConstantTimeCompare(derived, stored.key) == 1 &&
		len(derived) == len(stored.key)
	stale = stored.params != cur
	return matches, stale, nil
}

// Stale reports whether the stored hash's parameters differ from the current
// ones. It is the standalone half of Verify for a caller that only rehash
// decisions need.
func Stale(enc string) bool {
	stored, valid := parsePHC(enc)
	if !valid {
		return true
	}
	return stored.params != CurrentParams()
}

// zero clears a derived key this function no longer needs. It cannot reach a
// copy the runtime made; see the residual-risk note in the secret package.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
