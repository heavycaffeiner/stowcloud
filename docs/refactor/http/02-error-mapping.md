# HTTP 02: error classification and protocol mapping

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/apierr`, `go/internal/dav/method.go` (`StatusOf`), and
> `go/internal/compat/ncwire/loginflow.go` (`flowErr`) is referenced as a
> behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## One classification, several wire vocabularies

Target `engine/http/apierr`. The old tree has three complete
`errors.Is` ladders. The rebuild has one classifier and thin protocol
renderers:

```go
type Class uint16

type Classified struct {
    Class Class
    Key   string
    Args  []Arg
}

func Classify(err error, visibility Visibility) Classified
func REST(c Classified) (status int, body *Error)
func DAV(c Classified) (status int, condition Name)
func OCS(c Classified, version OCSVersion) (httpStatus, ocsCode int)
```

`Classify` is presentation-owned, not service-owned. The core remains a
sentinel taxonomy with no HTTP concepts (`../core/01-errors.md`); putting a
second numeric taxonomy in service would violate that decision merely to
deduplicate presentation code. This package imports and recognizes service
sentinels once. DAV and compat import only the `Class` result and never repeat
the sentinel ladder.

## Classes

The closed enumeration covers at least:

| Class | Meaning |
| --- | --- |
| `Internal` | unrecognized or infrastructure failure |
| `Malformed` | syntax, malformed encoding, bad XML or bad header |
| `Unprocessable` | parsed input violates a named constraint |
| `BodyTooLarge` | request/raw body outer bound |
| `LimitExceeded` | a configured structural/resource bound |
| `AuthRequired` | no usable credential was presented |
| `AuthInvalid` | a presented credential failed |
| `AccountDisabled` | known principal may not authenticate |
| `RateLimited` | request or login-flow rate bound |
| `Hidden` | missing or denied where existence must not be disclosed |
| `Denied` | caller may know it exists but lacks this action |
| `NotFound` | ordinary absence on a non-hidden surface |
| `Gone` | expired/consumed public capability |
| `Conflict` | current state conflicts with the action |
| `Exists` | no-clobber create collided |
| `NotEmpty` | collection must be empty |
| `Precondition` | validator or submitted state failed |
| `Locked` | no acceptable lock token was submitted |
| `NoSpace` | physical floor or quota exhausted |
| `ShareUnavailable` | registered share backing is temporarily unavailable |
| `LastAdmin` | deployment would lose its last administrator |
| `WeakPassword` | password floor refusal |
| `NameTaken` | unique account/group name collision |
| `SubsystemUnavailable` | optional service absent in this build |
| `NotImplemented` | recognized operation not supplied by this build |
| `BadGateway` | protocol target names another origin/server |
| `SetupComplete`, `SetupExpired`, `SetupInvalidToken` | setup gate outcomes |
| `FlowUnknown`, `FlowPending`, `FlowApproved`, `FlowTooSoon` | login flow v2 |

Typed errors contribute safe fields only: current ETag, configured bound,
share display label, fixed reason token, minimum password length. No host path,
raw lower-layer message, credential, or offending client value becomes an
argument.

## Visibility and the existence rule

`VisibilityHidden` folds `core.ErrNotFound`, resolver denial, refused symlink
and malformed-unaddressable path into `Hidden`. REST renders the same status,
code, message and bytes for every member: 404 `fs.not_found`. DAV renders the
same bare 404. Public-link token lookup does the same. Timing is not promised
identical, but body and status are.

`VisibilityKnown` allows a true denied action to become `Denied` (REST/DAV
403), because the caller already learned the resource through an authorized
parent. The caller chooses visibility at the boundary; handlers do not infer
it from error text.

## Native envelope

The v1 envelope remains:

```json
{
  "error": {
    "code": "fs.invalid_name",
    "message": "invalid name",
    "detail": {
      "reason_key": "fs.invalid_name",
      "reason_params": {"component": "name"}
    }
  }
}
```

`Error.MarshalJSON` is the only envelope encoder. `Wire` is exported only for
batch item outcomes. An internal error always suppresses detail. `Sc-Trace`
is a response header, not a body field.

The REST adapter is the only table assigning native status, stable code,
fallback message and catalogue key. Request constructors create semantic
classes, not statuses:

```go
func BadRequest(key, field string) error
func Unprocessable(key, field string) error
func BadGateway(key, field string) error
```

They carry the **field name**, never its client-supplied value.

## Protocol adapters

DAV maps the common class onto RFC statuses and may add a DAV condition:

- `Locked` is 423 with `DAV:lock-token-submitted` when no token authorizes
  the write.
- `Precondition` is 412.
- `Exists` is 405 where RFC method semantics require it.
- `NoSpace` and structural `LimitExceeded` are 507.
- `BadGateway` is 502 for foreign `Destination`.

Protocol-parser errors enter as already-classified values; they do not add a
second domain-sentinel switch.

OCS maps a class to its envelope code and then lets OCS v1/v2 select the HTTP
status according to the compatibility table in `05-compat-scope.md`. Login
flow storage sentinels are translated into `Flow*` classes at one adapter
edge, replacing `flowErr`.

TUS remains the one separate status vocabulary. It consumes `Classified`
where the classes overlap but keeps protocol statuses and headers, including
460 for checksum mismatch. It must not grow another service-sentinel ladder.

## Commit state

An error mapper writes only when the response is uncommitted. Once an SSE,
archive, file body or WebSocket has committed, later failures are logged and
the stream's own terminal shape is used where one exists. No mapper attempts
to overwrite a committed body.

## Deliberate changes

1. **Three sentinel ladders become one classifier with protocol adapters**
   (presentation audit cross-package finding).
2. **Request errors stop storing HTTP statuses**; they carry semantic classes
   and the REST adapter chooses 400/422/502.
3. **Fiber body-limit errors are explicitly classified as 413**, replacing
   the old `*http.MaxBytesError` special case.
4. **Visibility is an explicit classifier input**, making the existence rule
   testable across REST, DAV and public links.

The native codes/messages/envelope, DAV's protocol distinctions and OCS's
versioned status behavior otherwise carry whole.

## Tests

- A table names every service and protocol sentinel and its class; reflection
  over the documented sentinel inventories fails when a new one is unmapped.
- REST, DAV and OCS adapters are table-tested for every class.
- Missing, outside-grant, denied-hidden and symlink-denied produce byte-exact
  identical REST 404 bodies; DAV and public-link mounts match their own exact
  hidden response.
- Internal errors never carry detail or lower-layer text.
- Typed safe fields survive; host paths, credentials and raw offending values
  never appear (secret scan plus fixtures).
- An oversized Fiber body maps to 413 and `http.body_too_large`.
- A mapper does not write after a stream commits.
- Adding an independent `errors.Is` ladder under `engine/http/dav`,
  `engine/http/compat` or `engine/http/handler` fails a source gate.
