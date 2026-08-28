package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

const (
	testIssuer   = "https://idp.example.test"
	testClientID = "stowcloud"
	testKid      = "key-1"
	testNonce    = "the-nonce-of-this-attempt"
)

// signer holds a key pair and mints tokens with it, so the verifier is tested
// against real signatures rather than a stub that reports what it was told.
type signer struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, rsaMinBits)
	if err != nil {
		t.Fatalf("generating an RSA key: %v", err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating an EC key: %v", err)
	}
	return &signer{rsaKey: rk, ecKey: ek}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *signer) rsaJWK() jwk {
	return jwk{
		Kty: "RSA", Kid: testKid, Use: "sig", Alg: algRS256,
		N: b64(s.rsaKey.N.Bytes()),
		E: b64(big.NewInt(int64(s.rsaKey.E)).Bytes()),
	}
}

func (s *signer) ecJWK() jwk {
	return jwk{
		Kty: "EC", Kid: testKid, Use: "sig", Alg: algES256, Crv: "P-256",
		X: b64(s.ecKey.X.FillBytes(make([]byte, 32))),
		Y: b64(s.ecKey.Y.FillBytes(make([]byte, 32))),
	}
}

// sign renders a token from a header and a claim set, signing whatever it is
// handed, so a test can mint the malformed shapes a provider never would.
func (s *signer) sign(t *testing.T, header map[string]any, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("encoding the header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encoding the claims: %v", err)
	}
	input := b64(headerJSON) + "." + b64(claimsJSON)
	sum := sha256.Sum256([]byte(input))

	var sig []byte
	switch header["alg"] {
	case algES256:
		r, sv, serr := ecdsa.Sign(rand.Reader, s.ecKey, sum[:])
		if serr != nil {
			t.Fatalf("signing: %v", serr)
		}
		sig = append(r.FillBytes(make([]byte, 32)), sv.FillBytes(make([]byte, 32))...)
	default:
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsaKey, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
	}
	return input + "." + b64(sig)
}

func claimsAt(clk clock.Clock) map[string]any {
	now := clk.Now().Unix()
	return map[string]any{
		"iss":   testIssuer,
		"sub":   "subject-123",
		"aud":   testClientID,
		"exp":   now + 300,
		"iat":   now,
		"nonce": testNonce,
	}
}

// verifier is a client whose key set is already warm, so a verification test
// makes no network call and tests the verifier rather than the transport.
func verifier(t *testing.T, keys []jwk, clk clock.Clock) *Client {
	t.Helper()
	c := &Client{
		cfg: Config{
			Issuer:       testIssuer,
			ClientID:     testClientID,
			ClientSecret: secret.New([]byte("a client secret")),
		},
		clk:       clk,
		discovery: newTTLCache[*Discovery](limits.OIDCDiscoveryTTL),
		jwks:      newTTLCache[[]jwk](limits.OIDCJWKSTTL),
		// Unreachable on purpose: a verification test that reached the network
		// would be testing the transport, and a refetch this fixture triggers
		// has to fail as the provider being unreachable rather than as a nil
		// dereference.
		http: &http.Client{Transport: unreachable{}},
	}
	c.jwks.store(keys, clk)
	return c
}

// unreachable is a transport that answers nothing, standing for a provider
// this test never intends to reach.
type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("the provider is unreachable in this test")
}

func fixedClock() clock.Clock {
	return clock.Fixed(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
}

func TestAValidTokenVerifiesUnderBothAlgorithms(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)

	for _, tc := range []struct {
		alg string
		key jwk
	}{
		{algRS256, s.rsaJWK()},
		{algES256, s.ecJWK()},
	} {
		c := verifier(t, []jwk{tc.key}, clk)
		raw := s.sign(t, map[string]any{"alg": tc.alg, "kid": testKid}, claimsAt(clk))
		claims, err := c.VerifyIDToken(ctx, raw, testNonce)
		if err != nil {
			t.Fatalf("%s: VerifyIDToken: %v", tc.alg, err)
		}
		if claims.Subject != "subject-123" || claims.Issuer != testIssuer {
			t.Fatalf("%s: the claims are %+v", tc.alg, claims)
		}
	}
}

