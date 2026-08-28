package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/argon2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// Params is a full Argon2id parameter set. Raising the cost protects existing
// accounts only because a successful verification under older parameters
// rehashes; see Stale and Service.Verify.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	KeyLen      uint32
}

// CurrentParams is what this build hashes every new password under.
//
// It is a function rather than a variable so nothing importing this package
// can reassign it. The memory cost is 48 MiB rather than 64 because peak
// memory is that number times GateConcurrency, and a per-hash setting that
// lets four concurrent logins exhaust the container is not stronger in
// practice.
func CurrentParams() Params {
	return Params{MemoryKiB: 49152, Iterations: 3, Parallelism: 1, KeyLen: 32}
}

// GateConcurrency is how many Argon2 invocations may run at once. There is
// exactly one pool of permits and every hashing path shares it; a second,
// independent gate would silently double the real cap.
const GateConcurrency = 4

// saltLen is short enough to fit the encoded form and long enough that a
// collision across accounts is a ciphertext-level event.
const saltLen = 16

// maxKeyLen bounds what a stored hash may claim its derived key is, so a
// hostile row cannot ask for an allocation nobody chose.
const maxKeyLen = 64

// gate bounds concurrent Argon2 work with a fixed-size permit pool. A
// buffered channel is the whole primitive: send to acquire, receive to
// release, nothing to reference-count wrong.
type gate struct {
	permits chan struct{}

	// The two counters exist for the concurrency proof: they observe the peak
	// in-flight count, which is the one number showing the gate bounds peak
	// memory rather than being described as doing so.
	concurrent atomic.Int32
	highWater  atomic.Int32
}

func newGate() *gate {
	return &gate{permits: make(chan struct{}, GateConcurrency)}
}

// acquire blocks until a permit is free or ctx is done. A client that gives
// up stops waiting rather than queuing behind work it no longer wants, which
// is the same denial of service arriving from the cancellation direction.
//
// A context already done is refused before the permit is looked at, because a
// select with both cases ready picks either one, and a caller who has already
// gone away should never buy an invocation.
func (g *gate) acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.permits <- struct{}{}:
		g.raiseHighWater(g.concurrent.Add(1))
		return func() {
			g.concurrent.Add(-1)
			<-g.permits
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *gate) raiseHighWater(c int32) {
	for {
		hw := g.highWater.Load()
		if c <= hw || g.highWater.CompareAndSwap(hw, c) {
			return
		}
	}
}

// PeakConcurrency is the highest number of simultaneous invocations the gate
// has admitted, which is what the concurrency test asserts against.
func (s *Service) PeakConcurrency() int32 { return s.gate.highWater.Load() }

// Hash derives a fresh hash under CurrentParams, through the gate.
func (s *Service) Hash(ctx context.Context, pw secret.Secret) (string, error) {
	release, err := s.gate.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, saltLen)
	if _, rerr := rand.Read(salt); rerr != nil {
		return "", fmt.Errorf("salting the password: %w", rerr)
	}
	p := CurrentParams()
	key := argon2.IDKey(pw.Reveal(), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	defer zero(key)
	return encodePHC(p, salt, key), nil
}

// Verify checks pw against enc under the parameters the stored hash names, so
// a hash made with older costs still validates. stale reports that those
// parameters differ from the current ones, which is the caller's signal to
// rehash.
//
// A malformed stored hash answers false rather than an error: a corrupt row
// fails that login and nothing else. It also answers stale, so the row is
// replaced the moment its owner proves the password through another path.
func (s *Service) Verify(ctx context.Context, enc string, pw secret.Secret) (ok, stale bool, err error) {
	stored, valid := parsePHC(enc)
	if !valid {
		return false, true, nil
	}
	release, gerr := s.gate.acquire(ctx)
	if gerr != nil {
		return false, false, gerr
	}
	defer release()

	derived := argon2.IDKey(pw.Reveal(), stored.salt, stored.params.Iterations,
		stored.params.MemoryKiB, stored.params.Parallelism, stored.params.KeyLen)
	defer zero(derived)
	matches := len(derived) == len(stored.key) &&
		subtle.ConstantTimeCompare(derived, stored.key) == 1
	return matches, stored.params != CurrentParams(), nil
}

// Stale reports whether a stored hash's parameters differ from the current
// ones. It is the standalone half of Verify for a caller that only needs the
// rehash decision.
func Stale(enc string) bool {
	stored, valid := parsePHC(enc)
	if !valid {
		return true
	}
	return stored.params != CurrentParams()
}

// parsedPHC is one decoded password hash.
type parsedPHC struct {
	params Params
	salt   []byte
	key    []byte
}

// encodePHC renders the standard string form, which is what makes a stored
// hash self-describing: the parameters travel with it, so raising them later
// still verifies every password already on file.
func encodePHC(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// parsePHC decodes one. Every numeric field is bounded before it is narrowed,
// so a hostile stored hash cannot truncate an out-of-range cost into an
// allocation nobody chose.
func parsePHC(s string) (parsedPHC, bool) {
	fields := strings.Split(s, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, key]
	if len(fields) != 6 || fields[1] != "argon2id" || fields[2] != "v=19" {
		return parsedPHC{}, false
	}
	var p Params
	seen := map[string]bool{}
	for _, kv := range strings.Split(fields[3], ",") {
		name, val, found := strings.Cut(kv, "=")
		if !found || seen[name] {
			return parsedPHC{}, false
		}
		seen[name] = true
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return parsedPHC{}, false
		}
		switch name {
		case "m":
			v, nerr := num.Narrow[uint32](n)
			if nerr != nil {
				return parsedPHC{}, false
			}
			p.MemoryKiB = v
		case "t":
			v, nerr := num.Narrow[uint32](n)
			if nerr != nil {
				return parsedPHC{}, false
			}
			p.Iterations = v
		case "p":
			v, nerr := num.Narrow[uint8](n)
			if nerr != nil {
				return parsedPHC{}, false
			}
			p.Parallelism = v
		default:
			return parsedPHC{}, false
		}
	}
	if p.MemoryKiB == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return parsedPHC{}, false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(fields[4])
	if err != nil || len(salt) == 0 {
		return parsedPHC{}, false
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(fields[5])
	if err != nil || len(key) == 0 || len(key) > maxKeyLen {
		return parsedPHC{}, false
	}
	keyLen, nerr := num.Narrow[uint32](len(key))
	if nerr != nil {
		return parsedPHC{}, false
	}
	p.KeyLen = keyLen
	return parsedPHC{params: p, salt: salt, key: key}, true
}

// zero clears a derived key this package no longer needs. It cannot reach a
// copy the runtime made, which is the residual risk the secret package names.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
