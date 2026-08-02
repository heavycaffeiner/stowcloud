# Legacy-client compatibility layer

`sc-compat-nc`. Removable in full behind `feature = "compat-nc"`.

---

## 1. Isolation contract

### 1.1 The test

One question, applied to every new feature:

> **Would this feature need to exist without the compat layer?**

| Feature | Verdict | Lives in |
|---|---|---|
| Stable file id | Yes (WebDAV rename tracking, sync in general) | **core** (`sc-meta`) |
| Directory aggregate ETag | Yes (efficient sync in general) | **core** (`sc-meta`) |
| Recursive size | Yes (UI display) | **core** |
| Favorites | No — not a concept in our own UI | compat |
| `"%08d{fileid}{instanceid}"` serialization | No — pure NC vocabulary | compat |
| `oc:permissions` RGDNVCK string | No | compat |
| OCS envelope | No | compat |
| Chunked upload session engine | Yes | **core** (`sc-upload`) |
| `MKCOL`/`PUT {n}`/`MOVE .file` mapping | No | compat |
| Share links | Yes | **core** |
| `shareType` integer codes, OCS share JSON | No | compat |

### 1.2 What compat may consume

Only the public traits `sc-core` exposes:

```rust
pub trait Vfs        { /* open, stat, read_dir, create, rename, remove … */ }
pub trait AclEval    { fn evaluate(&self, u: UserId, s: ShareId, p: &SafePath, w: Perms) -> Decision; }
pub trait AuthProv   { fn verify(&self, cred: &Credential) -> Option<Principal>;
                       fn issue_app_password(&self, u: UserId, name: &str, scope: Scope) -> Secret; }
pub trait MetaStore  { fn fileid(&self, s: ShareId, st: &Stat) -> FileId;
                       fn dir_etag(&self, s: ShareId, id: FileId) -> Result<Aggregate>; }
pub trait UploadEng  { fn create(&self, spec: SessionSpec) -> Result<SessionId>; … }
pub trait LinkStore  { fn create_link(&self, spec: LinkSpec) -> Result<Link>; … }
```

Compat's own state lives in its own tables (`nc_*`) only:

```sql
CREATE TABLE nc_favorite      (user INTEGER, fileid INTEGER, PRIMARY KEY(user, fileid));
CREATE TABLE nc_upload_alias  (tid TEXT PRIMARY KEY, user INTEGER, session BLOB);
CREATE TABLE nc_login_flow    (poll_hash BLOB PRIMARY KEY, flow_hash BLOB UNIQUE,
                               client_name TEXT, created_ns INTEGER, expires_ns INTEGER,
                               result BLOB);   -- app password after approval, consumed once
CREATE TABLE nc_instance      (k TEXT PRIMARY KEY, v TEXT);   -- instanceid, etc.
```

### 1.3 What actually enforces this

`scripts/verify.sh` runs two checks that make the rule above more than a convention:

1. **No compat vocabulary in core code.** Greps `crates/sc-{vfs,meta,core,acl,auth,dav,upload,http,watch,search,preview,smb}/src` (case-insensitive) for `\boc[:_-]`, `\bocs\b`, `remote\.php`, skipping comment lines so a core crate may still *explain* in a doc comment why a protocol-neutral abstraction exists. A hit in actual code — a header name, a route string, error text — fails the gate.
2. **The feature-stripped build compiles.** `cargo build -p sc-server --no-default-features` must succeed, proving `sc-compat-nc` and every one of its route strings are absent from the binary, not merely unrouted. `crates/sc-server/tests/wiring.rs::compat_paths_are_absent_without_the_feature` then checks the routing half — that `/status.php`, `/ocs/v2.php/cloud/capabilities`, `/remote.php/webdav/x` fall through to the native stack rather than being answered.

Both pass today. A third property — that `sc-core` can never depend on `sc-compat-nc` — needs no gate: `sc-compat-nc` depends on `sc-core`, so a reverse edge would be a dependency cycle Cargo refuses to build at all.

`sc-server routes --json` (`cmd_routes`) exists as a manual diagnostic for auditing the live route table; it is not wired into `verify.sh` as an automated gate.

---

## 2. Version advertised

NC clients gate features on the server version, and refuse to connect if it is too low. Claiming too high sets an expectation for features we do not have.

**Not hardcoded.** `compat_matrix.toml` holds the verified combination; the admin UI can change it.

```toml
[claim]
version       = "31.0.4.1"     # <major>.<minor>.<patch>.<build>
versionstring = "31.0.4"
edition       = ""
productname   = "Nextcloud"     # clients branch on this string — do not change it

[source_audited]                       # we read the source — a record, not evidence
desktop = "desktop @ master"
android = "android-library @ master"
ios     = "iOS SDK @ master"

[device_verified]                      # a real device actually connected — none yet
desktop = []
android = []
ios     = []
rclone  = []
```

`31.0.4.1` is a real shipped maintenance release — not the `.ref/` clone's unreleased `35.0.0 dev`, which would put clients on unreleased-server code paths and, on desktop, trigger an "unsupported server" banner. It sits at or above every current client's minimum, and every feature a client gates on at this version (chunking v2, OCS v2, Login Flow v2, the `oc:`/`nc:` vocabulary, share types 0/1/3) is one we actually implement.

**The two lists are kept apart on purpose.** `source_audited` means we read that client's network layer and matched our responses to what its code requires. `device_verified` means a real build of that client actually talked to this server and worked. **Only the second is evidence.**

This distinction has already cost us once: reading the desktop client's source alone produced `<D:multistatus>` — valid XML, correct for every namespace-aware parser — while iOS saw every directory as empty and reported success anyway (`proposals/stowcloud-4-webdav.md`). Source-reading found the bug; source-reading is also what shipped it. `device_verified` stays empty until a device has actually run.

---

## 3. status.php

```
GET /status.php  → 200 application/json
{
  "installed": true,
  "maintenance": false,
  "needsDbUpgrade": false,
  "version": "31.0.4.1",
  "versionstring": "31.0.4",
  "edition": "",
  "productname": "Nextcloud",
  "extendedSupport": false
}
```

Unauthenticated. **No product identity or real implementation detail goes here** — client parsers expect the reference shape, and there is no reason to hand a scanner anything extra. Our own identity is disclosed in `/api/capabilities` and the `Server:` header instead.

---

## 4. OCS envelope

```
GET /ocs/v2.php/…            header: OCS-APIRequest: true (401/403 without it)
query: ?format=json          (XML otherwise)
```

```xml
<?xml version="1.0"?>
<ocs>
  <meta><status>ok</status><statuscode>200</statuscode><message>OK</message></meta>
  <data>…</data>
</ocs>
```

```json
{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{…}}}
```

