package oidc

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// Token verification, with no JOSE library. A general parser for
// attacker-controlled input is a large surface for a job this build does in
// two algorithms.
//
// The header's algorithm selects nothing. It is compared against what the key
// can do, and the key decides. That single rule closes both the unsigned
// token and the confusion case where a token names a symmetric algorithm and
// the verifier hands it the provider's public key as the shared secret.

// ErrTokenVerify is a token that failed any of the checks.
var ErrTokenVerify = errors.New("the identity token did not verify")

// VerifyError names which check refused.
type VerifyError struct{ Reason string }

func (e *VerifyError) Error() string { return "the identity token did not verify: " + e.Reason }

func (e *VerifyError) Is(target error) bool { return target == ErrTokenVerify }

func refuse(reason string) error { return &VerifyError{Reason: reason} }

// The algorithms this build verifies. Each names a key type, which is what
// makes the key rather than the header decide.
const (
	algRS256 = "RS256"
	algES256 = "ES256"
)

// The sizes the two accepted algorithms fix.
const (
	// rsaMinBits is below what any provider uses and above what is breakable.
	rsaMinBits = 2048
	// ecdsaP256SigBytes is the two coordinates at the curve's fixed width.
	ecdsaP256SigBytes = 64
)

func supportedAlgs() []string { return []string{algRS256, algES256} }

func anySupported(offered []string) bool {
	for _, a := range offered {
		for _, ours := range supportedAlgs() {
			if a == ours {
				return true
			}
		}
	}
	return false
}

// jwk is one key from the provider's set.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// jwsHeader is the part of the header this reads. The algorithm is read only
// to be checked against the key, never to select one.
type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	// A key carried in the token itself is refused: a token that supplies the
	// key that verifies it verifies itself, which is not a signature.
	Jwk  json.RawMessage `json:"jwk"`
	Jku  string          `json:"jku"`
	X5u  string          `json:"x5u"`
	X5c  json.RawMessage `json:"x5c"`
	Crit []string        `json:"crit"`
}

// Claims is what a verified token asserts.
type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience audArg `json:"aud"`
	Expiry   int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	Nonce    string `json:"nonce"`

	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// audArg is the audience, which the specification allows to be one string or
// a list of them. Both shapes are read and neither is guessed at.
type audArg []string

func (a *audArg) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("aud is neither a string nor a list of them")
	}
	*a = many
	return nil
}

// VerifyIDToken checks the signature, the issuer, the audience, the validity
// window and the nonce.
//
// Every check is required and they run in this order, because a check that
// runs before the signature verified is a check on data nobody authenticated.
func (c *Client) VerifyIDToken(ctx context.Context, raw, nonce string) (*Claims, error) {
	if len(raw) > limits.OIDCTokenBytes {
		return nil, limits.Exceed("oidc id token", limits.OIDCTokenBytes, int64(len(raw)))
	}

	headerB64, payloadB64, sigB64, ok := splitJWS(raw)
	if !ok {
		return nil, refuse("it is not three base64url segments")
	}

	headerRaw, err := decodeSegment(headerB64)
	if err != nil {
		return nil, refuse("the header is not base64url")
	}
	var header jwsHeader
	if uerr := json.Unmarshal(headerRaw, &header); uerr != nil {
		return nil, refuse("the header is not JSON")
	}

	// A token that carries or points at the key verifying it verifies itself.
	if len(header.Jwk) > 0 || len(header.X5c) > 0 || header.Jku != "" || header.X5u != "" {
		return nil, refuse("the header supplies the key that would verify it")
	}
	// An extension the token declares as mandatory is one this build does not
	// implement, so honouring the token means ignoring a requirement its
	// issuer marked as not ignorable.
	if len(header.Crit) > 0 {
		return nil, refuse("the header declares a critical extension this build does not implement")
	}
	if header.Kid == "" {
		return nil, refuse("the header names no key")
	}

	key, err := c.keyFor(ctx, header.Kid)
	if err != nil {
		return nil, err
	}
	// The key decides the algorithm. The header's value is only compared
	// against it, so naming a symmetric algorithm or none at all selects
	// nothing and matches nothing.
	if merr := keyMatchesAlg(key, header.Alg); merr != nil {
		return nil, merr
	}

	sig, err := decodeSegment(sigB64)
	if err != nil {
		return nil, refuse("the signature is not base64url")
	}
	signingInput := raw[:len(headerB64)+1+len(payloadB64)]
	if verr := verifySignature(key, header.Alg, []byte(signingInput), sig); verr != nil {
		return nil, verr
	}

	// Only now is the payload read at all: before this line it is text an
	// attacker chose.
	payload, err := decodeSegment(payloadB64)
	if err != nil {
		return nil, refuse("the payload is not base64url")
	}
	var claims Claims
	if uerr := json.Unmarshal(payload, &claims); uerr != nil {
		return nil, refuse("the payload is not JSON")
	}
	if cerr := c.checkClaims(&claims, nonce); cerr != nil {
		return nil, cerr
	}
	return &claims, nil
}

