# WebDAV

`sc-dav`. **Protocol-pure implementation** — not one byte of compat vocabulary lives here. NC extensions are layered on top as a decorator, in `sc-compat-nc`.

RFC 4918 Class 2 (including LOCK) + RFC 4331 (quota).

---

## 1. Why Class 2 is mandatory

Stopping at Class 1 (no LOCK) is not an option:

- **macOS Finder** mounts read-only, or refuses to mount at all, without a `DAV: 1,2` advertisement.
- **MS Office** takes a LOCK on save; if it fails, the user sees "someone else is editing this" and the save is blocked.

We document honestly what this lock actually is: a logical lock valid only inside our own database. Jellyfin or rsync working the same directory never sees it. Real protection against concurrent writers comes from `If-Match`-based optimistic concurrency, not from LOCK.

---

## 2. Methods

| Method | Behavior | Notes |
|---|---|---|
| `OPTIONS` | `DAV: 1, 2, 3`, `MS-Author-Via: DAV`, `Allow:` | must be answerable unauthenticated, or Windows never mounts (§7) |
| `PROPFIND` | Depth 0/1. **Depth infinity is refused by default** — `403` + `<DAV:propfind-finite-depth/>` (an RFC 4918-sanctioned response). `dav.allow_infinite_depth = true` opens it, subject to an entry cap (`dav.infinite_depth_max_entries`, default 50000). Unbounded, Depth infinity on a million-file tree is a single-request DoS | streamed generation (§4) |
| `PROPPATCH` | stores dead properties, maps a few onto live ones | required for Office's Win32 properties (§7) |
| `MKCOL` | `415` if a body is present | |
| `GET`/`HEAD` | Range, conditional requests, `sendfile` | forces `Content-Disposition: attachment` |
| `PUT` | atomic replace (same path as `DESIGN-CORE.md` §5.3) | `If-Match`/`If-None-Match` |
| `DELETE` | trash policy applies | Depth infinity is implicit |
| `COPY`/`MOVE` | `Destination`, `Overwrite` | cross-mount handling (§6) |
| `LOCK`/`UNLOCK` | exclusive write lock, Depth 0/infinity | §5 |
| `REPORT` | **not implemented** | out of scope for the core; NC's search REPORT lives in the compat layer |

---

## 3. XML parser hardening

The largest remote attack surface in this server. `quick-xml`'s `NsReader`, with these forced on:

```rust
pub fn dav_reader(body: &[u8]) -> Result<NsReader<&[u8]>, DavError> {
    if body.len() > cfg.dav.max_request_body { return Err(TooLarge) }   // 1 MiB default
    let mut r = NsReader::from_reader(body);
    r.config_mut().check_end_names   = true;
    r.config_mut().expand_empty_elements = false;
    r.config_mut().trim_text(true);
    Ok(r)
}

// In the event loop, unconditionally:
match ev {
    Event::DocType(_) => return Err(DtdForbidden),   // ← blocks XXE / billion laughs outright
    Event::PI(_)      => return Err(PiForbidden),
    Event::Start(_)   => { depth += 1; if depth > 64 { return Err(TooDeep) } }
    Event::End(_)     => { depth -= 1; }
    _ => {}
}
```

- **A DTD event fails the request immediately.** No attempt is made to "safely" expand entities — WebDAV has no legitimate reason to need a DTD.
- Caps on element count (10000 default), depth (64), and attribute name length.
- Namespace-aware parsing is mandatory. Clients use `D:`, `d:`, `a:`, or no prefix at all — a literal prefix comparison on the *input* side will always eventually break.
- Fuzz targets: every PROPFIND/PROPPATCH/LOCK body parser.

### 3.1 Liberal on input, `d:` lowercase fixed on output

Incoming requests are resolved by namespace as above, but **outgoing XML always declares the prefix as lowercase `d:`.** The spec allows any prefix; reality does not.

The iOS client SDK looks up elements by **literal string**, via SwiftyXMLParser — `NKDataFileXML.swift:287` does `xml["d:multistatus", "d:response"]`, and there is no namespace handling anywhere in that library. XML element names are case-sensitive, so emitting `D:multistatus` gets **zero matches: iOS sees every directory as empty while the request still reports success.** No error reaches the user; their data just appears to have vanished.