- **v1 (`ocs/v1.php`) succeeds with `100`; v2 (`ocs/v2.php`) with `200`.** Getting this wrong makes some clients fail silently.
- v2 also reflects `statuscode` onto the HTTP status.
- **"v1 is always HTTP 200" was wrong in an earlier draft.** The reference's `V1Response::getStatus()` promotes `997` (`RESPOND_UNAUTHORISED`) to HTTP **401**; every other code is HTTP 200.
- **v1's `meta` always has 5 keys** — `totalitems`/`itemsperpage` are present as an **empty string** when there is no value. v2 omits them entirely and only includes them as integers when meaningful.
- A missing `OCS-APIRequest` header makes the reference answer **HTTP 412 with an unwrapped `{"message":"CSRF check failed"}`**. We wrap `997 → 401` in a proper envelope instead — more consistent, and we will match byte-for-byte if that ever turns out to matter.
- One wrapper implementation handles both:

```rust
pub struct Ocs<T>(pub OcsVersion, pub Result<T, OcsError>);
impl<T: Serialize> IntoResponse for Ocs<T> { /* format negotiation + per-version codes */ }
```

XML is **hand-written**, not serde-derived — NC's XML shape (array element names, empty-value representation) diverges from serde's default mapping often enough that automating it costs more than it saves.

---

## 5. capabilities

```
GET /ocs/v2.php/cloud/capabilities?format=json
```

```json
{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{
  "version": {"major":31,"minor":0,"micro":4,"string":"31.0.4","edition":"","extendedSupport":false},
  "capabilities": {
    "core": {
      "pollinterval": 60,
      "webdav-root": "remote.php/webdav",
      "reference-api": false,
      "mod-rewrite-working": true
    },
    "dav": { "chunking": "1.0", "bulkupload": "1.0" },
    "files": {
      "bigfilechunking": true,
      "chunked_upload": { "max_size": 10485760, "max_parallel_count": 4 },
      "blacklisted_files": [".htaccess"],
      "forbidden_filename_characters": ["\\", "/", ":", "*", "?", "\"", "<", ">", "|"],
      "undelete": true,
      "versioning": false,
      "comments": false,
      "directEditing": { "url": "", "etag": "", "supportsFileId": false }
    },
    "files_sharing": {
      "api_enabled": true,
      "resharing": false,
      "group_sharing": true,
      "user": { "send_mail": false, "expire_date": {"enabled": true} },
      "public": {
        "enabled": true,
        "upload": true,
        "multiple_links": true,
        "password": { "enforced": false, "askForOptionalPassword": true },
        "expire_date": { "enabled": true },
        "send_mail": false
      },
      "federation": { "outgoing": false, "incoming": false },
      "sharee": { "query_lookup_default": false }
    },
    "theming": { "name":"…", "color":"#0082c9", "background":"", "logo":"", "slogan":"" },
    "user_status": { "enabled": false },
    "notifications": { "ocs-endpoints": ["list","get","delete"] }
  }
}}}
```

Rules:

1. **A feature we lack is `false`/empty, never omitted.** An absent key makes clients assume a default (usually `true`) and call an endpoint that does not exist. **§5.1's "presence = on" keys are the opposite — read both.**
2. `chunked_upload.max_size` carries `upload.chunk_size_advisory` — **advisory only**. There is no server-side chunk size ceiling (`proposals/stowcloud-7-upload.md`); a larger chunk is accepted normally. A client that sends more and hits a 413 from an intermediary proxy handles it with its own auto-adjust. **Mobile never reads this field at all** — Android's `GetCapabilitiesRemoteOperation` has no `chunked_upload` parsing, and iOS's chunk size is injected by the app. This is a desktop-only hint.
3. `bulkupload` is omitted entirely when unsupported — present, it routes clients onto a small-file batching path we do not implement.
4. `forbidden_filename_characters` **must be a superset of `SafePath`'s actual rejection rules, not equal to them.** It is a client-side creation hint — stopping a Windows client from creating a name its own filesystem can't store — not a mirror of server enforcement, and it must not be made equal by rejecting all nine server-side either: shared folders are co-accessed by Jellyfin, rsync and Samba on a Linux filesystem that permits `* ? " < > |` in names, so rejecting them here would make files that already exist on disk inaccessible through us. A mismatch in the dangerous direction (advertised legal, actually rejected) puts a client's sync into a permanent retry loop — that direction must never happen. Verified against `sc-vfs::safe_path::validate_component`: it rejects `:` (matching the list), NUL and control bytes 0x01–0x1F/0x7F, a trailing `.`/space, and Windows device names (`CON`, `PRN`, `COM1`–`9`, `LPT1`–`9`) — none of which overlap with the other eight listed characters (`\ / * ? " < > |`), which `SafePath` does not actually reject at the character level. The list is therefore a strict superset of what is enforced: over-conservative for those eight (a client that follows it will simply never try names we would in fact accept), never under-conservative. The one true gap — control characters and the trailing-dot/space and reserved-name rules have no character-list representation at all — is the same gap the reference server's own list has, not a divergence we introduced. The real, survivable cost of the superset direction: a file already on disk whose name contains one of those eight characters (written by Samba, NFS, or another service sharing the directory) is listed to an NC client, which considers the name invalid per this list and declines to sync just that one file — a known gap, not a retry loop, because the client never tries to *create* that name itself.

### 5.1 "Presence = on" keys (found in the mobile audit)

The exception to rule 1. These keys are read for **existence, not value** — `{}` still turns the feature on. Omit the key entirely to keep it off.

| Key | Evidence |
|---|---|
| `activity` | Android `GetCapabilitiesRemoteOperation.java:643-648`: `if (respCapabilities.has(NODE_ACTIVITY)) setActivity(TRUE)` · iOS `+Capabilities.swift:413`: `activityEnabled = json.activity != nil` |
| `external` | iOS `:429`: `externalSites = json.external != nil` |
| `governance` | iOS `:416`. The model is `struct Governance: Codable {}` — **no field exists** to express "off" at all |
| `richdocuments` | iOS `:407`. Also reads `mimetypes` with `getJSONArray` unconditionally, so on Android its absence throws |

An earlier draft sent `"activity": {"apiv2": []}`. Correct for desktop, but on both mobile clients it turns the activity UI on and starts polling an endpoint we answer with 404. **The key was removed.**

### 5.2 Keys whose absence discards the whole response

Android parses with `org.json`, where `getString`/`getInt`/`getBoolean` **throw** on a missing key, and the entirety of `parseResponse` sits behind one `catch (JSONException | IOException)` (`GetCapabilitiesRemoteOperation.java:252-254`). **One missing key does not disable one feature — it discards the whole capabilities response.**

These are read without a `has()` guard — required whenever their parent object is present at all:

