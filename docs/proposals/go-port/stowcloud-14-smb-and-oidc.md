# SMB and OIDC - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Two small subsystems with one thing in common: both generate or consume
something a hostile party can influence, and both enforce a network rule that has
to hold against the address actually used rather than against a string that was
checked earlier.

## 2. Background & Motivation

`sc-smb` plus `sc-smb-agent` is 3,121 lines and `sc-oidc` is 2,750.

**SMB** does not implement the protocol. It orchestrates a Samba sidecar:
generates `smb.conf`, maintains the password database, and enforces at
generation time that `smbd` binds only private-range interfaces. That last part
matters more than it looks: the sidecar shares the host's network stack, so an
empty interface list binds every private range it finds, which on that stack
includes the Docker bridges, and any container on the machine can then reach it.
The rule is enforced in the generated configuration, not documented as advice.

**OIDC** is link-only single sign-on. The security-relevant part is the
back-channel token exchange, which is the only outbound HTTP this server makes.
The Rust implementation wraps the DNS resolver so the private-address rule is
enforced on the addresses actually connected to, rather than on a separate
lookup a hostile IdP could rebind between. That is the detail a reimplementation
loses first.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] `smb.conf` generation with the private-range bind rule enforced in the
      output, not in the documentation.
- [ ] The passdb, with the NT hash decrypted only at the moment it is written.
- [ ] The uid contract that makes SMB and the web UI see the same ownership.
- [ ] OIDC discovery, the authorize URL, the back-channel exchange, RS256 and
      ES256 verification, with no JWT module.
- [ ] The private-address rule enforced on the connected address (DNS rebinding
      closed).
- [ ] An explicit trust-anchor pool with a refusal on an empty one (C5).
- [ ] Link-only: an OIDC identity attaches to an existing account and cannot
      create one.

### 3.2 Non-Goals

- [ ] Implementing SMB. Samba stays Samba.
- [ ] SMB turned on by default. It starts explicitly and that is principle 5.
- [ ] OIDC as the only authentication method, or OIDC-driven provisioning.
- [ ] Dynamic client registration, or any OAuth flow other than authorization
      code with PKCE.
- [ ] Kerberos or AD integration.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/smb
  render.go    smb.conf generation and the bind rule
  passdb.go    the password database
  uid.go       the uid and gid contract
  control.go   sidecar lifecycle signals
internal/oidc
  discovery.go the document, and validation of every endpoint in it
  authorize.go the URL, state, nonce, PKCE
  exchange.go  the back-channel POST
  jws.go       RS256 and ES256 verification against the JWKS
  dial.go      the address guard
  link.go      identity to account