// The header's algorithm selects nothing. Naming a symmetric algorithm or
// none at all matches no key, which closes confusion and the unsigned token
// in one move.
func TestTheHeaderCannotChooseTheAlgorithm(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	for _, alg := range []string{"none", "HS256", "ES256", "RS512", ""} {
		raw := s.sign(t, map[string]any{"alg": alg, "kid": testKid}, claimsAt(clk))
		if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Fatalf("a token claiming %q returned %v", alg, err)
		}
	}
}

// A token that supplies the key that verifies it verifies itself, which is
// not a signature.
func TestAHeaderSupplyingItsOwnKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	for name, header := range map[string]map[string]any{
		"an embedded key":          {"alg": algRS256, "kid": testKid, "jwk": map[string]any{"kty": "RSA"}},
		"an embedded chain":        {"alg": algRS256, "kid": testKid, "x5c": []string{"MIIB"}},
		"a pointer to a key set":   {"alg": algRS256, "kid": testKid, "jku": "https://attacker.test/keys"},
		"a pointer to a chain":     {"alg": algRS256, "kid": testKid, "x5u": "https://attacker.test/chain"},
		"a critical extension":     {"alg": algRS256, "kid": testKid, "crit": []string{"exp"}},
		"no key identifier at all": {"alg": algRS256},
	} {
		raw := s.sign(t, header, claimsAt(clk))
		if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
}

// A signature that verifies under a key an attacker can factor is a signature
// an attacker can make.
func TestASmallRSAKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()

	// Deliberately undersized: the refusal is what is under test, and it
	// cannot be proved with a key the rule accepts.
	small, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the weak key is the fixture.
	if err != nil {
		t.Fatalf("generating a small key: %v", err)
	}
	s := &signer{rsaKey: small}
	c := verifier(t, []jwk{{
		Kty: "RSA", Kid: testKid, Use: "sig", Alg: algRS256,
		N: b64(small.N.Bytes()), E: b64(big.NewInt(int64(small.E)).Bytes()),
	}}, clk)

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claimsAt(clk))
	if _, verr := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(verr, ErrTokenVerify) {
		t.Fatalf("a 1024-bit key was accepted: %v", verr)
	}
}

// A point that is not on the curve is a key an attacker chose, and verifying
// against one leaks the private key of whoever is tricked into using it.
func TestAnOffCurvePointIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)

	off := s.ecJWK()
	// One coordinate moved by one, which almost certainly leaves the curve.
	bad := s.ecKey.X.Bytes()
	bad[len(bad)-1] ^= 1
	off.X = b64(new(big.Int).SetBytes(bad).FillBytes(make([]byte, 32)))

	c := verifier(t, []jwk{off}, clk)
	raw := s.sign(t, map[string]any{"alg": algES256, "kid": testKid}, claimsAt(clk))
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an off-curve point was accepted: %v", err)
	}
}

// A key published for encryption is not a key to verify with, and a key that
// names an algorithm the token disagrees with is one of the two being wrong.
func TestAKeyThatDisagreesWithTheTokenIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)

	encryption := s.rsaJWK()
	encryption.Use = "enc"
	mismatched := s.rsaJWK()
	mismatched.Alg = algES256

	for name, key := range map[string]jwk{
		"a key published for encryption": encryption,
		"a key for another algorithm":    mismatched,
	} {
		c := verifier(t, []jwk{key}, clk)
		raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claimsAt(clk))
		if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
}

// Each claim check refuses on its own, so none of them is carrying the
// others.
func TestEveryClaimCheckRefusesOnItsOwn(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)
	now := clk.Now().Unix()

	skew := int64(limits.OIDCClockSkew.Seconds())
	for name, mutate := range map[string]func(map[string]any){
		"a different issuer": func(m map[string]any) { m["iss"] = "https://other.example.test" },
		"another client":     func(m map[string]any) { m["aud"] = "someone-else" },
		"no expiry":          func(m map[string]any) { delete(m, "exp") },
		"expired past skew":  func(m map[string]any) { m["exp"] = now - skew - 1 },
		"issued past skew":   func(m map[string]any) { m["iat"] = now + skew + 2 },
		"no subject":         func(m map[string]any) { delete(m, "sub") },
		"a different nonce":  func(m map[string]any) { m["nonce"] = "another attempt" },
		"no nonce at all":    func(m map[string]any) { delete(m, "nonce") },
	} {
		claims := claimsAt(clk)
		mutate(claims)
		raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
		if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
}