Lowercase is what sabre/dav emits, and therefore what every server built on it sends (`SyncServiceTest.php:89`). None of the three clients audited hardcode uppercase — desktop and Android (Jackrabbit) resolve namespaces properly and are indifferent either way. **Matching the reference implementation's wire format is always the safer choice.**

Changing the prefix means changing its `xmlns:` declaration too. Change only the element and the prefix becomes undeclared, which breaks the XML for **every** client, not just iOS. A test that only checks for a substring like `<d:getetag>` passes right through this mistake, so the regression guard checks the actual PROPFIND output for both properties at once: ① no uppercase prefix appears, and ② every prefix used is declared (`sc-dav/tests/dav.rs`).

---

## 4. PROPFIND streaming

Building a 100,000-entry multistatus as a DOM costs hundreds of MB. **Streamed over a channel instead.**

```rust
async fn propfind(req: DavReq) -> Response {
    let (tx, rx) = mpsc::channel::<Bytes>(8);
    tokio::spawn(async move {
        let mut w = Writer::new(BufWriter::with_capacity(64 * 1024, ChanWriter(tx)));
        w.multistatus_open()?;                       // <?xml…><d:multistatus xmlns:d=…>
        w.response(&self_entry)?;
        if depth >= 1 {
            let mut it = vfs.read_dir(&dir)?;        // getdents64, streamed
            while let Some(e) = it.next() {
                if !acl.can_read(&e) { continue }    // no permission to list: omit the entry entirely
                w.response(&stat_and_props(e)?)?;    // auto-flushes at 64 KiB
            }
        }
        w.multistatus_close()
    });
    Response::builder().status(207)
        .header(CONTENT_TYPE, "application/xml; charset=utf-8")
        .body(StreamBody::new(rx))
}
```

- No `Content-Length` is computed; the body is chunked. Windows clients have been reported to dislike chunked 207 responses, so `dav.buffer_propfind` buffers and attaches `Content-Length` below a configurable entry count (1000 by default).
- An error mid-stream happens after the 207 header is already sent. The affected entry's `<d:status>` carries the error and the stream continues; a fatal error closes the stream instead (the client detects incomplete XML).
- **Entries the caller cannot list are simply absent from the response.** A `403` row would leak that the entry exists.

### 4.1 Live properties

| Property | Value |
|---|---|
| `getetag` | `"3f2a…"` — **quotes included**. Omitting them makes several clients refuse to sync |
| `getcontentlength` | files only |
| `getlastmodified` | RFC 1123 (`Tue, 27 Jul 2026 09:00:00 GMT`) |
| `creationdate` | ISO 8601, only when `statx` reports a btime |
| `resourcetype` | `<d:collection/>` for a collection |
| `getcontenttype` | guessed from extension, `application/octet-stream` otherwise |
| `supportedlock` | exclusive/shared write |
| `lockdiscovery` | active locks |
| `quota-available-bytes` / `quota-used-bytes` | RFC 4331. **Without these, Finder reports zero free space and refuses to copy** |

`allprop` returns exactly the list above (RFC allows omitting the full set of dead properties). `propname` returns names only.

### 4.2 Dead properties

Anything set via `PROPPATCH` is stored in `sc-meta`, **keyed by fileid**:

```sql
CREATE TABLE dav_prop (
  fileid INTEGER NOT NULL,
  ns     TEXT NOT NULL,
  name   TEXT NOT NULL,
  value  TEXT NOT NULL,          -- XML fragment, re-serialized on write to normalize it
  PRIMARY KEY (fileid, ns, name)
) WITHOUT ROWID;
```

**Never written as an xattr on disk.** Scattering xattrs across a shared folder breaks backup tools (rsync does not preserve them by default), other services, and filesystem migration. The value is re-serialized as XML on write, normalizing escaping — echoing the client's raw input back verbatim is an XML injection vector.

Keyed by fileid, so **a rename carries the property with it.** A GC sweep cleans up rows for files that no longer exist.

---

## 5. Locking

### 5.1 Model