// checkClaims applies every check that is not the signature.
func (c *Client) checkClaims(claims *Claims, nonce string) error {
	// Exact equality. A prefix or a suffix comparison lets one issuer speak
	// for another.
	if claims.Issuer != c.cfg.Issuer {
		return refuse("it names a different issuer than the configured one")
	}

	found := false
	for _, a := range claims.Audience {
		if a == c.cfg.ClientID {
			found = true
		}
	}
	if !found {
		// A token issued for another client of the same provider is a valid
		// token that says nothing about this server.
		return refuse("it was not issued for this client")
	}

	now := c.clk.Now().Unix()
	skew := int64(limits.OIDCClockSkew.Seconds())
	if claims.Expiry == 0 {
		return refuse("it never expires")
	}
	if now > claims.Expiry+skew {
		return refuse("it has expired")
	}
	// A token issued in the future is refused past the same allowance: a clock
	// that far ahead is either wrong or the token was minted to be replayed.
	if claims.IssuedAt != 0 && claims.IssuedAt-skew > now {
		return refuse("it was issued in the future")
	}

	// The nonce ties this token to the attempt this server started. Without
	// the check, a token obtained from any other attempt at the same provider
	// is accepted here.
	if nonce == "" {
		return refuse("this server started no attempt to compare a nonce against")
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return refuse("it does not carry the nonce of this attempt")
	}

	if claims.Subject == "" {
		return refuse("it names no subject")
	}
	return nil
}

// keyMatchesAlg checks the header's algorithm against what the key can do.
func keyMatchesAlg(k jwk, alg string) error {
	switch alg {
	case algRS256:
		if k.Kty != "RSA" {
			return refuse(fmt.Sprintf("%s needs an RSA key and the named one is %s", algRS256, k.Kty))
		}
	case algES256:
		if k.Kty != "EC" {
			return refuse(fmt.Sprintf("%s needs an EC key and the named one is %s", algES256, k.Kty))
		}
		if k.Crv != "P-256" {
			return refuse(fmt.Sprintf("%s needs the P-256 curve and the named key is %s", algES256, k.Crv))
		}
	default:
		return refuse(fmt.Sprintf("the algorithm %q is not one this build verifies", alg))
	}
	// A key that names its own algorithm and disagrees is refused rather than
	// used: one of the two is wrong, and guessing which picks the attacker's
	// answer half the time.
	if k.Alg != "" && k.Alg != alg {
		return refuse("the named key is for a different algorithm than the token claims")
	}
	// A key published for encryption is not a key to verify with.
	if k.Use != "" && k.Use != "sig" {
		return refuse("the named key is not published for signing")
	}
	return nil
}

// verifySignature checks the signature with the key.
func verifySignature(k jwk, alg string, signingInput, sig []byte) error {
	sum := sha256.Sum256(signingInput)

	switch alg {
	case algRS256:
		return verifyRS256(k, sum[:], sig)
	case algES256:
		return verifyES256(k, sum[:], sig)
	default:
		return refuse("the algorithm is not one this build verifies")
	}
}

func verifyRS256(k jwk, digest, sig []byte) error {
	n, err := decodeSegment(k.N)
	if err != nil {
		return refuse("the key's modulus is not base64url")
	}
	e, err := decodeSegment(k.E)
	if err != nil {
		return refuse("the key's exponent is not base64url")
	}
	if len(e) == 0 || len(e) > 8 {
		return refuse("the key's exponent is not a usable size")
	}
	exponent := new(big.Int).SetBytes(e)
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > int64(int(^uint(0)>>1)) {
		return refuse("the key's exponent is not a usable value")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}
	// A key below this size is refused rather than verified against: a
	// signature that verifies under a key an attacker can factor is a
	// signature an attacker can make.
	if pub.N.BitLen() < rsaMinBits {
		return refuse(fmt.Sprintf("the key is %d bits, below the %d this build accepts",
			pub.N.BitLen(), rsaMinBits))
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig); err != nil {
		return refuse("the signature does not verify")
	}
	return nil
}

