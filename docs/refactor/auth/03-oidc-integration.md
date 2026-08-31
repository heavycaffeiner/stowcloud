# Auth 03: the durable halves of single sign-on

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/auth` (here `oidcflow.go`, `oidclink.go`, the link/unlink
> halves of `reconfirm.go`) is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Scope

The protocol half of single sign-on (discovery, JWKS, token verification)
is the `oidc` package's own phase (`../oidc/00-relying-party.md`). This
document owns what auth stores for it: the in-flight flow rows and the
durable identity links. The split is the trust boundary: the oidc package
talks to the internet and holds no state; auth holds state and talks to
nobody.

## The position: link-only

The provider authenticates and never creates an account. Authority over
who has an account stays in this database, which is what makes a
revocation here total: unlink, and the provider's say-so opens nothing.

The identity is **the issuer and the subject together, never the email
address**. A provider may reassign an address to a different person;
matching on one would hand that person the account.

## Flow rows

```go
type OIDCFlow struct {
    User         int64
    Nonce        string // handed to the token verifier
    CodeVerifier string // handed to the provider in the exchange
    RedirectURI  string
    ReturnTo     string
}

var ErrNoOIDCFlow = errors.New(...)

func (s *Service) StartOIDCFlow(ctx context.Context, userID int64,
    state, nonce, binding, verifier, redirectURI, returnTo string) error
func (s *Service) TakeOIDCFlow(ctx context.Context, state, binding string) (OIDCFlow, error)
```

What rests as a digest and what rests whole is the design:

- **The state and the browser binding rest only as SHA-256 digests.**
  Both values go to the browser, so storing them whole would make a read
  of this table sufficient to complete somebody else's link. The callback
  checks equality, and a digest answers equality.
- **The nonce and the code verifier rest whole**, because both must be
  handed back out: the verifier to the provider, the nonce to the token
  verifier. Neither authenticates anything on its own.

Behaviors:

- `StartOIDCFlow` sweeps expired flows in the same transaction that
  inserts one. Sweeping here rather than on a timer means a deployment
  nobody links on accumulates nothing, and there is no timer to forget.
- `TakeOIDCFlow` **consumes**: the row is deleted whether or not the
  binding matches, because a flow whose binding failed is not one to
  leave redeemable for another attempt. The binding comparison is
  constant-time. An unknown state and an expired state are one answer
  (`ErrNoOIDCFlow`); distinguishing them would say whether a state value
  was ever real.
- The binding is a cookie-held value, so a state lifted from a log or a
  referrer is not enough on its own.

## Identity links

```go
type OIDCLink struct {
    Issuer, Subject string
    LinkedNs        int64
    LastLoginNs     *int64 // nil: linked and never used to sign in
}

var ErrNoOIDCLink   = errors.New(...)
var ErrOIDCLinkTaken = errors.New(...)

func (s *Service) CreateOIDCLink(ctx context.Context, userID int64, issuer, subject string) error
func (s *Service) OIDCLinkOf(ctx context.Context, userID int64) (OIDCLink, error)
func (s *Service) UserForOIDCIdentity(ctx context.Context, issuer, subject string) (int64, error)
func (s *Service) RemoveOIDCLink(ctx context.Context, userID int64) error
func (s *Service) TouchOIDCLink(ctx context.Context, issuer, subject string) error
func (s *Service) LinkOIDC / UnlinkOIDC   // the passdb-republishing wrappers
```

- One link per account; re-linking replaces the old identity.
- An identity already linked to a different account is
  `ErrOIDCLinkTaken`, typed, because the screen offers "sign in there and
  unlink" as the fix and needs to know that is the case.
- `TouchOIDCLink` stamps `last_login_ns` on every provider sign-in, so
  the screen can show a link that exists but was never used.
- **Linking and unlinking republish the passdb.** A linked account's SMB
  eligibility can change (the separate-password rule in 01), and the
  sidecar must agree with the database.

## Deliberate changes

1. **The SQL moves to a state aggregate** (`oidclink.go` +
   `oidclink_sql.go` in `engine/store/state`, owning both the flow and
   link tables). Same statements, same digest columns.
2. Nothing else. The digest/whole split, the consume-on-take, the
   link-only position and the issuer+subject identity are all as the
   reference.

## Tests

- A flow round-trips; the second take answers `ErrNoOIDCFlow` (consumed).
- A wrong binding refuses **and** consumes: the third take with the right
  binding also answers `ErrNoOIDCFlow`.
- An expired flow answers `ErrNoOIDCFlow`; the sweep removes it on the
  next start.
- The stored state and binding columns hold digests, not the values
  (read the row raw).
- The nonce comes back whole for the verifier.
- Linking: round-trip; re-link replaces; a taken identity answers
  `ErrOIDCLinkTaken`; unlink detaches and `UserForOIDCIdentity` stops
  resolving.
- Signing in stamps the link (`TouchOIDCLink`), and a never-used link
  reports a nil stamp.
- Link and unlink each republish the passdb (counting sink).