```rust
pub struct DavLock {
    pub token:     Uuid,          // exposed as "urn:uuid:…"
    pub fileid:    FileId,        // locks a fileid, not a path → survives a rename
    pub principal: UserId,
    pub owner_xml: String,        // the client's <d:owner> content, re-serialized on storage
    pub depth:     Depth,         // Zero | Infinity
    pub scope:     LockScope,     // Exclusive | Shared
    pub expires_ns: i128,
}
```

- In-memory `DashMap` plus SQLite persistence. **Locks must survive a restart** or Office gets confused about who owns the document.
- `Timeout: Second-300` default, 3600 max. `Infinite` is refused and clamped to the max instead.
- 60-second expiry sweep, to bound how long a stale lock can block writers.
- A depth-infinity lock covers every descendant, so a write to a child must check its ancestors too. **The ancestor check compares path prefixes, not a fileid chain** — because fileid rows are allocated lazily (`ARCHITECTURE.md` §4.1), an ancestor may not have one yet, and minting one just to answer a lock check would defeat the point of lazy allocation. So `dav_lock` stores the virtual path at lock time, and the (small) active-lock set is scanned by prefix. The lock's own target identity is still fileid-based, so the lock survives the locked resource being renamed.

### 5.2 Lock-null resources

RFC 4918 deprecates lock-null resources in favor of "create an empty resource." A LOCK on a path that does not exist yet **creates a zero-byte file.**

That has a side effect in a shared folder — Jellyfin, for instance, can scan the zero-byte file. Mitigation: the created file is left alone unless its name starts with `.sclock-`, and **a GC sweep deletes it once the lock has expired and the file is still zero bytes.** This can be turned off per share (then such a LOCK answers `409` instead).

### 5.3 The `If` header

RFC 4918 §10.4's `If` header grammar — tagged lists, `Not`, state tokens mixed with ETags — is non-trivial, and clients actually send forms like `If: (<urn:uuid:…> ["etag"])`. Parsed properly, and **fuzzed.**

```
If = 1*( No-tag-list | Tagged-list )
No-tag-list = List
Tagged-list = Resource-Tag 1*List
List        = "(" 1*Condition ")"
Condition   = ["Not"] (State-token | "[" entity-tag "]")
```

A parse failure is `400`. An unsatisfied condition is `412`. A write to a locked resource with no valid token submitted is `423 Locked`.

---

## 6. COPY / MOVE

```rust
fn parse_destination(h: &HeaderValue, base: &Url) -> Result<(ShareId, SafePath)> {
    let u = base.join(h.to_str()?)?;
    ensure!(u.host() == base.host() && u.port() == base.port(), BadGateway); // 502: cross-server move not supported
    let vpath = percent_decode(u.path()).strip_prefix(DAV_PREFIX).ok_or(Conflict)?;
    acl_scope.resolve(vpath)                       // virtual path → (share, SafePath). Single entry point.
}
```

| Situation | Response |
|---|---|
| Destination's parent missing | `409 Conflict` |
| Destination exists, `Overwrite: F` | `412 Precondition Failed` |
| Destination exists, `Overwrite: T` | overwrite, `204` |
| New target | `201 Created` |
| Different host | `502 Bad Gateway` |
| Locked target | `423 Locked` |
| Cross-mount, size ≤ `dav.sync_copy_limit` | synchronous copy, then respond |
| Cross-mount, size over the limit | `507` and a pointer to the web UI |

**Large cross-mount moves have an acknowledged limit.** WebDAV has no notion of progress, and we sit behind Cloudflare's 100-second window. `202 Accepted` is technically allowed by the RFC but real client support for it is poor, so instead there is a size cap (2 GiB default) with a clear error past it — better than a silent timeout.

Collection COPY defaults to Depth infinity. A partial failure is reported as a 207 multistatus listing the failed items.

---

## 7. Client-specific behavior

What actually determines interoperability. Each row here is **pinned by an integration test.**