func verifyES256(k jwk, digest, sig []byte) error {
	x, err := decodeSegment(k.X)
	if err != nil {
		return refuse("the key's x coordinate is not base64url")
	}
	y, err := decodeSegment(k.Y)
	if err != nil {
		return refuse("the key's y coordinate is not base64url")
	}
	// The signature is the two halves concatenated at a fixed width, not a
	// structured encoding. A structured one here would accept trailing bytes,
	// which is a second valid encoding of the same signature.
	if len(sig) != ecdsaP256SigBytes {
		return refuse("the signature is not the fixed width this curve uses")
	}
	const coordBytes = ecdsaP256SigBytes / 2
	if len(x) != coordBytes || len(y) != coordBytes {
		return refuse("the key's coordinates are not the fixed width this curve uses")
	}
	// The point is validated by parsing it in the uncompressed encoding, which
	// checks it is on the curve. A point that is not is a key an attacker
	// chose, and verifying against one leaks the private key of whoever is
	// tricked into using it.
	point := append([]byte{4}, append(x, y...)...)
	if _, perr := ecdh.P256().NewPublicKey(point); perr != nil {
		return refuse("the key's point is not on the curve")
	}
	pub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(x),
		Y:     new(big.Int).SetBytes(y),
	}
	r := new(big.Int).SetBytes(sig[:coordBytes])
	s := new(big.Int).SetBytes(sig[coordBytes:])
	if !ecdsa.Verify(pub, digest, r, s) {
		return refuse("the signature does not verify")
	}
	return nil
}

// splitJWS separates the three segments without allocating them.
func splitJWS(raw string) (header, payload, sig string, ok bool) {
	first := strings.IndexByte(raw, '.')
	if first < 0 {
		return "", "", "", false
	}
	rest := raw[first+1:]
	second := strings.IndexByte(rest, '.')
	if second < 0 {
		return "", "", "", false
	}
	sig = rest[second+1:]
	// A fourth segment means an encrypted token, which is a different
	// structure with a different verification, not a signed one with extras.
	if strings.IndexByte(sig, '.') >= 0 {
		return "", "", "", false
	}
	if first == 0 || second == 0 || sig == "" {
		return "", "", "", false
	}
	return raw[:first], rest[:second], sig, true
}

// decodeSegment decodes one segment, unpadded and strict.
//
// A padded or otherwise non-canonical segment is refused rather than
// accepted, because two encodings of the same bytes means a signature over
// one input can be presented as a signature over another.
func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.Strict().DecodeString(s)
}

// keyFor finds the named key, refetching the set once if it is not there.
func (c *Client) keyFor(ctx context.Context, kid string) (jwk, error) {
	if keys, ok := c.jwks.fresh(c.clk); ok {
		if k, found := findKid(keys, kid); found {
			return k, nil
		}
		// A key this server has not seen is what a rotation looks like, so the
		// set is refetched once. Without this a rotation is an outage lasting
		// as long as the cache does.
		c.jwks.forget()
	}

	fetched, err := c.fetchJWKS(ctx)
	if err != nil {
		return jwk{}, err
	}
	if k, found := findKid(fetched, kid); found {
		return k, nil
	}
	return jwk{}, refuse("it names a key the provider does not publish")
}

func findKid(keys []jwk, kid string) (jwk, bool) {
	for _, k := range keys {
		if k.Kid == kid {
			return k, true
		}
	}
	return jwk{}, false
}

// fetchJWKS reads the provider's key set.
func (c *Client) fetchJWKS(ctx context.Context) ([]jwk, error) {
	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, doc.JWKSURI)
	if err != nil {
		return nil, err
	}
	var set jwkSet
	if uerr := json.Unmarshal(body, &set); uerr != nil {
		return nil, &ProviderError{Reason: "the key set is not JSON"}
	}
	if len(set.Keys) == 0 {
		return nil, &ProviderError{Reason: "the key set is empty"}
	}
	if len(set.Keys) > limits.OIDCJWKSKeys {
		return nil, limits.Exceed("oidc jwks keys", limits.OIDCJWKSKeys, int64(len(set.Keys)))
	}
	c.jwks.store(set.Keys, c.clk)
	return set.Keys, nil
}
