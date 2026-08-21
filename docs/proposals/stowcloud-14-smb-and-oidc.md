# SMB and OIDC - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Implemented, with one exception** |
| Reviewers  |                                  |

---

> **The sidecar agent is not ported.** Everything in this document is
> implemented in Go except `sc-smb-agent`, the privileged half of SMB
> publishing, which is still the Rust program it always was and now lives in
> `smb-agent/`. It was in no phase's milestone list and the cutover kept it
> rather than shipping a broken feature or writing root-running code in the
> phase least able to test it. Recorded as Q10 in `OPEN-QUESTIONS.md`.

## 1. Summary

Two small subsystems with one thing in common: both generate or consume
something a hostile party can influence, and both enforce a network rule that has
to hold against the address actually used rather than against a string that was
checked earlier.

## 2. Background & Motivation

`sc-smb` plus `sc-smb-agent` is 3,121 lines and `sc-oidc` is 2,750.

**SMB** does not implement the protocol. It orchestrates a Samba sidecar:
generates `smb.conf`, maintains the password database, and decides at
generation time which interfaces `smbd` may bind. That last part matters more
than it looks. The sidecar shares the host's network stack, so a broadly
written interface list reaches everything on that stack, which includes the
Docker bridges, and any container on the machine can then reach the share.

The rule is therefore enforced in the generated file rather than documented as
advice, and §4.3.1 states what it computes: the networks the machine is
actually attached to, internal ones only unless the operator explicitly opts
in, with a loopback-only baseline so an unexpanded configuration is closed.

The stance that decides both is that a control is enforced where it cannot be
skipped: SMB's bind restriction lives in the generated file rather than in the
documentation, and OIDC's address rule lives in the dial rather than in a check
before the dial. A rule that a later step can route around is advice.

**OIDC** is link-only single sign-on, and "link-only" is a position rather than
a scope decision: the provider authenticates and never creates an account, so
authority stays in the local database. That is what makes revocation here total.
An identity provider that can provision accounts is an identity provider that
can grant access to this server, and the trust that would require is not the
trust an operator thinks they are extending when they configure single sign-on.

The security-relevant part is the
back-channel token exchange, which is the only outbound HTTP this server makes.
The Rust implementation wraps the DNS resolver so the private-address rule is
enforced on the addresses actually connected to, rather than on a separate
lookup a hostile IdP could rebind between. That is the detail a reimplementation
loses first.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] `smb.conf` generation with the bind rule enforced in the output rather
      than in the documentation, computed from the host's own interfaces, and
      closed by default when nothing expanded it.
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
empty operator list does **not** mean "bind everything", and a supplied address
the host is not actually attached to is a configuration refusal naming the
address.

**The set is derived from the host's own interfaces, and that is what the Rust
tree already does.** This is worth stating carefully, because an earlier draft
of this section described the current implementation as a hardcoded CIDR table
rendered on every host, and every clause of that was wrong. What is actually
there, and what the port carries over unchanged:

- The sidecar enumerates the host's own devices, classifies each address as
  internal or global, and skips devices that have only a link-local address.
- The core renders **loopback only** when `smb.interfaces` is unpinned, so a
  configuration nothing expanded is closed rather than open. That baseline is
  the default today, not a new idea.
- The private-CIDR list (nine entries) goes into **`hosts allow` only**, never
  into `interfaces`, and only when the operator pinned `smb.interfaces` or when
  the container's network namespace shows nothing but veth devices and there is
  no host address to classify.
- Global addresses stay behind an explicit `smb.allow_public_bind`, with its
  audit event and its admin banner.

The port's contribution here is not a redesign. It is that the two halves live
in one document with the fallback's conditions written down, because the thing
that is easy to get wrong is treating the CIDR list as the policy when it is the
fallback.

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

**Republishing is a revocation path, not a maintenance task.** `smbd`
authenticates against the last file published to it, so a credential revoked in
`state.db` and not republished here stays usable over SMB. The six paths that
must reach the sink are listed in [`6`](stowcloud-6-auth-and-acl.md) §4.3.8a,
and the property to test is the file's contents after each of them rather than
the database row. This is the one gap in the product where a completed
transaction is not a completed security decision.

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

C5 in the index. `webpki-roots` was chosen in the Rust tree
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
behaviour §2 gives the reason for: the provider authenticates, and authority
over who has an account stays here.

## 5. API Design

### 5-1. New / Modified

```go
package smb

// Render produces the loopback-only smb.conf baseline and the policy consumed
// by the sidecar. A pinned global interface without allow_public_bind is
// refused here; discovered host interfaces are classified and expanded by the
// sidecar in the namespace that actually binds them. Unsafe share names and
// paths are always refused.
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
| `ErrBindRefused` | a pinned interface is global without `smb.allow_public_bind`, or the pinned value is invalid. Attachment of discovered interfaces is decided by the sidecar, not by the core process |
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

- `crates/sc-smb/src/lib.rs`, `crates/sc-server/src/smb_cmd.rs`, `passdb.rs`,
  `crates/sc-smb-agent/`: the generation, the passdb and the sidecar loop this
  translates.
- `crates/sc-oidc/src/`: discovery, the exchange, the address guard and the JWS
  verification this translates, including the resolver wrapper §4.4.1 replaces
  with `Dialer.Control`.
- `docker-compose.yml`, `Dockerfile.smb`: the sidecar's deployment shape, which
  does not change.
- `Cargo.toml:51-86`: the outbound stack the Rust tree needed and why, including
  the resolver wrapper §4.4.1 replaces with `Dialer.Control`.
- `Cargo.toml:66-70`: the `webpki-roots` reasoning §4.4.2 preserves.
- `README.md`, the SMB section: the interfaces rule and what an empty list does.
- RFC 8414 (discovery), RFC 7636 (PKCE), RFC 7515 (JWS), RFC 7517 (JWK).