// The window is real on both sides: a token just inside the allowance is
// accepted, so the skew is a tolerance rather than a rounding accident.
func TestTheExpiryWindowIsSkewedOnBothSides(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)
	now := clk.Now().Unix()
	skew := int64(limits.OIDCClockSkew.Seconds())

	justInside := claimsAt(clk)
	justInside["exp"] = now - skew + 1
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, justInside)
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); err != nil {
		t.Fatalf("a token inside the allowance was refused: %v", err)
	}

	justOutside := claimsAt(clk)
	justOutside["exp"] = now - skew - 1
	raw = s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, justOutside)
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("a token outside the allowance was accepted: %v", err)
	}
}

// The specification allows the audience to be one string or a list, and both
// are read rather than one of them being guessed at.
func TestAnAudienceListIsAcceptedWhenItNamesThisClient(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	claims := claimsAt(clk)
	claims["aud"] = []string{"another-client", testClientID}
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); err != nil {
		t.Fatalf("an audience list naming this client was refused: %v", err)
	}

	claims["aud"] = []string{"another-client", "a third"}
	raw = s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an audience list without this client was accepted: %v", err)
	}
}

// The claims are only read after the signature verified: before that they are
// text an attacker chose. A tampered payload must not be reported by the
// claim it happens to carry.
func TestATamperedPayloadRefusesAtTheSignature(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claimsAt(clk))
	parts := strings.Split(raw, ".")
	tampered, err := json.Marshal(map[string]any{
		"iss": "https://attacker.example.test", "sub": "root", "aud": testClientID,
		"exp": clk.Now().Unix() + 300, "nonce": testNonce,
	})
	if err != nil {
		t.Fatalf("encoding the tampered payload: %v", err)
	}
	parts[1] = b64(tampered)

	_, verr := c.VerifyIDToken(ctx, strings.Join(parts, "."), testNonce)
	var refusal *VerifyError
	if !errors.As(verr, &refusal) {
		t.Fatalf("a tampered token returned %v", verr)
	}
	if !strings.Contains(refusal.Reason, "signature") {
		t.Fatalf("a tampered token was refused for %q, want the signature", refusal.Reason)
	}
}

// Two encodings of the same bytes means a signature over one input can be
// presented as a signature over another, and a fourth segment is an encrypted
// token rather than a signed one with extras.
func TestAMalformedTokenShapeIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)
	valid := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claimsAt(clk))

	for name, raw := range map[string]string{
		"no dots":            "onlyonesegment",
		"two segments":       "aGVhZGVy.cGF5bG9hZA",
		"four segments":      valid + ".extra",
		"an empty header":    "." + strings.SplitN(valid, ".", 2)[1],
		"an empty signature": strings.TrimSuffix(valid, strings.Split(valid, ".")[2]),
		"a padded segment":   strings.Replace(valid, ".", "==.", 1),
	} {
		if _, err := c.VerifyIDToken(ctx, raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
}

// A token past the bound is refused before it is parsed, so the bound limits
// what is allocated rather than reporting on what already was.
func TestAnOversizedTokenIsRefusedAtTheBound(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	huge := strings.Repeat("a", limits.OIDCTokenBytes+1)
	if _, err := c.VerifyIDToken(ctx, huge, testNonce); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("an oversized token returned %v", err)
	}
}

// A key the set does not carry is what a rotation looks like, so the set is
// refetched once rather than the token being refused outright.
func TestAnUnknownKeyIsRefusedWhenTheProviderDoesNotPublishIt(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)
	c := verifier(t, []jwk{s.rsaJWK()}, clk)

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": "a-key-nobody-published"}, claimsAt(clk))
	// The refetch fails because this client has no transport, and the refusal
	// is the provider's rather than a silent acceptance.
	if _, err := c.VerifyIDToken(ctx, raw, testNonce); err == nil {
		t.Fatal("a token naming an unknown key was accepted")
	}
}
