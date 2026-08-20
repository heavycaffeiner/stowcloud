package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The header's algorithm selects nothing. It is compared against what the key
// can do, and the key decides. That one rule closes both the unsigned case and
// the confusion case.

const (
	testIssuer   = "https://idp.example.com"
	testClientID = "stowcloud"
	testKid      = "key-1"
	testNonce    = "the-nonce-of-this-attempt"
)

// testSigner holds a key pair and mints tokens with it.
type testSigner struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
}

func newSigner(t *testing.T) *testSigner {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, rsaMinBits)
	if err != nil {
		t.Fatalf("generating an RSA key: %v", err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating an EC key: %v", err)
	}
	return &testSigner{rsaKey: rk, ecKey: ek}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *testSigner) rsaJWK() jwk {
	return jwk{
		Kty: "RSA", Kid: testKid, Use: "sig", Alg: algRS256,
		N: b64(s.rsaKey.N.Bytes()),
		E: b64(big.NewInt(int64(s.rsaKey.E)).Bytes()),
	}
}

func (s *testSigner) ecJWK() jwk {
	return jwk{
		Kty: "EC", Kid: testKid, Use: "sig", Alg: algES256, Crv: "P-256",
		X: b64(s.ecKey.X.FillBytes(make([]byte, 32))),
		Y: b64(s.ecKey.Y.FillBytes(make([]byte, 32))),
	}
}

func defaultClaims(clk clock.Clock) map[string]any {
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

// sign builds a token with the given header and claims.
func (s *testSigner) sign(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshalling the header: %v", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshalling the claims: %v", err)
	}
	input := b64(hb) + "." + b64(cb)
	sum := sha256.Sum256([]byte(input))

	var sig []byte
	switch header["alg"] {
	case algRS256:
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsaKey, 0x05, sum[:])
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
	case algES256:
		r, ss, serr := ecdsa.Sign(rand.Reader, s.ecKey, sum[:])
		if serr != nil {
			t.Fatalf("signing: %v", serr)
		}
		sig = append(r.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)
	case "none":
		// Non-empty, deliberately. An unsigned token with an empty signature
		// is caught by the structural check, so an empty one here would test
		// the wrong rule and pass even with the algorithm check removed.
		sig = []byte("not-a-signature")
	default:
		// The confusion case: the token names a symmetric algorithm and the
		// attacker signs with the provider's public key as the shared secret,
		// which is a value they have.
		mac := hmac.New(sha256.New, s.rsaKey.N.Bytes())
		mac.Write([]byte(input)) //nolint:errcheck // hash.Write never fails.
		sig = mac.Sum(nil)
	}
	return input + "." + b64(sig)
}

// verifyClient builds a client whose key set is already populated, so no
// network is touched.
func verifyClient(t *testing.T, clk clock.Clock, keys []jwk) *Client {
	t.Helper()
	c, err := New(Config{
		Issuer:       testIssuer,
		ClientID:     testClientID,
		ClientSecret: secret.New([]byte("client-secret")),
	}, clk)
	if err != nil {
		t.Skipf("no usable certificate pool on this host: %v", err)
	}
	c.jwks.store(keys, clk)
	return c
}

func TestAValidTokenVerifiesUnderBothAlgorithms(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)

	for _, tc := range []struct {
		alg string
		key jwk
	}{
		{algRS256, s.rsaJWK()},
		{algES256, s.ecJWK()},
	} {
		c := verifyClient(t, clk, []jwk{tc.key})
		raw := s.sign(t, map[string]any{"alg": tc.alg, "kid": testKid}, defaultClaims(clk))

		claims, err := c.VerifyIDToken(context.Background(), raw, testNonce)
		if err != nil {
			t.Fatalf("%s: %v", tc.alg, err)
		}
		if claims.Subject != "subject-123" {
			t.Errorf("%s: subject = %q", tc.alg, claims.Subject)
		}
	}
}

