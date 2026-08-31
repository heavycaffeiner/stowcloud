# HTTP 06: login flow v2

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/compat/nc/login_flow.go` and
> `go/internal/compat/ncwire/loginflow.go` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## State machine

Login flow v2 lets a desktop/mobile client obtain a revocable app password
without receiving the account password. It is compatibility vocabulary, but
the state machine is isolated in `engine/http/compat/loginflow.go` and backed
by the auth and state service ports.

```text
begin -> pending -> approved -> deliverable -> delivered -> expired/swept
```

Endpoints retain their protocol paths:

```text
POST /index.php/login/v2
POST /index.php/login/v2/poll
GET  /index.php/login/v2/flow/{login_token}
POST /index.php/login/v2/grant
```

There is deliberately no GET approval endpoint.

## Two independent tokens

Begin mints two independent 32-byte CSPRNG tokens in unpadded URL-safe base64:

- **poll token**: remains only in the client and authorizes collection;
- **login token**: travels through browser history, referrers and the visible
  address bar, so possession authorizes only viewing/approving the flow.

Only SHA-256 digests are stored and comparisons use constant time. Unknown,
expired and already-consumed token lookups are one `FlowUnknown` result. URLs
are built from the request origin only after the host boundary has confirmed
it is declared; otherwise the configured canonical origin is used. No
unchecked Host header becomes a returned URL.

## Lifetime and polling

An unapproved flow lives 20 minutes. Pending polls are limited to one per
second per flow by an atomic state update, and a faster request returns the
protocol's rate-limit response. An approved/deliverable flow bypasses the
pending poll delay; recording a refused poll must never push a ready client
into an endless reset window.

Sweep runs at startup and periodically. It removes expired token records and
temporary sealed delivery material. The flow table remains bounded by time
even if clients abandon every attempt.

## Consent

Opening the login URL while signed out redirects to the normal web login with
`safeReturnTo` preserving the local flow path. A signed-in browser gets a
server-rendered consent page. Approval requires all of:

- POST to the one grant endpoint;
- the ordinary session cookie;
- the ordinary Host/Origin boundary;
- the ordinary CSRF header/token;
- the login token in the submitted form/body.

A GET, public route, stale session, cross-origin form or token alone cannot
approve. The page names the requesting client generically and never displays
the poll token or future credential. It meets the accessibility requirements
in 03.

Approval atomically records one authorized user and login name. A second
approval returns `FlowAlreadyApproved`; it never changes the account. The
service operation is an `UPDATE ... WHERE approved_user IS NULL` inside the
state aggregate, not read-then-write.

## Delivery and retry

The old design text promised idempotent redelivery, but the implementation
minted an app password and immediately deleted the flow before knowing the
client had received the response. A dropped response left a live credential
the client never learned. The rebuild makes the promise true without storing
plaintext:

1. The first approved poll atomically claims delivery. Exactly one claimant
   may mint.
2. Auth creates an app password with full filesystem scope, no silent expiry,
   and a name identifying login flow v2. Policy lives in auth's
   `CreateSyncCredential`, not a wiring adapter literal.
3. The plaintext result is immediately sealed under the master key with AEAD
   binding to the flow row and stored as temporary delivery material together
   with credential id and key version. Plaintext never rests in SQLite.
4. The same poll token may retrieve and decrypt that **same credential** until
   the flow expires. It never mints a second one.
5. A successful response marks `delivered_ns`, but the sealed result remains
   until TTL so a connection lost after server write can retry. Sweep removes
   only the temporary ciphertext, not the app password the client now owns.

If mint succeeds but sealing/committing the delivery result fails, auth revokes
the just-created credential before returning. If revocation also fails, the
event is audited as an orphan credential with its id, never its value, and
health reports degradation. There is no silent live credential.

This requires a forward migration of `compat_login_flow` for delivery state,
sealed bytes, key version, credential id and delivered timestamp, and an auth
method to seal/open temporary credentials. The migration is added to
`../foundation/state.md` and the crypto operation to
`../auth/02-master-key-and-crypto.md` before implementation.

## Wire documents

Begin responds as the Nextcloud bare JSON shape:

```json
{
  "poll": {"token": "...", "endpoint": "https://host/index.php/login/v2/poll"},
  "login": "https://host/index.php/login/v2/flow/..."
}
```

Pending, unknown and too-soon responses retain the expected protocol statuses.
Delivery responds:

```json
{"server":"https://host","loginName":"alice","appPassword":"one-time-value"}
```

The server address is the admitted origin the client reached, so a multi-name
deployment does not send it to an unreachable first configured host.

## Audit

Distinct events record begin, approval, successful first delivery, redelivery,
expiry sweep, and failure/orphan cleanup. Events name actor and credential id
where known but never either flow token, either digest, ciphertext or app
password. Poll-pending noise is not audited per request; rate-limit abuse is
aggregated by the limiter.

## Deliberate changes

1. **Idempotent redelivery becomes real through temporary sealed delivery
   state.** The old implementation contradicted its own security comment by
   deleting immediately after mint.
2. **Full scope/no expiry moves from the wiring adapter into an auth-owned
   `CreateSyncCredential` policy method** (compat wire audit finding 3).
3. **Store sentinels enter the common error classifier**, deleting `flowErr`.
4. **Periodic sweep is mandatory assembly**, not an unused store method.

The two-token model, digest-only token storage, TTL, pending-poll interval,
POST-only approval and host-bound URLs otherwise carry whole.

## Tests

- Begin tokens are independent, 32-byte strength, URL-safe, stored only as
  digests and URLs use only an admitted origin.
- A leaked login token cannot poll; a leaked poll token cannot approve.
- GET approval, cross-origin POST, missing CSRF, no session and wrong login
  token all refuse without changing state.
- Hundreds of concurrent approvals produce one approved account; hundreds of
  concurrent first polls mint exactly one app password.
- Pending rate limit is atomic; an approved flow is delivered immediately
  even when the prior pending poll was inside the interval.
- Kill/drop the connection at every point from mint through response. The next
  poll either receives the same credential or no credential remains live;
  never two credentials.
- Redelivery returns byte-identical server/login/password until expiry and
  records redelivery without reminting.
- Database inspection finds token digests and sealed delivery bytes, never
  plaintext token/password substrings.
- Seal commit failure revokes the minted credential; forced double failure
  emits the orphan audit/health signal with no secret.
- Expiry and sweep remove temporary material; unknown/expired/consumed answers
  are indistinguishable.
- Master-key rotation leaves a deliverable flow readable under its recorded
  key version until normal sweep.
