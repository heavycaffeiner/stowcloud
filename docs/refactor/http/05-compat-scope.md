# HTTP 05: Nextcloud compatibility scope

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/compat/nc`, `go/internal/compat/ncport`, and
> `go/internal/compat/ncwire` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Decision: the complete surface ships

Target `engine/http/compat`, built only with `compat_nc`. The audit found a
half-product: status, OCS basics, preview and login flow were mounted, while
the implemented OCS share API, Nextcloud DAV layout, chunked upload v2, trash
and vendor properties had no live route. The rebuild does not preserve an
accidental wiring gap. It ships this feature matrix:

| Feature | Phase 3 status |
| --- | --- |
| status and captive-portal probes | ship |
| capabilities and exact OCS envelope | ship |
| current/other user and quota | ship |
| unified search, recent, favorites | ship |
| preview redirects and direct download URLs | ship |
| app-password revocation | ship |
| OCS share list/create/get/update/delete | ship |
| OCS sharee directory search | ship |
| `/remote.php/webdav` and `/remote.php/dav/files/{user}` | ship |
| chunked upload v2 | ship |
| trash collection | ship |
| ownCloud/Nextcloud DAV properties and REPORT/search vocabulary | ship |
| login flow v2 | ship, specified in 06 |
| notifications, user status, navigation and provisioning stubs | ship only where clients require the empty answer |
| activity stream, federation, comments, versions, E2EE, system tags, bulk upload, collaborative editing | do not advertise, do not mount |

Capabilities are generated from this matrix. A feature is advertised only
when every route and port required to complete it is wired. Presence-sensitive
keys that mean "try this endpoint" are omitted rather than set false.

## Isolation and ports

Vendor vocabulary lives only under `engine/http/compat`. The package may
import `engine/http/dav` and `engine/http/apierr`, but no service concrete type,
store or infra package. Neutral ports are declared here and implemented by
server assembly against service methods. The old separate `ncport` and
`ncwire` packages collapse into the one compat package plus one `ports.go`:
there is one consumer, and a third wiring package created more room for
nil omissions and inline SQL than isolation.

Ports cover:

- file resolve/list/stat and stable file-id lookup;
- account info, quota and scoped login lookup;
- filename search and recent entries;
- favorites;
- short-lived content-origin preview/download signing;
- trash flatten/list/restore/delete/empty;
- resumable upload aliases and chunks;
- OCS share operations;
- app-password revoke and login-flow operations.

No port is optional for an advertised feature. Construction validates the
matrix and refuses startup when a required port is nil. The old behavior of
shipping a route that always returned 500 because `Revoke` was nil dies.

The durable deployment instance id moves fully into the state aggregate
already described by `../foundation/state.md`; no inline SQL exists in
compat wiring. It remains stable forever, because changing it makes clients
re-sync as though they reached another server.

## Public and credential paths

The protocol declaration handed to 01 contains:

- File prefixes: `/remote.php/`, `/index.php/remote.php/`.
- Public GETs: `/status.php`, `/index.php/204`, capabilities under both OCS
  versions, and the browser consent page only as a navigation target whose
  handler itself requires/redirects to a session.
- Credential-flow POSTs: begin and poll only. Approval/grant is session plus
  CSRF and is never public.

Every file prefix gets the Basic challenge behavior from 01. A client-supplied
username segment is dropped for the caller's own tree; it never selects a
different principal. Auth is one server result, not re-verified by compat.

## OCS routes

Mounted under both `/ocs/v1.php` and `/ocs/v2.php`, with GET/POST/PUT/DELETE
at the prefix and internal dispatch for the protocol path. Exact routes:

- `GET /cloud/capabilities`
- `GET /cloud/user`
- `GET /cloud/users/{login}`
- `GET /search/providers`
- `GET /search/providers/files/search`
- `GET /apps/files/api/v1/recent`
- `GET /apps/files/api/v1/favorites`
- `POST /apps/dav/api/v1/direct`
- `DELETE /core/apppassword`
- `GET|POST /apps/files_sharing/api/v1/shares`
- `GET|PUT|DELETE /apps/files_sharing/api/v1/shares/{id}`
- `GET /apps/files_sharing/api/v1/sharees`
- the required empty stubs already named by the old surface.

Four known crash-triggering paths remain hard 404s before dispatch. Unknown
OCS requests log method/path and answer the version's not-found envelope.

Every OCS response carries `Access-Control-Allow-Origin: *`, matching the
reference protocol. This fixed protocol exception does not consume the general
`AllowedOrigins` setting. OCS requests authenticate with app passwords in
Authorization, not session-cookie ambient authority, and the header does not
make a browser able to issue authenticated cookie calls. No global CORS policy
is installed.

## Exact OCS envelope

The ordered `Val` tree and two custom writers carry whole because the JSON
and XML are not standard encodings of one Go value:

- maps preserve insertion order;
- XML list items become `<element>`;
- XML booleans are `1` or empty;
- empty values self-close;
- JSON booleans remain booleans;
- numeric map keys do not become tag names;
- all XML text uses the same correct escaper as DAV.

Format negotiation: query `format=json` wins, then an Accept containing
`application/json`, then XML. An unknown format query means XML.

Status table:

| OCS entry | Success code | HTTP behavior |
| --- | --- | --- |
| v1 | 100 | HTTP 200 except OCS 997 becomes HTTP 401 |
| v2 | 200 | normal code mirrored; 997 to 401, 998 to 404, 996/999 to 500, invalid range to 400 |

The envelope's byte quirks are protocol, not cleanup candidates.

## Account, quota, search and favorites

Current-user output keeps both `displayname` and `display-name`. Other-user
lookup exposes no quota and treats outside visibility scope and absence as
one not-found. Quota uses `-3` only in the quota field for unlimited; real
free/used/total remain non-negative and uint64 values clamp at MaxInt64 so an
Android client never reads negative free space and parks all uploads.

Search pages at 25 by default, clamp to 100 and emit one extra lookup to
decide the next cursor. Recent accepts RFC3339 or epoch seconds and uses a full
timestamp. Favorites key by file identity and follow rename. Search,
favorites, recent and property emission all materialize or reuse the same
stable file id; there is no parallel identity scheme.

Favorites are both readable and writable. The OCS list endpoint returns the
starred set, and the DAV `oc:favorite` dead/vendor property (`0`/`1`) toggles
the identity-backed favorite through the core seam. A tested helper with no
live mount is not considered shipped.

Preview endpoints never proxy bytes through the app origin. They resolve an
id/path under the caller and redirect 302 to a private, no-store, short-lived
`/c/{claim}` URL on the configured content host (03). Sizes default to 64 and
clamp to 4096. `forceIcon` and a
missing preview answer 404 so the client uses its own icon. The wildcard
thumbnail route is tested with an empty tail, one segment, nested tail and
encoded separators under Fiber.

Direct URLs follow the same content-origin capability with an immediate,
5-minute lifetime. A deployment with no content host omits/returns not-found
for preview/direct and does not advertise the capability. Every failure is
not-found to preserve the existence rule.

## OCS shares

All three supported share types are real:

- type 0 (user) and type 1 (group) are grants over the resolved path;
- type 3 (public link) is a core share link.

The service layer gains a `CompatibilitySharing` operation that takes actor,
resolved target, grantee and requested permission mask. It enforces the
actor's Share permission, prevents delegation beyond the actor's effective
permissions, resolves user/group names through auth, persists through core's
grant methods, reloads ACL before success, and triggers the synchronous SMB
propagation rule from 03. Presentation never creates a grant row directly.

Listing includes shares created by the actor and shares with the actor where
the wire filter requests them. Path, reshares, subfiles and shared-with-me
filters retain the reference's literal `"true"` rule. A path filter selects
that entry; with `subfiles` it selects the entries directly inside it, which
is how a folder listing badges its children in one call instead of one call
per child. Create/update forms preserve absent versus empty fields.
Public-link password values are never returned: null or literal `redacted`
says whether one exists.

The sharee search backs the picker that names a target. Without it the
advertised user and group types are unreachable, because a client has no way
to ask for a name. Who appears is the account service's directory rule and
not a second rule here: the caller, an administrator's full view, or an
account sharing a group. Disabled accounts and the caller themselves are left
out, a group the caller does not belong to is not offered, and every list the
reference sends is present even when empty, because the client reads each by
name and treats a missing one as a failed search rather than an empty result.

`FormatShare` remains exact, including inconsistent field types, fixed field
order, string id, reference date format, integer `mail_send`/`hide_download`,
and the overloaded public-link `share_with` password-state quirk. File-id
values clamp rather than wrap negative.

The capabilities change to truth: user/group sharing is advertised only when
`CompatibilitySharing` is wired; public links are always advertised when core
link crypto is attached. Resharing remains false because grant chains are not
offered.

## DAV layout and properties

`ParseDavPath` recognizes both `/remote.php/` and
`/index.php/remote.php/`, then:

- legacy `webdav/{path}`;
- `dav/files/{user}/{path}`;
- `dav/uploads/{user}/{transfer}/{member}`;
- `dav/trashbin/{user}/trash/{entry}` and restore;
- minimal principal root/user stubs.

All use 04's shared split-before-decode helper. Encoded separators and `.`/`..`
are refused. Deeper unknown nesting is not silently truncated.

Vendor properties register the ownCloud and Nextcloud namespaces through
DAV's `PropSource`. The exact permission-letter order remains `S R G D N V`
then `W` for a writable file or `C K` for a creatable directory; `M` is never
emitted. Missing W/N/V/CK changes client behavior, so the string has exhaustive
fixtures. Directory size is the recursive aggregate or omitted, never inode
size. File id, DAV id, share types, encryption and preview fields carry their
documented exact types.

REPORT/search leaves are passed to the registered service query source. A
namespace nobody claims refuses rather than yielding an empty success.

## Chunked upload v2

Protocol:

```text
MKCOL  /remote.php/dav/uploads/{user}/{tid}
PUT    /remote.php/dav/uploads/{user}/{tid}/{chunk}
MOVE   /remote.php/dav/uploads/{user}/{tid}/.file
DELETE /remote.php/dav/uploads/{user}/{tid}
PROPFIND collection
```

The attacker-controlled transfer id is a user-scoped alias, never a session
id. It is 1..128 bytes from ASCII letters, digits, `.`, `_`, `-`, excluding
`.` and `..`. Chunk names are canonical decimal 1 through 10,000: leading
zero aliases are refused, resolving the old disagreement with DAV.

`OC-Total-Length`, `X-OC-Mtime`, `X-OC-CTime`, `Destination`, `OC-FileId`
and `OC-ETag` retain exact spellings. Time accepts integer or fixed-point
seconds and truncates the fraction; exponent notation and overflow refuse.
Destination uses 04's segment decoder and same-origin rule, not a third path
parser.

Assembly success is 201 for create and 204 for overwrite and **must** carry
both DAV file id and ETag. A missing one is a hard client failure. Chunk size
is advisory, not a server ceiling, exactly as capabilities state.

## Trash

The protocol projects all reachable per-share trashes into one flat account
collection. The service supplies globally unique display names plus original
path, deletion seconds, size, kind and file id. PROPFIND renders through DAV's
one multistatus writer. DELETE on collection empties, DELETE on entry purges,
MOVE from entry restores to the recorded original path; the request's target
leaf is ignored so restore cannot become an arbitrary move.

## Build tag and route validation

With `compat_nc`, construction registers the full matrix and validates every
required port. Without it, one tagged sibling returns no routes, protocol
paths, DAV aliases, sources or capabilities; compat code is not typechecked
into the product. The layer gate checks that compat imports no store/infra and
that no service imports vendor vocabulary.

## Deliberate changes

1. **Every previously implemented but unwired feature is wired** (compat audit
   finding 1).
2. **OCS share CRUD is mounted and backed by a service policy surface**
   (finding 2).
3. **App-password revoke is always wired when advertised** (finding 3).
4. **One DAV XML escaper and one path decoder replace the duplicate spellings**
   (findings 4 and 5).
5. **Canonical chunk names refuse leading zeros** (finding 7).
6. **One safe uint64-to-int64 clamp helper replaces three copies** (finding 8).
7. **The build/layer import gate is real CI, not a package-doc assertion**
   (finding 11).
8. **Instance-id SQL moves to the state aggregate; `ncport`/`ncwire` collapse**
   (ncwire findings 1, 2 and 4).
9. **No advertised port may be nil**, ending silent partial wiring.

Reference quirks that clients parse, including share field inconsistency,
OCS envelope shape, wildcard OCS CORS and property-letter order, are unchanged.

## Tests

- Feature-matrix test: every advertised capability has all routes/ports and
  every unadvertised feature has none.
- Build both tag states; source/import gate proves vendor isolation.
- Byte-golden OCS v1/v2 XML and JSON for null, bool, ordered maps, lists,
  empty values, Unicode and every status code branch.
- Every OCS route under both prefixes and all supported methods; the four
  hard-404 paths and unknown-route warning.
- Account/quota fixtures including unlimited and >MaxInt64 values; hidden user
  lookup is indistinguishable from absent.
- Search/recent/favorite paging and stable-id behavior.
- Favorite set/unset through DAV property plus OCS listing, including rename
  following and replacement-at-old-path not inheriting the star.
- Preview/direct route wildcard edge cases and app-origin byte negative test.
- OCS share CRUD for user, group and link; non-delegable permission refusal;
  exact `FormatShare` golden documents; SMB propagation called once per
  authorization change.
- DAV path corpus across both prefixes, principal stubs, empty/trailing tails,
  encoded separators and traversal.
- Every permission-letter combination, stable DAV id, directory-size omission
  and vendor namespace/property golden response.
- Chunk flow round trip, user-scoped alias collision, canonical decimal,
  fractional times, required file-id/ETag and 201/204 distinction.
- Flat trash across multiple shares, unique names, restore ignoring the target
  leaf, purge and empty.
- No constructor can advertise or register a feature with a nil required port.