| Parent | Required child | Line |
|---|---|---|
| `core` | `pollinterval` | :371 |
| `files_sharing` | `resharing` | :442 |
| `files_sharing.public` | `enabled` | :396 |
| `files_sharing.user` | `send_mail` | :438 |
| `files_sharing.federation` | `outgoing`, `incoming` | :446, :448 |
| `files` | `bigfilechunking` | :458 |
| `files.directEditing` | `etag` | :477 |
| `theming` | `name`, `slogan`, `color` | :513-515 |
| `end-to-end-encryption` | `enabled`, `api-version` | :619, :636 |
| `version` | `major`, `minor`, `micro`, `string`, `edition` | :347-351 |

iOS has the same trap in a different shape: `Codable`, with the four `version` fields and `files_sharing.public.enabled` **non-optional**, so any one missing throws `JSONDecoder.decode` into `.invalidData` (`+Capabilities.swift:109-116, 177, 57`). **Types are strict too** — `"major": "31"` (a number sent as a string) fails to decode.

Both rules are locked by golden tests in `capabilities.rs`.

### 5.3 Endpoint differences

| Client | Path |
|---|---|
| Android | `ocs/v2.php/cloud/capabilities?format=json` (`GetCapabilitiesRemoteOperation.java:42`) |
| iOS | **`ocs/v1.php/cloud/capabilities`** (`+Capabilities.swift:32`) |

**Both must be served.** Android sends `If-None-Match` and handles 304 correctly (`:226`, `isNotModified`). iOS does not make conditional requests here.

---

## 6. Login Flow v2

The standard login path for current desktop and mobile clients.

### 6.1 Sequence

```
① client
   POST /index.php/login/v2
   User-Agent: Mozilla/5.0 (…) Nextcloud-android/3.30
   → 200 {
       "poll": { "token": "<64 chars>", "endpoint": "https://host/index.php/login/v2/poll" },
       "login": "https://host/index.php/login/v2/flow/<flowtoken>"
     }

② client opens the login URL in the system browser
   GET /index.php/login/v2/flow/<flowtoken>
   → unauthenticated: redirect to our web login (returnTo preserved)
   → authenticated: consent screen "Nextcloud-android/3.30 is requesting access to your account"
      · client name comes from parsing User-Agent. HTML-escaped, mandatorily
      · the user may narrow the scope (read-only / share-limited)  ← an extension NC does not have
   → clicking [Approve] issues an app password and stores it in nc_login_flow.result

③ client repeats ①'s poll (a few seconds apart by default)
   POST /index.php/login/v2/poll   body: token=<poll token>
   → 404  (still pending — see §6.2 for what else this code covers)
   → 200 { "server":"https://host", "loginName":"alice", "appPassword":"stow_…" }
        consumed and deleted immediately after
```

### 6.1.1 Client differences (mobile audit)

| | desktop | iOS | Android |
|---|---|---|---|
| `token` location (poll) | form body | **query string** | body (unverified) |
| `OCS-APIRequest` header | sent | **not sent** | unverified |
| Request body (init/poll) | form | **empty** | unverified |
| Success test | 2xx | `200..<300`, strict | unverified |

iOS puts the poll token in the URL, not the body:

```swift
// iOS SDK +Login.swift:398
let serverUrl = endpoint + "?token=" + token
// :407  POST, parameters: nil  → empty body
```

Reading only the body makes this request indistinguishable from "no token" — permanent 404 — so **iOS could approve consent and never finish logging in.** We check query, JSON body and form body, all three (precedence: form body → JSON body → query string — a body value wins if both are present, since the reference documents the body as canonical and only iOS sends nothing there).

iOS also does not send `OCS-APIRequest` on login/v2 requests (`:329-334`). **Requiring that header on this path makes iOS login impossible;** §4's requirement applies only to `/ocs/` routes. Conversely, iOS *does* send `OCS-APIRequest: true` on WebDAV requests (PROPFIND/MKCOL/MOVE/PUT/…, `NKCommon.swift:336`) — **do not reject it on DAV routes either.**

Android sends the header name as `OCS-APIREQUEST` (all caps, `RemoteOperation.java:51`). HTTP headers are case-insensitive, so our matching must be too.

### 6.2 Security requirements

- `poll token` and `flow token` are **two independent 256-bit values**; only their SHA-256 digests are stored. Knowing the flow token must not be enough to poll.
- 20-minute expiry, swept.
- The poll endpoint is rate-limited to one poll per token per second. Unbounded polling is a database-scan DoS.
- **The consent screen must be approved by a human clicking something.** If a bare `GET` could approve, visiting an attacker's page while logged in would be enough to mint and hand over an app password — full account takeover from a drive-by image tag. Approval is a CSRF-protected `POST`.
- The consent screen shows the requesting IP and client name.
- Issuance is written to the audit log (`apppw.created`, `detail.via = "login_flow_v2"`) and to a user notification.
- The `login` URL's host comes from the **configured canonical URL**, never the request's `Host` — trusting `Host` turns this into a phishing redirect.

**The poll endpoint answers only `404` or `200` — nothing else.** Pending, unknown/expired, already-consumed, and throttled are all deliberately indistinguishable, exactly as the reference documents 404 ("not found or completed"). Distinguishing them would turn the poll endpoint into an oracle for which flow tokens are live.

This used to answer `429` when rate-limited, and that broke enrolment on a real Android phone three times. The app does not understand `429` on this endpoint: it stops polling and never asks again, then the human finishes consent, sees "Access granted, you may close this window" — and the app sits on its spinner forever, with nothing in any log to explain why, because the failure is a status code choice, not an error. `429` was invented for a wire that has exactly two answers a client understands.

Returning `404` for the throttled case still preserves the reason the rate limit exists: the check short-circuits **before** the store lookup (`login_flow.rs`), so an unbounded poll loop still costs no DB scan. Rate-limiting only stops us from telling the client something it cannot act on anyway.

---

## 7. WebDAV decorator

Wraps `sc-dav` rather than inheriting from it. The core handler builds the property set; NC properties are injected on top.