| Client | Symptom | Handling |
|---|---|---|
| **Windows Explorer** (WebClient) | probes with an unauthenticated `OPTIONS /` before mounting; gives up if a 401 lacks `WWW-Authenticate` | unauthenticated `OPTIONS` returns `DAV: 1,2,3` + `MS-Author-Via: DAV`; every other method answers `401` + `WWW-Authenticate: Basic realm="…"` |
| | default download cap of 50 MB | documented registry workaround (`FileSizeLimitInBytes`) — nothing the server can do |
| | rejects Basic auth over plain HTTP | HTTPS is a documented requirement |
| | sends `Translate: f` | ignored |
| **macOS Finder** | shows zero free space and refuses to copy without quota properties | RFC 4331 properties are always answered |
| | creates `.DS_Store`, `._*` (AppleDouble) | not hidden from DAV responses (hiding them confuses Finder) — **hidden only in the web UI** |
| | sends filenames as NFD | `SafePath` lookups try both NFC and NFD (`DESIGN-CORE.md` §2) |
| | mounts read-only without LOCK | Class 2 |
| **MS Office** | `PROPPATCH`es `Win32CreationTime`, `Win32LastAccessTime`, `Win32LastModifiedTime`, `Win32FileAttributes`; a non-`200` fails the save | stored as dead properties, answered `200`. `Win32LastModifiedTime` optionally applied to the real mtime |
| | LOCK → PUT → UNLOCK on save | Class 2 |
| **rclone** | vendor auto-detection; the NC vendor profile expects `oc:checksums` | the core answers as a standard vendor; NC-flavored behavior lives in the compat layer |
| **Cyberduck** | relies heavily on ETag, conditional GET | ETag stability is required |
| **Joplin / Obsidian** | thousands of small files, frequent PROPFIND | Depth:1 performance is what matters; listing cache helps |
| **Android DAVx⁵** | Depth:1 plus bulk GET | Range and conditional GET correctness |

---

## 8. Error mapping

```rust
fn map_errno(e: Errno, ctx: Ctx) -> StatusCode {
    match e {
        EACCES | EPERM => FORBIDDEN,                        // 403
        ENOENT         => NOT_FOUND,                        // 404
        EEXIST         => if ctx.overwrite { PRECONDITION_FAILED } else { CONFLICT },
        ENOTEMPTY      => CONFLICT,                         // 409
        ENOSPC | EDQUOT => INSUFFICIENT_STORAGE,            // 507
        ELOOP          => FORBIDDEN,                        // symlink policy violation
        ENAMETOOLONG   => BAD_REQUEST,
        EXDEV          => unreachable!("handled in §6"),
        EROFS          => FORBIDDEN,
        _              => INTERNAL_SERVER_ERROR,
    }
}
```

**Distinguishing 403 from 404 is itself an information leak.** A path the caller cannot list gets `404` regardless of whether it exists. `403` is reserved for a path that *is* listable but not writable.

---

## 9. Performance

- `GET` uses `sendfile` behind a reverse proxy over plain HTTP; `ReaderStream` with a 256 KiB buffer when we terminate TLS ourselves.
- Range requests support a single range only (multi-range is unused by media clients and only adds implementation complexity). A multi-range request gets the whole body back as `200` — RFC-permitted.
- Conditional GET (`If-None-Match`, `If-Modified-Since`) → `304`. Most sync-client traffic disappears here.
- PROPFIND Depth:1 requests only the `statx` fields it actually needs, not the full `STATX_BASIC_STATS` set every time.

---

## 10. Verification

| Item | Method |
|---|---|
| Protocol conformance | full **Litmus** suite, required in CI |
| Real-world use | CI container round-trips with `rclone check`/`sync`, DAVx⁵ simulation |
| Parser | `cargo-fuzz`: PROPFIND/PROPPATCH/LOCK bodies, the `If` header, the `Destination` header |
| Locking | concurrent LOCK contention, expiry auto-release, survival across a restart |
| Information leaks | every method against a path the caller cannot list returns `404` only (no 403/409/423 leakage) |
| Clients | a regression test for every row in §7 |

---

## 11. Chunked upload on the native mount

RFC 4918 has no partial-write verb. `PUT` is whole-body, `Range` is honoured
on `GET` only, and there is nothing in the specification to chunk with — so a
client that loses a connection 9 GB into a 10 GB `PUT` starts over. Every
vendor solves this out-of-band; the reference server's answer is a *session folder*, and
the compatibility layer already speaks it at
`/remote.php/dav/uploads/{user}/{tid}/**`.

