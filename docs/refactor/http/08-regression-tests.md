# HTTP 08: known regression suite

> This document describes a from-scratch rebuild. Historical defect comments
> and tests under `go/internal/httpapi`, `go/internal/server`,
> `go/internal/dav`, and `go/internal/compat` are referenced as a behavioral
> specification only. The new implementation and its tests are written
> completely from scratch; nothing is copied.

## Purpose

These are incidents the project already paid to learn. Each becomes a named
black-box or integration test against the Fiber rebuild. A comment beside an
implementation is not accepted as the only guard. This list supplements the
owning documents' tests and is the merge checklist for Phase 3.

## Middleware and routing

| Test name | Regression held closed |
| --- | --- |
| `session hex is decoded before lookup` | Hashing printable session hex instead of raw token invalidated every session. |
| `DAV absence receives a Basic challenge` | Classifying `/dav` as public skipped credential parsing and challenge; clients never prompted. |
| `file protocol GET is never a static asset` | Catch-all public GET swallowed DAV/compat reads, so write worked and read failed. |
| `new app host updates host and CSRF together` | HostGuard used live hosts while CSRF kept a frozen boot-time copy. |
| `first-boot origin bypass requires private peer` | CSRF's empty-list bypass separated from its local-network premise. |
| `untrusted peer headers are ignored unparsed` | Direct clients could otherwise name their own audit/rate address. |
| `malformed XFF aborts trust walk` | Skipping an attacker-inserted bad hop redirected trust past it. |
| `all-trusted XFF has no client` | A proxy address was incorrectly promoted to end user. |
| `oversized body is a client error` | Underlying body-limit error fell through to 500 instead of 413. |
| `every route declares access and body class` | A newly mounted route silently inherited an unsafe default. |
| `scope and dispatcher use one Fiber match` | Parallel route matching could grant a route different scope from dispatch. |
| `DAV never falls through to SPA` | Unknown DAV methods/content rendered the application page. |
| `compat wildcard tails match edge cases` | Fiber wildcard semantics differ from `ServeMux` for empty/trailing tails. |

## Native handlers

| Test name | Regression held closed |
| --- | --- |
| `login is mounted on login` | Login was mounted on the change-password path while unit tests stayed green. |
| `file write has one mounted method` | Shipped client and server disagreed about POST versus PUT. v1 accepts only the documented POST. |
| `job cancel has one mounted spelling` | Client sent DELETE while only POST cancel was mounted. v1 accepts only the documented POST action. |
| `link edit routes are mounted` | PATCH/DELETE handlers existed but every request answered 405. |
| `thumbnail addresses by path` | File-id-based request required an id the listing did not carry. |
| `share create grants through core` | Handler-created raw grant policy could drift from core and skip reload. |
| `logout does not hide revoke failure` | Cookie was cleared while a database-failed session remained server-side. |
| `settings save reports stored versus applied` | Stored changes that failed live application were presented as clean success. |
| `settings patch cannot lock out its own host silently` | A host-list edit made the undo request unreachable. |
| `SMB revocation is synchronous and detached` | Revoked web access stayed live in SMB until someone pressed apply. |
| `filename cannot escape Content-Disposition` | CR/LF/quote/backslash could create or alter response headers. |
| `search emits terminal done on failure` | A post-commit failure closed silently and left the UI spinner running. |
| `WebSocket rechecks ACL at delivery` | A revoked grant kept receiving invalidations on a long-lived socket. |
| `WebSocket disconnect releases watch refs` | A closed tab left directories sticky forever. |
| `WebSocket upgrade requires same origin` | Authenticated GET upgrade otherwise admitted cross-site ambient cookies. |

## WebDAV

| Test name | Regression held closed |
| --- | --- |
| `DAV root is a grant projection` | Resolving a nonexistent physical root made clients call a valid account unreachable. |
| `path splits before percent decode` | Encoded `/` introduced a segment boundary after security parsing. |
| `foreign Destination is 502` | COPY to another host silently copied to that path on this server. |
| `reverse-proxy scheme does not break Destination` | Comparing scheme refused every HTTPS client forwarded as HTTP. |
| `If and lock guard share one snapshot` | Two reads observed different lock states in one request. |
| `conflicting LOCK admission is atomic` | Concurrent exclusive locks both passed a snapshot check. |
| `weak revalidation and strong write conditions differ` | Reusing one ETag matcher made writes accept weak validators. |
| `PROPPATCH live property aborts all` | Partial dead-property updates violated request atomicity. |
| `depth infinity refuses before walking` | Large trees began an unbounded traversal before the configured ceiling. |
| `expired locks are swept` | Every expired lock row remained forever. |
| `chunk member decimal is canonical` | `00001` and `1` could name two aliases for one intended chunk. |