```rust
pub struct NcDav<D: DavService> { inner: D, meta: Arc<dyn MetaStore>, nc: Arc<NcStore> }

impl<D: DavService> DavService for NcDav<D> {
    fn propfind_props(&self, e: &Entry, req: &PropReq) -> PropSet {
        let mut p = self.inner.propfind_props(e, req);       // standard live properties
        if req.wants(NS_OC, "id")          { p.raw(NS_OC, "id", &nc_id(e.fileid, self.instance_id())) }
        if req.wants(NS_OC, "fileid")      { p.num(NS_OC, "fileid", e.fileid.0) }
        if req.wants(NS_OC, "permissions") { p.raw(NS_OC, "permissions", &oc_permissions(e)) }
        if req.wants(NS_OC, "size")        { p.num(NS_OC, "size", self.recursive_size(e)) }
        if req.wants(NS_OC, "favorite")    { p.num(NS_OC, "favorite", self.nc.is_favorite(e)) }
        if req.wants(NS_OC, "owner-id")    { p.text(NS_OC, "owner-id", &e.owner_name) }
        if req.wants(NS_OC, "share-types") { p.list(NS_OC, "share-types", self.share_types(e)) }
        if req.wants(NS_NC, "has-preview") { p.bool(NS_NC, "has-preview", self.can_preview(e)) }
        if req.wants(NS_NC, "mount-type")  { p.text(NS_NC, "mount-type", "") }
        if req.wants(NS_NC, "is-encrypted"){ p.num(NS_NC, "is-encrypted", 0) }
        p
    }
}
```

Namespaces: `oc` = `http://owncloud.org/ns`, `nc` = `http://nextcloud.org/ns`, `d` = `DAV:`.

**The `d:` prefix on every element we emit is lowercase**, matching what sabre/dav — and therefore every server derived from it — puts on the wire. This matters because iOS (its client SDK, via `SwiftyXMLParser`) does not resolve XML namespaces at all: it looks up elements by their literal name, `xml["d:multistatus", "d:response"]` (`NKDataFileXML.swift:287, 600, 686`), so `D:multistatus` and `d:multistatus` are different, unmatched strings to it. An implementation derived only from the desktop client's source once emitted `<D:multistatus>` — valid XML, correct for every namespace-aware parser (Android/Jackrabbit and desktop/`QXmlStreamReader` both use real namespace resolution and were unaffected) — and iOS saw every directory as empty while the request still reported `.success`, exactly the silent-failure mode this project is built to avoid. That gap is now closed: `sc-dav` emits `d:` throughout (`propfind.rs`, `proppatch.rs`), and `sc-dav/tests/dav.rs` asserts no `<D:` ever appears in a response body — the regression is a golden test, not a comment. See `proposals/stowcloud-4-webdav.md` for the fix at the protocol layer; that layer owns the serialization, this layer only owns which `oc:`/`nc:` properties ride along.

#### propstat order: 200 first

iOS reads **only the first `d:propstat`** (`NKDataFileXML.swift:333`, `[0]`). If a 404 block comes first, every property on that item silently vanishes for iOS. Android checks `status[0]`, falls through to `status[1]` if it is 404, and so tolerates either order (`WebdavEntry.kt:119-123`).

`sc-dav/src/propfind.rs:250-261` writes the 200 block first, correctly. **This order is a contract, not an accident.**

### 7.0 The property set mobile actually requests

| | properties requested | source |
|---|---|---|
| desktop | ~12 | `LsColJob::defaultProperties` |
| Android | 40 (`oc`/`nc` 33 + DAV 7) | `WebdavUtils.getAllPropSet()` (`WebdavUtils.java:85-131`), used on every folder listing (`ReadFolderRemoteOperation.java:60`) |
| iOS | 47 + DAV | `NKProperties.allCases` (`NKProperties.swift:11-66`) |

**A requested-but-unanswered property is tolerated by both** — both read with an `if prop != null` pattern and fall back to a default. So properties for features we do not implement (`nc:system-tags`, `nc:sharees`, `nc:lock-*`, `nc:metadata-photos-*`, `nc:share-download-limits`, …) are simply left in the 404 propstat.

What we answer beyond the reference minimum:

| Property | Reason |
|---|---|
| `nc:creation_time` | Real data we already have (`btime_ns`). iOS's photo grid sorts by this; without it, camera roll sorts by upload time instead of capture time |
| `nc:upload_time` | No separate tracking, so we answer with mtime |
| `oc:comments-unread` | `0`. Android reads this as a badge count |
| `nc:lock` | `0`, consistent with capabilities' `files.locking: false` |
| `nc:hidden` | `false` |

**Deliberately not answered: `nc:rich-workspace`.** Android distinguishes **absent → `null`** (feature off) from **present-empty-string → `""`** (renders an empty workspace) (`WebdavEntry.kt:360-370`). Silence is the feature here.

#### An empty element fails the whole folder listing

Android reads several properties with `(prop.value as String).toLong()`, catching only `NumberFormatException`. A truly empty `<nc:creation_time/>` throws a Kotlin **cast NPE** instead, which escapes that catch, propagates out of the `WebdavEntry` constructor, and makes `ReadFolderRemoteOperation` **fail the entire folder**.

Affected: `oc:size`, `oc:fileid`, `d:getcontentlength`, `nc:creation_time`, `nc:upload_time`, `nc:lock-owner-type`, `nc:trashbin-deletion-time`.

⇒ **Emit a number when there is one, omit the element entirely otherwise. Never emit an empty numeric element.** `oc:checksums` is the one exception (neither mobile parser reads it, and desktop treats an empty value as "no checksum"). `props.rs::no_numeric_property_is_ever_emitted_empty` enforces this.

#### Other parser constraints

- `oc:favorite` is compared against the string `"1"` (`WebdavEntry.kt:262`). We send integer 1/0, never `true`/`false`.
- `nc:has-preview` **absent reads as `true` on Android** (`WebdavEntry.kt:326-332`) — the only "absence = on" property the mobile audit found (as opposed to key). iOS is the opposite: absent reads as `false` (`NKDataFileXML.swift:444`). Always emit it explicitly.
- Android cuts `<d:href>` with `href.split(davBasePath, limit=2)[1]` (`WebdavEntry.kt:118`). If the href does not contain the DAV base path the client itself constructed, **literally**, that throws `ArrayIndexOutOfBoundsException` and fails the whole folder. We must echo back the `/remote.php/dav/files/{user}` prefix exactly as the client sent it.
- iOS requests `oc:downloadURL`, `oc:data-fingerprint` and `oc:checksums` under the `oc:` prefix but **parses them under `d:`** (`NKDataFileXML.swift:356, 360, 380`) — answering correctly still leaves iOS unable to read them, so we do not send them.
- iOS requests but never parses `d:getcontentlength` and `d:displayname`; size only ever comes from `oc:size`.

Path mapping:

| NC path | core |
|---|---|
| `/remote.php/dav/files/{user}/…` | virtual root, unchanged |
| `/remote.php/webdav/…` | alias of the above (legacy) |
| `/remote.php/dav/uploads/{user}/…` | upload session (§9) |
| `/remote.php/dav/principals/users/{user}` | minimal stub |
| `/remote.php/dav/` (PROPFIND Depth:0) | root collection |

### 7.1 `oc:id` / `oc:fileid`

```rust
fn nc_id(f: FileId, instance: &str) -> String { format!("{:08}{}", f.0, instance) }
// e.g. "00000123oc9k2m4x1p"
```