// The unsigned case. The header's value selects nothing, so naming no
// algorithm matches no key.
func TestAnUnsignedTokenIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	for _, alg := range []string{"none", "None", "NONE", "nOnE"} {
		raw := s.sign(t, map[string]any{"alg": alg, "kid": testKid}, defaultClaims(clk))
		if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Errorf("a token claiming %q gave %v, want a refusal", alg, err)
		}
	}
	// And with an empty signature, which is the shape the structural check
	// catches. Both are refused, by different rules.
	if _, err := c.VerifyIDToken(context.Background(), "e30.e30.", testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an empty signature gave %v, want a refusal", err)
	}
}

// The confusion case. A token naming a symmetric algorithm signed with the
// provider's public key as the shared secret, which is a value the attacker
// has, verifies against any implementation that reads the algorithm from the
// header.
func TestAlgorithmConfusionIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	for _, alg := range []string{"HS256", "HS384", "HS512", "hs256", "RS512", "ES384", ""} {
		raw := s.sign(t, map[string]any{"alg": alg, "kid": testKid}, defaultClaims(clk))
		if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Errorf("a token claiming %q gave %v, want a refusal", alg, err)
		}
	}
}

// A token signed with the wrong algorithm for the named key is refused, which
// is the same rule from the other direction.
func TestAKeyOfTheWrongTypeIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)

	// An EC key named by a token claiming the RSA algorithm.
	c := verifyClient(t, clk, []jwk{s.ecJWK()})
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an EC key under the RSA algorithm gave %v, want a refusal", err)
	}

	// And the other way.
	c = verifyClient(t, clk, []jwk{s.rsaJWK()})
	raw = s.sign(t, map[string]any{"alg": algES256, "kid": testKid}, defaultClaims(clk))
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an RSA key under the EC algorithm gave %v, want a refusal", err)
	}
}

// A token that carries or points at the key verifying it verifies itself,
// which is not a signature.
func TestATokenSupplyingItsOwnKeyIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	own, err := json.Marshal(s.rsaJWK())
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	for _, header := range []map[string]any{
		{"alg": algRS256, "kid": testKid, "jwk": json.RawMessage(own)},
		{"alg": algRS256, "kid": testKid, "jku": "https://attacker.example/keys"},
		{"alg": algRS256, "kid": testKid, "x5u": "https://attacker.example/cert"},
		{"alg": algRS256, "kid": testKid, "x5c": []string{"MIIB"}},
	} {
		raw := s.sign(t, header, defaultClaims(clk))
		if _, verr := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(verr, ErrTokenVerify) {
			t.Errorf("a token supplying its own key gave %v, want a refusal", verr)
		}
	}
}

// An extension the token marks as not ignorable is one this build does not
// implement, so honouring the token means ignoring that requirement.
func TestACriticalExtensionIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	raw := s.sign(t, map[string]any{
		"alg": algRS256, "kid": testKid, "crit": []string{"exp-behaviour"},
	}, defaultClaims(clk))
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("a critical extension gave %v, want a refusal", err)
	}
}

// A tampered payload is a signature over different bytes.
func TestATamperedPayloadIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	parts := strings.Split(raw, ".")

	forged := defaultClaims(clk)
	forged["sub"] = "somebody-else"
	fb, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	tampered := parts[0] + "." + b64(fb) + "." + parts[2]

	if _, verr := c.VerifyIDToken(context.Background(), tampered, testNonce); !errors.Is(verr, ErrTokenVerify) {
		t.Fatalf("a tampered payload gave %v, want a refusal", verr)
	}
}