That surface disappears under `--no-default-features`, which `scripts/verify.sh`
gates. Until this section existed, turning the compatibility layer off removed
resumable WebDAV upload from the product. `/dav-uploads` is the vendor-neutral
equivalent, compiled unconditionally.

### 11.1 Sequence

```text
MKCOL     /dav-uploads/{tid}         Destination (required)                 -> 201
                                     Upload-Length (optional)
PUT       /dav-uploads/{tid}/{n}     n numeric, 1..10000                    -> 201
MOVE      /dav-uploads/{tid}/.file   Upload-Length (optional)               -> 201 created
                                     X-Mtime (optional)                        204 overwritten
                                                                              + ETag
DELETE    /dav-uploads/{tid}         abort                                  -> 204
PROPFIND  /dav-uploads/{tid}         chunk listing, for resume              -> 207
OPTIONS   /dav-uploads/{tid}                                                -> 200 + Allow
anything else                                                               -> 405
```

`{n}` is a **sort key, not an offset**. Chunk sizes are chosen by the client
and need not be uniform, so assembly is by ascending name. Leading zeros are
accepted and discarded: `00007` and `7` are the same chunk.

`Destination` accepts both an absolute URL and a bare path, because which one a
client sends is not something any specification pins down. It is fixed at
`MKCOL`; if `MOVE` repeats it, it must agree, or the request is `409` — silently
honouring a different one would publish the bytes somewhere the client's own
bookkeeping says they did not go.

`Upload-Length` is named after TUS 1.0.0 (`FEATURES.md` #28), not after
`OC-Total-Length`. Absent on the `MOVE`, the engine's own received length is
used.

### 11.2 Why it is not under `/dav`

`/dav/{*path}` maps straight onto the share tree. axum matches a literal
segment before a wildcard, so registering `/dav/uploads/**` would make a share
actually *named* `uploads` permanently unreachable. The reference server has no such
problem because its files tree lives under its own `/remote.php/dav/files/`
prefix; on the native mount the root **is** the tree, so the session surface
gets a prefix of its own. `dav_chunked_upload.rs` creates a share named
`uploads` and asserts it is still addressable.

### 11.3 `{tid}` is attacker-controlled

The client picks the transfer id, so it is guessable, collidable, and can never
be a session key on its own. It is resolved through `sc_upload`'s
`upload_alias` table, whose primary key is `(user, tid)`, and every lookup
passes the authenticated principal. A tid belonging to another account answers
`404` — identically to one that never existed, per §8, so it is not an
existence oracle either. There is deliberately no `{user}` path segment: a name
in the path only recreates the "bob addresses alice's path" case that then has
to be checked.

A second `MKCOL` on a live tid is `409`, not a silent rebind: overwriting the
binding would orphan the first session's spool with nothing left to address it
by.

### 11.4 Losslessness

Nothing is written to the destination before `MOVE .file`. Chunks live in
`sc-upload`'s spool; assembly and publish go through the existing
`assemble_and_finalize` path, which finishes with an atomic rename. A
mid-transfer disconnect, a browser refresh, or a process restart therefore
cannot leave a partial file at the destination. `DELETE` drops the spool and
unbinds the tid; abandoned sessions are reclaimed by `UploadEngine::gc`, which
deletes the alias along with the session so a tid can never address a freed
session id.

### 11.5 Known limitation: `Scope::shares`-restricted app passwords

An app password restricted by `perms_mask` works here — `dav_authenticate`
maps the methods the same way it does for `/dav` (`MKCOL`→`CREATE`,
`PUT`→`WRITE`, `MOVE`→`MOVE|CREATE`, `DELETE`→`DELETE`, `PROPFIND`→`READ`).
An app password restricted by `shares` is **refused outright** on this mount.
Resolving which share a `{tid}` names requires the upload engine inside that
gate, which today holds only `auth` and `core`. The refusal fails closed, and
such a token can still use whole-body `PUT` on `/dav`.

### 11.6 Not automatic for generic clients

A generic WebDAV client (rclone, DAVx⁵, Windows Explorer) will never discover
or use this. It is opt-in, for our own web UI and for anything written against
this server specifically. Generic clients keep using whole-body `PUT` on
`/dav`, which is unchanged.