These must be **unique, stable and positive**: a client's local sync journal is keyed on them, and a duplicate id makes the client conclude two different resources are one — observed as files vanishing locally, or a permanent resync loop.

**Ids are allocated lazily, and not by every code path.** `Core::stat_entry` allocates a stable id on demand when an entry does not have one yet; `Core::list` never does — a plain directory listing must not write one `node` row per entry it walks (`DESIGN-FOOTPRINT.md` §2, on why a per-file path string or an eager per-listing allocation blows the storage budget). This is deliberate, not an oversight, but it means an entry that has never individually been stat'd, aggregated, or otherwise touched has no fileid yet — and "no id yet" must never degrade to a shared placeholder. It used to: `FileId(0)` was reused for every untouched entry, observed as 11 of 12 grant roots answering `oc:fileid=0` in the same listing. The property source (`NcPropBridge::emit`) now materializes a real id through `Core::ensure_fileid` at emission time whenever a request actually asks for `oc:id`/`oc:fileid` and the entry doesn't have one — on demand, only for requests that need identity, so the lazy-allocation default stays intact for everything else.

**Share roots have no `node` row to own an id at all** — a share's physical root sits above every real file-system entry `sc-meta` tracks, so `ensure_fileid` returns the sentinel `FileId(0)` for it by construction, not as an error. Left alone, every share's root would report the same id. Instead, share roots draw from a reserved, real-row-disjoint range:

- `1u64 << 62` OR'd with the `ShareId` — a share's own root.
- `1u64 << 61` — the synthetic files-root collection itself (§7.4), a distinct bit rather than reusing bit 62 with share id 0, since nothing in the type system actually guarantees `ShareId` is never zero.

Both are plain positive integers — not negative sentinels — because `oc:id`'s formatting treats the id as an unsigned zero-padded value, and a negative id is a value no reference client's parser was ever written to expect (`i64::MIN` in particular is actively dangerous on the wire: negating it overflows, `abs()` panics in most languages). `1 << 62` is a value only a database north of four quintillion rows could ever reach through ordinary allocation — not a hard guarantee, but disjoint in every practical sense.

**The instance id is a suffix of every `oc:id` this server has ever emitted.**

> #### Changing it forces a full resync on every connected client.
>
> Every client keys its local sync journal on `oc:id`. A new instance id makes every file look brand new: every client discards its journal and re-downloads everything. For a large deployment that is terabytes of traffic and hours of unavailability, and it happens silently — nothing errors, sync just restarts from zero.
>
> It is generated once, on first startup, and stored in `nc_instance`. **It must be in your backups and must be restored verbatim.** Restoring file data without `nc_instance` is not a restore.

### 7.2 `oc:permissions` mapping

Getting this wrong makes desktop clients refuse to sync **without reporting an error** — the hardest failure mode to debug in the whole compatibility surface. Pinned to a table and locked by an integration test.

| Letter | Meaning | Our condition |
|---|---|---|
| `S` | shared | this entry has an active share link/grant |
| `R` | shareable | `Perms::SHARE` |
| `M` | mounted (external storage) | never emitted — we have no external storage concept |
| `G` | readable | `Perms::READ` |
| `D` | deletable | `Perms::DELETE` |
| `N` | renameable | `Perms::RENAME` |
| `V` | movable | `Perms::MOVE` |
| `C` | can create a file in this collection | directory + `Perms::CREATE` |
| `K` | can create a directory in this collection | directory + `Perms::CREATE` |
| `W` | can write file content | file + `Perms::WRITE` |

```rust
fn oc_permissions(e: &Entry) -> String {
    let mut s = String::with_capacity(8);
    if e.shared                     { s.push('S') }
    if e.perms.contains(P::SHARE)   { s.push('R') }
    if e.perms.contains(P::READ)    { s.push('G') }
    if e.perms.contains(P::DELETE)  { s.push('D') }
    if e.perms.contains(P::RENAME)  { s.push('N') }
    if e.perms.contains(P::MOVE)    { s.push('V') }
    if e.is_dir {
        if e.perms.contains(P::CREATE) { s.push('C'); s.push('K') }
    } else if e.perms.contains(P::WRITE) { s.push('W') }
    s
}
```

The letter order is not alphabetical and is not free to choose — it is taken verbatim from the reference's `DavUtil::getDavPermissions()`, because some clients string-compare the value rather than checking for individual letters.

Consequences of getting one letter wrong:

- No `W` on a file → the client treats it as read-only and never uploads local edits.
- No `N`/`V` → rename becomes delete + re-upload; renaming a 1 GB directory re-uploads 1 GB.
- No `C`/`K` on a directory → nothing can be created inside it.
- **A read-only share's own root must still carry `G`.** An empty permission string makes the client ignore the entry outright.

### 7.3 ETag

`d:getetag` **must include the double quotes.** A directory's ETag must change whenever any descendant changes (`proposals/stowcloud-2-core-vfs.md`) — without that property, desktop sync effectively does not work.

`oc:checksums` needs a content hash, so it is **empty by default**; clients skip verification when it's absent. Only when a share sets `compute_checksums = true` do we compute it at upload time, cache it in `sc-meta`, and expose it here.

### 7.4 The synthesized files root

`PROPFIND /remote.php/dav/files/{user}/` (and its `/remote.php/webdav/` alias), addressed at the empty relative path — the caller's files root itself — is a real client's very first request after Login Flow v2 finishes enrolment.

`sc-core` has no vpath meaning "root of everything": every vpath names a grant-projected label as its first segment, and an empty label is unconditionally `NotFound` regardless of how many shares exist or who has grants on them (`resolve.rs`). Handed straight to `sc_dav::DavService` the way every other compat request is, this PROPFIND 404s — which it did, for every user, on the very first request they ever made. There is no single "home directory" the way the reference server's default install has one: a user can be granted several roots, each a distinct label, with no prior notion of which one is "home" reaching this code path at all.