```

### 4.2 Data Model Changes

`oidc_link` in `state.db`, mapping `(issuer, subject)` to a user. The NT hash
lives in `state.db` encrypted under the master key, as today. Nothing else.

### 4.3 SMB

#### 4.3.1 Generation is the enforcement point

The interface list in the generated `smb.conf` is computed, not copied. An
operator-supplied list is filtered to private ranges and a supplied address
outside them is a configuration refusal, naming the address. An empty list does
**not** mean "bind everything": it means the enumerated private interfaces
minus the container bridges, and if that set is empty the render refuses rather
than producing a file that binds broadly.

`bind interfaces only = yes` accompanies every interface list, because an
interface list without it is advice to `smbd` rather than a restriction.

The generated file is a trust boundary in the outbound direction (D20): every
value interpolated into it is validated and escaped for `smb.conf` syntax, and a
share name or path that cannot be represented safely is a refusal rather than an
escape attempt. A share name is not a place to discover that Samba's parser has
opinions.

#### 4.3.2 The passdb

`smbpasswd`-format entries written from the decrypted NT hash. The decryption
happens at the moment of writing and the plaintext hash is held in a
`secret.Secret` (D12) that is zeroed immediately after, which is the best
available given the residual risk recorded in
[`stowcloud-1`](stowcloud-1-defensive-standard.md) D12.

The file is written through the durable helper: correct mode before publish, and
the mode is `0600` because it is a credential store.

#### 4.3.3 The uid contract

The sidecar and the server must agree on ownership, or a file created over SMB
is unwritable from the web UI and vice versa. The contract is that both run as
the same uid and gid, that the share's configured mode is applied by both, and
that `force user` and `create mask` in the generated configuration reflect the
share policy rather than Samba's defaults.

This is stated as a contract with a test rather than as configuration advice:
the render is asserted to emit a mask consistent with the share's policy for
every policy value.

### 4.4 OIDC

#### 4.4.1 The address guard

The rule is that a token endpoint, a JWKS endpoint or a userinfo endpoint may
not resolve to a private, loopback, link-local or unspecified address, unless the
operator explicitly allowed it for a private IdP.

The rule must be enforced **on the address the connection actually uses**. A
check that resolves the hostname, validates the result, and then hands the
hostname to an HTTP client performs a second lookup, and a hostile IdP can
answer the two differently. The Rust implementation wraps the resolver for
exactly this reason.

In Go the hook is `net.Dialer.Control`:

```go
// Control runs after the address is resolved and before the socket connects,
// with the address that will actually be used. Rejecting here closes the
// rebinding gap that a resolve-then-check leaves open, because there is no
// second lookup between the check and the connection.
dialer := &net.Dialer{
    Control: func(network, address string, c syscall.RawConn) error {
        return guard.Allow(address)
    },
}
```

This is one of the few places where Go's standard library offers a cleaner
mechanism than the Rust tree had to build, and it is worth naming because the
temptation is to skip the hook and check the URL.

Redirects are not followed on the back channel. The token endpoint is where the
discovery document said it is, and a redirect is a refusal.

#### 4.4.2 Trust anchors

C5 from the folder README. `webpki-roots` was chosen in the Rust tree
specifically because a compiled-in anchor list does not depend on the runtime
image shipping a CA bundle, which `Cargo.toml` records the distroless image as
not guaranteeing.

Go's `crypto/x509.SystemCertPool` reads the image, so taking it unconditionally
drops that property. The client therefore takes an explicit `*x509.CertPool`:
built from the system pool by default, overridable with a configured PEM file,
and **a startup refusal when the resulting pool is empty**. Phase 11 confirms
what the shipped image carries; if it carries nothing, a PEM set is generated at
build time and embedded, which restores the Rust behaviour rather than
reinventing it.

#### 4.4.3 The discovery document

A trust boundary (D20). Every field is validated before use: the issuer must
match the configured one exactly, every endpoint must be absolute, `https`, and
on the same host as the issuer unless explicitly allowed otherwise, and the
supported algorithm list must include one this build verifies.

The document is size-capped, parsed with `encoding/json` into a struct with no
`interface{}` fields, and cached with a bounded lifetime.

#### 4.4.4 Verification

No JWT module. `crypto/rsa`, `crypto/ecdsa`, `crypto/sha256`, `encoding/json`
and `encoding/base64` verify a JWS directly, which is the same conclusion the
Rust tree reached with `ring`.

The checks, in order and all of them required: the algorithm is one this build
accepts and matches the key's type (never taken from the header alone), the
signature verifies against a JWKS key selected by `kid`, `iss` matches, `aud`
contains the client id, `exp` and `iat` are within the allowed skew, and `nonce`
matches the one this server generated.

`alg: none` and algorithm confusion are closed by the first check: the header's
algorithm selects nothing, it is compared against what the key can do.

#### 4.4.5 Link-only

An OIDC identity attaches to an existing account. It cannot create one, and it
cannot elevate one. A successful authentication for an unlinked identity is a
refusal that tells the user to link from inside their account, which is the
behaviour `docs/proposals/stowcloud-0-oidc-login.md` specifies.

## 5. API Design

### 5-1. New / Modified

```go
package smb