// Every claim check refuses on its own.
func TestEveryClaimCheckRefuses(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})
	now := clk.Now().Unix()

	for name, mutate := range map[string]func(map[string]any){
		// A document served from one issuer must not speak for another.
		"a different issuer": func(m map[string]any) { m["iss"] = "https://attacker.example" },
		// A prefix comparison would accept this.
		"an issuer with a suffix": func(m map[string]any) { m["iss"] = testIssuer + ".attacker.example" },
		// A valid token issued for another client of the same provider says
		// nothing about this server.
		"another client's audience": func(m map[string]any) { m["aud"] = "some-other-client" },
		"an audience list without us": func(m map[string]any) {
			m["aud"] = []string{"a", "b"}
		},
		"an expired token":  func(m map[string]any) { m["exp"] = now - 3600 },
		"no expiry at all":  func(m map[string]any) { delete(m, "exp") },
		"a future issuance": func(m map[string]any) { m["iat"] = now + 3600 },
		"no subject":        func(m map[string]any) { delete(m, "sub") },
		// Without this, a token obtained from any other attempt at the same
		// provider is accepted here.
		"another attempt's nonce": func(m map[string]any) { m["nonce"] = "a-different-attempt" },
		"no nonce":                func(m map[string]any) { delete(m, "nonce") },
	} {
		claims := defaultClaims(clk)
		mutate(claims)
		raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
		if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
			t.Errorf("%s gave %v, want a refusal", name, err)
		}
	}
}

// An audience list containing this client is accepted, because the
// specification allows either shape.
func TestAnAudienceListContainingThisClientIsAccepted(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	claims := defaultClaims(clk)
	claims["aud"] = []string{"another-client", testClientID}
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); err != nil {
		t.Fatalf("an audience list containing this client: %v", err)
	}
}

// The allowance covers ordinary drift between two machines and nothing more.
func TestTheSkewAllowanceIsWhatAdmitsAndRefuses(t *testing.T) {
	s := newSigner(t)
	base := time.Now()

	// A token that expired just inside the allowance is accepted.
	inside := clock.Fixed(base)
	c := verifyClient(t, inside, []jwk{s.rsaJWK()})
	claims := defaultClaims(inside)
	claims["exp"] = base.Add(-limits.OIDCClockSkew + time.Second).Unix()
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); err != nil {
		t.Fatalf("a token inside the allowance was refused: %v", err)
	}

	// And one just outside it is refused, so the allowance is what decides
	// rather than something else nearby.
	claims["exp"] = base.Add(-limits.OIDCClockSkew - 2*time.Second).Unix()
	raw = s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("a token outside the allowance gave %v, want a refusal", err)
	}
}

// A key an attacker can factor makes a signature an attacker can forge.
func TestAnUndersizedKeyIsRefused(t *testing.T) {
	clk := clock.System()
	small, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // G403 is the point: this key is deliberately undersized so the refusal can be proved.
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	weak := jwk{
		Kty: "RSA", Kid: testKid, Use: "sig",
		N: b64(small.N.Bytes()),
		E: b64(big.NewInt(int64(small.E)).Bytes()),
	}
	c := verifyClient(t, clk, []jwk{weak})

	s := &testSigner{rsaKey: small}
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	if _, verr := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(verr, ErrTokenVerify) {
		t.Fatalf("an undersized key gave %v, want a refusal", verr)
	}
}

// A point that is not on the curve is a key an attacker chose, and verifying
// against one leaks the private key of whoever is tricked into it.
func TestAKeyOffTheCurveIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)

	bad := s.ecJWK()
	// Move the y coordinate off the curve.
	y, err := base64.RawURLEncoding.DecodeString(bad.Y)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	y[31] ^= 0x01
	bad.Y = b64(y)

	c := verifyClient(t, clk, []jwk{bad})
	raw := s.sign(t, map[string]any{"alg": algES256, "kid": testKid}, defaultClaims(clk))
	if _, verr := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(verr, ErrTokenVerify) {
		t.Fatalf("a point off the curve gave %v, want a refusal", verr)
	}
}

// A key published for encryption is not a key to verify with.
func TestAKeyNotPublishedForSigningIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	k := s.rsaJWK()
	k.Use = "enc"
	c := verifyClient(t, clk, []jwk{k})

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an encryption key gave %v, want a refusal", err)
	}
}