## Nextcloud compatibility

| Test name | Regression held closed |
| --- | --- |
| `every implemented compat feature is mounted` | DAV/chunk/trash/properties/shares existed only in tests. |
| `app-password removal actually revokes` | Live route always returned 500 because its port was nil. |
| `OCS share routes dispatch` | A 299-line share surface was unreachable. |
| `OCS v1 success is 100 inside HTTP 200` | Generic status mapping breaks clients without visible errors. |
| `unlimited quota keeps real free space` | Android compared against negative free and never started uploads. |
| `recent lower bound is a full timestamp` | Date-only comparison failed both mobile clients' request. |
| `DAV permission letters keep exact order` | Wrong/missing W,N,V,C,K made sync silently read-only or reupload trees. |
| `directory size is aggregate or absent` | Inode size presented a plausible but false folder total. |
| `chunk mtime accepts fixed-point seconds` | iOS final assembly always failed on fractional timestamp. |
| `assembled chunk response has file id and ETag` | Desktop client rejected success as inaccessible. |
| `path thumbnail is not one label too deep` | Compat path was projected relative to the wrong root. |
| `compat thumbnail and direct bytes stay off app origin` | Proxying capability bytes would erase cookie/content host isolation. |
| `approved flow bypasses pending poll delay` | Polling during approval reset the limiter forever on a ready flow. |
| `login flow response loss does not orphan credential` | Mint-and-delete made a dropped delivery leave an unknown live app password. |

## Server and command

| Test name | Regression held closed |
| --- | --- |
| `listener binds replacement before old shutdown` | A typo in saved listen address took the deployment dark. |
| `listener drain ignores triggering request cancellation` | Navigating away could abort the listener handoff. |
| `TLS pair survives every crash window` | Separate writes left a certificate and key that did not match. |
| `setup token publish is durable` | Crash left memory and token file disagreeing. |
| `probe snapshot is durable and whole` | Rename without full durability could lose the healthcheck's settled address. |
| `setup grants the first admin every share` | Fresh admin signed in to an empty interface with no way to grant itself. |
| `sandbox applies before long-lived opens and token issue` | Re-exec opened stores twice and printed two setup tokens. |
| `new share parent is already in sandbox` | A share created after startup was registered but unreachable until restart. |
| `root is never granted for a root-child share` | Parent-grant policy could accidentally expose the whole filesystem. |
| `engine failure serves repair door instead of exiting` | Supervisor loop repeated an unrepairable stored failure. |
| `emergency falls back on parse and bind failures` | The repair door inherited the broken listener setting it existed to fix. |
| `healthcheck verifies stored certificate` | Skipping verification could accept an unaccounted server identity. |
| `argv dispatch works without a shell` | Docker exec-form HEALTHCHECK cannot rely on shell parsing. |
| `SMB agent does not restart an intentional stop` | Poll loop repeatedly tore down and rewrote accounts while SMB was off. |

## Execution and ownership

Tests live with the owning package where possible. Cross-mount and cutover
cases live under `engine/http/server` integration tests. Names above are
normalized human-readable test descriptions; Go test names use the same words.
Each table row maps to exactly one test function or one table case whose
failure output includes the row's phrase.

All transport tests run through a real TLS listener, not only Fiber's in-memory
test method. At minimum the matrix includes direct HTTP/1.1 and HTTP/2 at a
test reverse proxy forwarding to Fiber, because proxy address, streaming and
Host/Origin behavior differ at that boundary. WebDAV and Nextcloud reference
fixtures are byte-golden where clients parse quirks.

## Deliberate changes

1. **Historical comments become executable named tests.** No behavior change.
2. **Newly found defects during specification are added beside historical
   regressions**: WebSocket watch-ref cleanup and login-flow orphan delivery.
3. **The direct HTTP/2 test becomes a reverse-proxy HTTP/2 test**, matching
   the explicit Fiber transport decision in 07.

## Tests

- A meta-test parses the tables in this document's checked-in fixture and
  asserts every row has a registered test id.
- The full suite runs under `-race` where supported.
- Fault-injection cases run in subprocesses so kill/crash points are real.
- Tag matrix: plain, `embed_ui`, `compat_nc`, and both tags.
- No skipped regression test is accepted without a platform reason and an
  equivalent non-skipped assertion on the shipping Linux target.
