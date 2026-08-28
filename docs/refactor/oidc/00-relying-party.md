# OIDC: the relying party

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/oidc` is referenced as a behavioral specification only. The
> new implementation is written completely from scratch; nothing is
> copied.

## What this package is

The protocol half of single sign-on: discovery, JWKS fetching, the
authorize URL, the code exchange, and identity-token verification. It
talks to the internet and holds no state; the durable halves (flow rows,
identity links) are auth's (`../auth/03-oidc-integration.md`). The audit
found no defect in the old package; this rebuild is a behavioral
transcription whose job is to carry six verified security properties
forward as explicit requirements rather than happy accidents.

Target: `engine/service/oidc`. Dependencies: stdlib, `kit/clock`,
`kit/limits`, `kit/netzone` where the address classification overlaps.
No dependency on auth, core, or any protocol package. The token
verifier is dependency-free by design: a JOSE library is a parser for
attacker-controlled input, and this build verifies exactly the two
families it accepts.

This package holds the layer gate's one named `net/http` exception
(`../03-engine-bootstrap.md`). What it builds is an outbound client, never
a server, and the rule it is excepted from is about a package below the
presentation tier being able to answer a request. The gate names this
package specifically and still refuses every other service package.

## The six properties (normative)

1. **The SSRF guard runs twice.** The address guard refuses loopback,
   link-local, unique-local, unspecified, and the IPv4-in-IPv6
   mapped/embedded encodings of all of those. It runs at
   discovery-parse time against every endpoint URL, and **again at dial
   time** through `net.Dialer.Control`, because the gap between
   resolving a name and connecting to it is a TOCTOU an attacker's DNS
   controls. A deployment may opt in to private providers
   (`allowPrivate`), which relaxes the classification, never the
   double-check.
2. **The back channel follows no redirect.** A redirect on discovery,
   JWKS or the token endpoint is a refusal: the guard validated one URL,
   and a redirect is a different URL nobody validated.
3. **Bodies are bounded.** Every response reads through a limit reader
   at `limits.OIDCResponseBytes` with the plus-one so exactly-at-bound
   and over-bound stay distinguishable. Close errors join the return
   path rather than vanishing in a defer.
4. **The algorithm comes from the key, never the header.** Token
   verification derives the expected algorithm from the JWKS key's type
   and refuses a token whose header disagrees. This closes alg-confusion
   and `none` in one move. Embedded key material in the header (`jwk`,
   `x5c`, `jku`, `x5u`) is refused; `crit` is refused; RSA under 2048
   bits is refused; EC points are validated on-curve.
5. **Signature before claims.** Issuer (exact match), audience, expiry
   with skew, and the nonce (constant-time) are checked only after the
   signature verified. Claims from an unverified token are
   attacker-controlled text.
6. **A fetch failure is never cached.** The discovery and JWKS caches
   are TTL-bounded, mutex-guarded, and positive-only: caching a failure
   would turn a transient provider outage into an hour-long local one.

## Surface

```go
type Config struct {
    Issuer, ClientID, ClientSecret string
    Scopes                         []string
    CACertFile                     string // optional trust anchors
    AllowPrivate                   bool
}

func New(cfg Config, clk clock.Clock) (*Client, error)
func (c *Client) Settings() (issuer, clientID string, scopes []string, allowPrivate bool)

type FlowSecrets struct{ State, Nonce, Binding, Verifier string } // String/GoString redact
func NewFlowSecrets() (FlowSecrets, error)
func (f FlowSecrets) CodeChallenge() string // S256
func Hash(v string) [32]byte                // what auth stores digests with

func (c *Client) AuthorizeURL(ctx context.Context, redirectURI string, f FlowSecrets) (string, error)
func (c *Client) Exchange(ctx context.Context, code, redirectURI string, f FlowSecrets) (string, error)
func (c *Client) VerifyIDToken(ctx context.Context, raw, nonce string) (*Claims, error)
```

Errors: `ErrProvider` (wrapped by `ProviderError` with a reason),
`ErrNoTrustAnchors`, `ErrAddressBlocked` (wrapped by
`BlockedAddressError`), and `VerifyError` with a reason for token
refusals. None chooses a wire status.

Client authentication follows what discovery advertises
(`client_secret_basic` preferred, `client_secret_post` accepted); a
provider advertising neither is refused at discovery, not at exchange.
PKCE (S256) is always on.

## The one structural decision

**`LinkStore` and `ResolveIdentity` are dropped.** The audit (finding 5)
found the one-method interface has no non-test caller: auth's own flow
implements the equivalent directly. Two integration seams existed and
one was dead; the rebuild carries only the live one. The oidc package
returns verified `Claims`; the caller (the presentation layer's login
flow) hands `claims.Issuer` and `claims.Subject` to
`auth.UserForOIDCIdentity`. The dependency arrow stays
presentation -> {oidc, auth}, with no arrow between the two.

## Deliberate changes

1. **The dead seam is deleted** (above).
2. **The two caches become one implementation** used twice (audit
   finding 6): same TTL bound, same no-negative-caching rule, one code
   path to get right.
3. **The address classification reuses `kit/netzone`** where the ranges
   overlap; the OIDC-specific encodings (mapped/embedded v4-in-v6)
   stay here if netzone does not carry them.

## Tests

Written fresh, covering at least what the old suite proves:

- Every blocked range refuses, including the v4-in-v6 encodings; a
  public address passes; the guard refuses at the socket (dial-time
  test with a listener on loopback).
- A redirecting provider is refused on every back-channel endpoint.
- An over-bound body is refused; an exactly-at-bound body is accepted.
- Token vectors: a valid RS256 and ES256 token verify; an alg-header
  mismatch refuses; `none` refuses; embedded `jwk`/`x5c`/`jku`/`x5u`
  refuse; `crit` refuses; a small RSA key refuses; an off-curve EC
  point refuses.
- Claims: wrong issuer, wrong audience, expired-with-skew boundary, and
  a wrong nonce each refuse; the nonce comparison is constant-time (by
  construction; assert the code path uses the subtle compare).
- A signature failure refuses before any claim is examined (ordering
  observable via a claims struct that poisons on access, or by
  instrumenting the parse).
- Discovery: a repeated JSON key refuses; a provider advertising no
  usable signing algorithm or client-auth method refuses at discovery.
- The cache serves inside its TTL, refetches after, and never serves a
  cached failure.
- `FlowSecrets` redacts under `%v`, `%#v` and JSON.