The honest answer, and the one real clients already handle correctly (it is exactly how the reference server's own external-storage / group-folder mounts appear on the wire), is a hand-assembled multistatus whose children are the caller's grant-projected roots — real `oc:permissions`, `oc:fileid`, and friends pulled from `sc-core`, not placeholders, so a client that stats a child here sees the same answer it gets PROPFINDing that root directly afterward.

The collection's own row (not any child):

- `oc:permissions: G` only — read, nothing else. There is nothing to write "next to" the caller's roots: this collection has no filesystem backing of its own for `CREATE`/`WRITE`/`DELETE`/`RENAME`/`MOVE` to mean anything about, and a permissive string here would offer an upload that can only fail. A `PUT`/`MKCOL` aimed at the root itself answers `405 Method Not Allowed` with `Allow: OPTIONS, PROPFIND, HEAD` — not `404`, which the `HEAD`/`PROPFIND` responses above already disprove (`h_remote`, `sc-server/src/nc.rs`); this matches sabre-dav's own `MethodNotAllowed` for a `PUT`/`MKCOL` against an existing, non-writable collection.
- `quota-available-bytes` / `quota-used-bytes`: `-2`/`-2` — the reference server's own `FileInfo::SPACE_UNKNOWN` sentinel. That server's root is a single real mount and can report a real number; this collection's children can be different shares on different filesystems, so there is no honest single figure to fold them into. `-2` is already part of the vocabulary real clients parse, so it is used verbatim rather than fabricating a sum.
- `getetag`: hashed over every root's `(label, etag)` pair, not left absent — absent, a client loses its "anything to re-list?" shortcut and re-walks the whole account every pass; a constant would be worse than absent, since it would claim nothing ever changes. This hash changes exactly when a grant is added or removed, or a root's own top-level content changes.
- `oc:id`/`oc:fileid`: the `1u64 << 61` pseudo-id from §7.1, not left as a 404 — it is the value a client's sync journal is actually keyed on, and this is the first response a real client ever sees.

**Requested-but-unanswerable properties get their own second `propstat`, at `404`, per RFC 4918 §9.1** — only when the client named properties explicitly rather than sending `allprop`/`propname`, the same rule the ordinary PROPFIND path applies. Before this, an unanswerable property on this synthetic row was simply absent from the response with no propstat at all, which several clients read as "the server is broken" rather than "not applicable here."

---

## 8. Users and quota

```
GET /ocs/{v1,v2}.php/cloud/user?format=json
data: { "id":"alice", "display-name":"Alice", "email":null, "enabled":true,
        "quota": { "free":…, "used":…, "total":…, "relative":…, "quota":… },
        "groups":["…"], "language":"ko", "locale":"ko_KR" }
```

When unlimited, the reference sets `free` and `total`, not just `quota`, to `-3`, with `relative: 0`. An earlier draft filled `free`/`total` with real byte counts, which makes clients draw a usage bar that means nothing. We follow the reference: real `statvfs`-derived numbers only when a quota is actually configured. **Which share to report is ambiguous when a user has more than one** — the default home share if one exists, otherwise the first accessible share. The admin UI can pin a "quota reporting share" explicitly.

---

## 9. Chunked upload

Full mapping in `proposals/stowcloud-7-upload.md` Summary:

| NC request | core engine |
|---|---|
| `MKCOL /remote.php/dav/uploads/{user}/{tid}` | `create(mode = NameOrdered)`. `{tid}` maps through `nc_upload_alias` to an internal `SessionId` — **a client-controlled value is never used as the session key directly** |
| `PUT …/{tid}/{name}` | in-order arrival appends straight to the part file (zero copies); out-of-order spools |
| `MOVE …/{tid}/.file` | spooled chunks merged in name order via `copy_file_range`, then the common finalize path |
| `DELETE …/{tid}` | abort |
| `PROPFIND …/{tid}` | list of received chunks (client resume) |

`X-OC-Mtime` → `utimensat` restores the original mtime. `OC-Total-Length` is verified when present.

#### Response conditions that make a client hard-fail (found against the reference during implementation)

Not in an earlier draft. Violating these fails sync silently — no error, just a stall — which is the hardest failure mode in this project.

| Requirement | Consequence if violated |
|---|---|
| `MKCOL` answers **exactly 201** | Desktop aborts the whole transfer if `_httpErrorCode != 201` — 200 or 204 do not satisfy it |
| Final `MOVE` carries **both `OC-FileId` and an ETag** | Missing either makes desktop hard-fail the item even on a 201 |

Android does not check the MKCOL status at all (`ChunkedFileUploadRemoteOperation.java:161` discards the return value) and goes straight to PROPFIND. iOS PROPFINDs first and only sends MKCOL when that answers 404, treating anything but 2xx as failure — so for iOS, **405 ("already exists") is a failure** (`+Upload.swift:276-283`). Answering 201 for a new session and 207 to a PROPFIND on an existing one satisfies all three clients.

#### The real header set

`OC-Total-File-Length` **does not exist in any client codebase** — an earlier draft invented it. What actually travels:

- `PUT` → `OC-Chunk-Offset`, `Destination`, `OC-Total-Length`
- `MOVE` → `OC-Total-Length`, `X-OC-Mtime`, `OC-Checksum`, `If`

…on **desktop**. Mobile differs (§9.1).

### 9.1 Mobile chunked upload (audit findings)

#### Constants

| | chunk size | threshold | parallelism | naming | transfer id |
|---|---|---|---|---|---|
| desktop | dynamic | — | parallel | — | random |
| **Android** | **10,240,000** (cellular) / **40,960,000** (WiFi) | `> 10,240,000` | **1 (sequential)** | `%06d`, from 1 → `000001` | **`md5(file)`** |
| **iOS** | app-injected (no library constant) | app's own decision | **1 (sequential)** | `String(n)`, from 1 → `1` | UUID |

Sources: `ChunkedFileUploadRemoteOperation.java:42-43, 120-128, 197-211, 280` · `UploadFileOperation.java:1129-1137` · `NKCommon.swift:123, 180, 204-205` · `+Upload.swift:322-393`.

- Decimal, not binary — 10,240,000 ≠ 10 MiB.
- **40,960,000-byte (~39 MiB) chunks arrive on WiFi.** No server-side chunk ceiling (`proposals/stowcloud-7-upload.md`) makes this fine on our end, but any intermediary proxy's `client_max_body_size` must be at least this large — Android has no desktop-style 413 auto-adjust and simply fails on 413.
- **iOS chunk names are not zero-padded.** Lexicographic order would give `1, 10, 11, 2, …`, breaking assembly. We parse names as `u32` and assemble in numeric order, tested.
- Android's `md5(file)` transfer id is stable across retries and app restarts. An alias must be released on assembly or abort, or a retried upload of the same file collides with a dead session.

#### Per-request headers — actual client sets

| Request | desktop | Android | iOS |
|---|---|---|---|
| `MKCOL` | `Destination`, `OC-Total-Length` | `Destination` | `Destination`, `OC-Total-Length` |
| `PUT {n}` | `Destination`, `OC-Total-Length` | `Destination` | `Destination`, `OC-Total-Length` |
| `MOVE .file` | `OC-Total-Length`, `X-OC-Mtime`, `OC-Checksum` | **`X-OC-Mtime`, `X-OC-Ctime` only** | `Destination`, `Overwrite: T`, `OC-Total-Length`, `X-OC-MTime`, `X-OC-CTime` |

Two things were broken here:

1. **`OC-Total-Length` was required on MOVE.** Android never sends it (`ChunkedFileUploadRemoteOperation.java:216-225`). ⇒ **every Android upload over 10,240,000 bytes failed with a 400 at assembly, after the full transfer had already completed.** Now a missing header falls back to the engine's actual contiguous received length; when the header **is** present, a mismatch is still a 400 — the invariant "never silently finalize a truncated upload" is unchanged.

2. **`X-OC-Mtime` was parsed as an integer only.** iOS formats it as a Swift `Double`:
   ```swift
   // iOS SDK +Upload.swift:404-406
   options.customHeader?["X-OC-MTime"] = "\(date.timeIntervalSince1970)"   // → 1751234567.891234
   ```
   ⇒ **every iOS chunked upload failed assembly with a 400.** Now truncated at the decimal point; a genuinely non-numeric value is still a 400.

`X-OC-Ctime`/`X-OC-CTime` are sent by both mobile clients but ignored (no portable way to set creation time). Not a failure cause.

#### PROPFIND on the upload folder — not optional for Android

Desktop only sends this on resume. **Android sends it unconditionally right after MKCOL, on every chunked upload, and aborts if it fails** (`ChunkedFileUploadRemoteOperation.java:164-172`). It derives its starting offset purely from the response:

```java
// :178-194
long nextByte = 0;  int lastId = 0;
for (MultiStatusResponse response : dataInServer.getResponses()) {
    …
    if (id > lastId) { lastId = id; }
    nextByte += we.getContentLength();      // ← d:getcontentlength
}
```

We used to emit only `<D:resourcetype/>` per chunk. `getContentLength()` came back 0, so a resuming client sent bytes from offset 0 while continuing chunk numbering from `lastId+1` — assembly produced a file with its beginning duplicated, silently. We now emit `d:getcontentlength`, `d:getcontenttype`, `d:resourcetype`, and the collection itself (`chunking::chunk_listing_xml`).

### 9.2 Remaining limitation: exact per-chunk size

`sc_upload::UploadEngine` exposes no per-chunk size table, only the contiguous received length (`SessionStatus::offset`). The listing's per-item length is the total divided evenly across the chunk count — **the total is correct**, and all three clients only ever use the total and the highest chunk number, so behavior is unaffected. An accurate per-chunk figure needs a new lookup API on `sc-upload` (core change).

#### `nc:is-encrypted`

The reference server master has no such property on the core (it belongs to the separate `end_to_end_encryption` app), but desktop requests it on every discovery PROPFIND (`LsColJob::defaultProperties`), as do Android (`WebdavUtils.java:101`) and iOS (`NKProperties.swift`) — so we answer `0`.

---

## 10. Share API

```
GET    /ocs/v2.php/apps/files_sharing/api/v1/shares[?path=&reshares=&subfiles=]
POST   /ocs/v2.php/apps/files_sharing/api/v1/shares
PUT    /ocs/v2.php/apps/files_sharing/api/v1/shares/{id}
DELETE /ocs/v2.php/apps/files_sharing/api/v1/shares/{id}
GET    /ocs/v2.php/apps/files_sharing/api/v1/sharees?search=&itemType=file
```

`shareType`: `0` = user, `1` = group, `3` = public link. Unsupported types (`4` email, `6` federated, `10` room) get `400` on creation and never appear in listings.

**User and group grants are refused outright, not silently downgraded.** `sc-acl` grants are administrator-owned with no per-user CRUD anywhere in this server, so `shareType` 0/1 return an explicit error rather than a share the client is told exists and can never find again — a client told creation failed is better off than one told it succeeded.

Response objects translate core `Link`/`Grant` into NC's shape:

```json
{ "id":"12", "share_type":3, "uid_owner":"alice", "displayname_owner":"Alice",
  "permissions":1, "stime":1753600000, "expiration":"2026-08-27 00:00:00",
  "token":"aB3xQ…", "path":"/Photos/Summer", "item_type":"folder",
  "mimetype":"httpd/unix-directory", "file_source":123, "file_target":"/Summer",
  "url":"https://host/s/aB3xQ…", "password":null, "label":"" }
```

`permissions` is the NC bitmask (1=read, 2=update, 4=create, 8=delete, 16=share), translated from our `Perms`. On the way back, a bit we do not support is rejected with `400`, never silently dropped — dropping it would grant less than requested while reporting success.

**`token` is `None` on list/get.** The plaintext exists once, at creation; only `sha256(token)` is kept, so no later read can reproduce it.

`sharees` is user/group autocomplete. **Partial-match search is an account enumeration oracle** — minimum 3 characters, rate-limited, and the admin can scope it with `sharee_lookup = same_group | all | off` (default `same_group`).

---

## 11. Preview

```
GET /index.php/core/preview?fileId=123&x=256&y=256&a=1&forceIcon=0&mode=cover
GET /index.php/core/preview.png?file=/path&x=256&y=256
```

Internally calls the thumbnail pipeline in `proposals/stowcloud-6-preview-sharing.md`, and **answers with a 302 redirect to a signed URL on the content origin.** Mobile clients follow redirects transparently, and image bytes never leave the app origin — the property that matters here.

`a=1` preserves aspect ratio, `mode=cover` crops. `forceIcon=1` asks for an icon fallback when no preview exists — we answer `404` and let the client draw its own icon.

### 11.1 What mobile actually requests (audit findings)

Mobile depends on previews far more than desktop — Android's photo grid and iOS's media browser are **entirely** thumbnails. One misread parameter is a wall of blank tiles.

| Client | Request |
|---|---|
| Android files/photos | `/index.php/core/preview?fileId={oc:fileid}&x=&y=&a=1&mode=cover&forceIcon=0` (`ThumbnailsCacheManager.java:648-650`) |
| Android path-based | `/index.php/apps/files/api/v1/thumbnail/{x}/{y}/{path}` (`:1209-1211`) |
| Android trash | `/index.php/apps/files_trashbin/preview?fileId=&x=&y=` (`:652-653`) |
| Android avatar | `/index.php/avatar/{user}/{px}` (`:958`) |
| iOS preview | `/index.php/core/preview?fileId=&x=1024&y=1024&a=1&mode=cover&forceIcon=0&mimeFallback=0&etag=` (`+API.swift:443`) |
| iOS trash | `/index.php/apps/files_trashbin/preview?…` (defaults 512×512, no `etag`) (`:554`) |
| iOS avatar | `/index.php/avatar/{user}/{size}` (`:649`) |

- `fileId` here is **`oc:fileid`, a plain integer**, not `oc:id`.
- Only iOS appends `mimeFallback` and `etag`. `etag` is the client's own URL-cache-invalidation key and may be ignored server-side (`:441` comment) — **but a parse failure on it must not reset the requested dimensions to defaults.**
- Android sends `Cookie: nc_sameSiteCookielax=true;nc_sameSiteCookiestrict=true` and `OCS-APIREQUEST: true`; both must be harmlessly ignorable on this path.
- Path-based thumbnails and avatars are additionally routed. There is no avatar storage, so avatars answer **404** — but a routed 404 matters: iOS feeds the response body straight into an image decoder and force-unwraps `image.cgImage!` on macOS (`:690`), so a clean 404 is safer here than an HTML error page reaching that code.

#### The redirect must be 302

Android disables its HTTP stack's own redirect following (`OwnCloudClient.java:190`) and loops manually, handling only **301, 302, 307** (`:235-238`, max 3 hops). **303 and 308 are not followed** and are returned as-is, failing the `status == SC_OK` check and discarding the thumbnail. iOS is unaffected (default `URLSession` behavior). We use 302, tested.

#### Must never answer 304

iOS sends `If-None-Match` on preview/avatar requests and validates against `200..<300` (`+API.swift:450-455`, `661`) — **`304 Not Modified` is an error to iOS** (`NKError` 304), discarding the thumbnail on every scroll. Conditional responses are not used on preview paths, including the content origin behind the 302 — **a `304` from `/c/{token}` breaks every iOS thumbnail.**

(Capabilities are the opposite: Android sends `If-None-Match` and handles 304 correctly, so 304 helps there. §5.3.)

---

## 12. Stub endpoints

Endpoints that make a client retry noisily or show an error if absent. All answer with an **empty success**.

| Path | Response |
|---|---|
| `/ocs/v2.php/apps/notifications/api/v2/notifications` | `data: []` |
| `/ocs/v2.php/apps/user_status/api/v1/user_status` | `404` + `enabled:false` in capabilities (not `data: {}` — Android's `GetStatusRemoteOperation` only handles a 404 safely; `{}` deserializes into a non-nullable `Status.status` field via `Unsafe.allocateInstance()`, leaving it null and crashing the first thing that reads it) |
| `/ocs/v2.php/apps/user_status/api/v1/statuses` | `data: []` (a list type, so empty is safe) |
| `/ocs/v2.php/core/navigation/apps` | `data: []` |
| `/ocs/v2.php/apps/activity/api/v2/activity` | `404` (consistent with advertising an empty array in capabilities) |
| `/ocs/v1.php/cloud/capabilities` | same data as v2, v1 envelope |
| `/index.php/apps/files/` | 302 to the web UI |
| `/ocs/v2.php/apps/dav/api/v1/direct` | `404` |

---

## 13. Non-goals

Explicitly not implemented, in both the documentation and capabilities:

App store / server-side encryption / versioning / comments / tags / Talk / groupware (Calendar, Contacts, CalDAV, CardDAV) / federation / Collabora or OnlyOffice integration / notify_push / activity stream / external storage mounts / workflows.

**Full compatibility stops at sync, browse, share, preview.** A request beyond that boundary is not a gap in the compat layer — it is rebuilding the server this layer only pretends to be.

---

## 14. Verification

| Item | Method |
|---|---|
| Desktop sync | CI container, real desktop-client CLI round-trip. Create/modify/rename/delete/conflict scenarios |
| Rename optimization | Rename a 1 GB directory, verify by network bytes that **no re-upload happens** (`N`/`V` permission regression) |
| Directory ETag | Modify a deep file, root PROPFIND ETag changes |
| Chunked upload | 1 GB upload, fast-path hit rate logged, zero spool remnants |
| 413 path | Over-limit chunk → client auto-adjusts, succeeds |
| Login Flow v2 | Headless browser approves → app password issued → DAV access with that password |
| Android/iOS | Real device or emulator smoke test (manual, pre-release) — see §15 for what still needs it |
| Isolation | §1.3's CI gates |
| Permission string | Golden test over every `Perms` combination × file/directory for `oc_permissions` |
| Mobile capabilities | §5.1 "presence = on" keys absent, §5.2 required keys present — golden tests in `capabilities.rs` |
| Mobile chunked upload | Android's and iOS's full request sequences, headers included, reproduced in `chunking.rs` |
| No empty properties | `props.rs::no_numeric_property_is_ever_emitted_empty` |

---

## 15. Open items

### 15.1 Needs a core change (left out of scope)

| Item | Impact | Location |
|---|---|---|
| Per-chunk exact size lookup | None today (the total is correct, so behavior is unaffected) — an accuracy improvement only | Needs a lookup API on `sc_upload::UploadEngine`. §9.2 |

Two items that used to be listed here are resolved and removed from this table:

- **`<D:multistatus>` → `<d:multistatus>`.** This *was* the largest iOS compatibility defect — every directory reported empty with no error — and *was* filed as a core (`sc-dav`) change out of scope. It has since been fixed in `sc-dav` itself (`propfind.rs`, `proppatch.rs`) and is locked by a regression test asserting no `<D:` ever appears in a response (`sc-dav/tests/dav.rs`). See §7.
- **`X-OC-Mtime` on a plain (non-chunked) `PUT`.** Also filed as needing a core change, on the reasoning that `sc-dav`'s PUT handler doesn't know this header. It turned out not to need one: the compat layer now reads the header *before* delegating the write to `sc-dav`, and applies the timestamp *after* a successful write through `sc_vfs::ShareRoot::set_times` — the same primitive the chunked path already used (`nc::h_put_files`). `sc-dav` still knows nothing about `X-OC-Mtime`, preserving the isolation contract (§1.3). This mattered specifically for Android's camera-roll auto-upload, since Android's chunking threshold (10,240,000 bytes) puts most phone photos on the plain PUT path — before this fix, every auto-uploaded photo's mtime was upload time, not capture time, and the gallery sorted wrong.

### 15.2 (closed)

Used to track the plain-`PUT` mtime gap. Folded into §15.1 above now that it's fixed — kept as a number rather than deleted so nothing that ever cited it by section lands on the wrong paragraph.

### 15.3 Only verifiable on real hardware

- **Android Login Flow v2.** The Android client clone available for source audit is partial (`client/network`, `datamodel`, `operations` only — no login activity), so whether Android sends the poll token as a body field or a query parameter is **unverified**. The current implementation accepts body, JSON, and query, so it should work either way — confirmation is still needed.
- That iOS's `SwiftyXMLParser` looks up elements by case-sensitive literal name is inferred from the XML spec and the library's code; §7's conclusion (lowercase `d:` required) is backed by "the real server sends lowercase and iOS hardcodes it," but final confirmation is a device's job.
- Whether 40,960,000-byte chunks (Android on WiFi) pass through the actual deployment proxy configuration.
- Whether the content origin behind the 302 redirect actually returns image bytes to both clients — in particular, whether Android re-sends its `Authorization` header across the redirect.