// A key whose own algorithm disagrees with the token's is refused rather than
// used: one of the two is wrong, and guessing picks the attacker's answer half
// the time.
func TestAKeyDisagreeingWithTheTokenIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	k := s.rsaJWK()
	k.Alg = "RS512"
	c := verifyClient(t, clk, []jwk{k})

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("a disagreeing key gave %v, want a refusal", err)
	}
}

// A structurally malformed token is refused rather than parsed loosely.
func TestAMalformedTokenIsRefused(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})
	good := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, defaultClaims(clk))
	parts := strings.Split(good, ".")

	for name, raw := range map[string]string{
		"empty":              "",
		"one segment":        parts[0],
		"two segments":       parts[0] + "." + parts[1],
		"an empty header":    "." + parts[1] + "." + parts[2],
		"an empty signature": parts[0] + "." + parts[1] + ".",
		// A fourth segment is an encrypted token, which is a different
		// structure with a different verification rather than a signed one
		// with extra parts.
		"four segments":             good + "." + parts[2],
		"not base64":                "!!!.@@@.###",
		"no key named":              s.sign(t, map[string]any{"alg": algRS256}, defaultClaims(clk)),
		"an unknown key":            s.sign(t, map[string]any{"alg": algRS256, "kid": "no-such-key"}, defaultClaims(clk)),
		"a padded segment":          parts[0] + "==." + parts[1] + "." + parts[2],
		"a header that is not JSON": b64([]byte("not json")) + "." + parts[1] + "." + parts[2],
	} {
		if _, err := c.VerifyIDToken(context.Background(), raw, testNonce); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The token bound refuses, rather than something else nearby.
func TestTheTokenBoundIsWhatRefuses(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	oversized := strings.Repeat("a", limits.OIDCTokenBytes+1)
	_, err := c.VerifyIDToken(context.Background(), oversized, testNonce)
	if !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("an oversized token gave %v, want the bound", err)
	}
}

// This server having started no attempt means there is nothing to compare
// against, which is a refusal rather than a comparison that trivially passes.
func TestNoAttemptIsARefusal(t *testing.T) {
	clk := clock.System()
	s := newSigner(t)
	c := verifyClient(t, clk, []jwk{s.rsaJWK()})

	claims := defaultClaims(clk)
	delete(claims, "nonce")
	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": testKid}, claims)
	if _, err := c.VerifyIDToken(context.Background(), raw, ""); !errors.Is(err, ErrTokenVerify) {
		t.Fatalf("an empty expected nonce gave %v, want a refusal", err)
	}
}

// The verifier reads a token a stranger's browser delivered, so it is fuzzed.
// Nothing may panic, and nothing may verify against a key set it was not
// signed with.
func FuzzVerifyIDToken(f *testing.F) {
	f.Add("a.b.c")
	f.Add("")
	f.Add(".")
	f.Add(strings.Repeat("a", 200) + "." + strings.Repeat("b", 200) + ".")

	clk := clock.Fixed(time.Unix(1_700_000_000, 0))
	c, err := New(Config{
		Issuer:       testIssuer,
		ClientID:     testClientID,
		ClientSecret: secret.New([]byte("s")),
	}, clk)
	if err != nil {
		f.Skipf("no usable certificate pool on this host: %v", err)
	}
	// A key set with one key nothing in the corpus was signed with.
	rk, err := rsa.GenerateKey(rand.Reader, rsaMinBits)
	if err != nil {
		f.Fatalf("generating: %v", err)
	}
	c.jwks.store([]jwk{{
		Kty: "RSA", Kid: testKid, Use: "sig",
		N: b64(rk.N.Bytes()),
		E: b64(big.NewInt(int64(rk.E)).Bytes()),
	}}, clk)

	f.Fuzz(func(t *testing.T, raw string) {
		claims, err := c.VerifyIDToken(context.Background(), raw, testNonce)
		if err != nil {
			return
		}
		// Anything that verified had to have been signed by the key above,
		// which the corpus has no private half of, so this is unreachable.
		t.Fatalf("a fuzzed token verified: %+v", claims)
	})
}