// Render produces smb.conf. It refuses rather than emitting a file that would
// bind a non-private interface, and it refuses rather than escaping a share
// name it cannot represent safely.
func Render(cfg Config, shares []Share) ([]byte, error)

package oidc

// Exchange performs the back-channel code exchange. Every connection it makes
// passes through the address guard at dial time, so a hostname that resolved
// acceptably cannot be re-resolved to something else between the check and the
// socket.
func (c *Client) Exchange(ctx context.Context, code string, v Verifier) (Tokens, error)

// VerifyIDToken checks signature, issuer, audience, expiry and nonce. The
// algorithm is chosen by the key, never by the token's header.
func (c *Client) VerifyIDToken(ctx context.Context, raw string, nonce string) (Claims, error)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 400 | a callback with a bad state, a missing code, or a mismatched nonce |
| 401 | the ID token failed verification |
| 403 | the identity is not linked to any account |
| 502 | the IdP was unreachable, answered malformed JSON, or redirected |
| 503 | SMB rendering failed, or the sidecar is not reachable |

| Error | Meaning |
|---|---|
| `ErrBindRefused` | the interface list would bind outside the private ranges |
| `ErrUnsafeValue` | a value cannot be represented in `smb.conf` safely |
| `ErrAddressBlocked` | the dial guard refused the resolved address |
| `ErrDiscovery` | the document failed validation, with the field named |
| `ErrTokenVerify` | signature, issuer, audience, expiry or nonce |
| `ErrNotLinked` | authenticated at the IdP, no account here |
| `ErrNoTrustAnchors` | startup, the certificate pool is empty |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 11a | `internal/smb`: render, the bind rule, the escaping, the mask test | M | Phase 5 | heavycaffeiner |
| Phase 11b | `passdb.go` and `uid.go` | S | 11a, Phase 3 | heavycaffeiner |
| Phase 11c | `internal/oidc`: `dial.go`, `discovery.go`, the anchor pool and its refusal | M | Phase 5 | heavycaffeiner |
| Phase 11d | `authorize.go`, `exchange.go`, `jws.go`, `link.go` | M | 11c, Phase 3 | heavycaffeiner |

11a and 11c are independent.

### 6-2. Dependencies

None. `net/http`, `crypto/tls`, `crypto/x509`, `crypto/rsa`, `crypto/ecdsa`,
`encoding/json` and `encoding/base64` cover the whole of OIDC, and SMB is text
generation.

This is the phase where the Go standard library replaces the most: the Rust tree
needed `hyper`, `hyper-util`, `hyper-rustls`, `rustls`, `webpki-roots`,
`tower-service`, `async-trait`, `ring` and `url` to make one outbound POST, and
the reason is recorded honestly in `Cargo.toml`: the workspace had no HTTP
client and no TLS stack at all before OIDC.

**Non-code dependency**: a real IdP to test against. Keycloak in the Linux VM,
plus one hosted provider, because discovery documents differ in ways only a real
one shows.

## 7. References

- `docs/proposals/stowcloud-1-smb.md`: the sidecar, the uid contract,
  propagation, what SMB cannot express.
- `docs/proposals/stowcloud-17-audit-gaps.md`: the SMB credential work, and the
  promises the current code does not keep.
- `docs/proposals/stowcloud-0-oidc-login.md`: link-only single sign-on.
- `Cargo.toml:51-86`: the outbound stack the Rust tree needed and why, including
  the resolver wrapper §4.4.1 replaces with `Dialer.Control`.
- `Cargo.toml:66-70`: the `webpki-roots` reasoning §4.4.2 preserves.
- `README.md`, the SMB section: the interfaces rule and what an empty list does.
- RFC 8414 (discovery), RFC 7636 (PKCE), RFC 7515 (JWS), RFC 7517 (JWK).
