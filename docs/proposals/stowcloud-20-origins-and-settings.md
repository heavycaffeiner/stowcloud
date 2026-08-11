# Declared Origins and the Settings Screen - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-11                       |
| Status     | **Implemented**                  |
| Reviewers  |                                  |

---

## 1. Summary

This server already answers to any number of hostnames and IP addresses. The
host guard, the CSRF origin allowlist and the self-signed certificate are all
built from `app_hosts`, which is a list, and every one of them handles N
entries correctly today. What does not handle N entries is most of what the
server *hands out*: a share link and an OIDC redirect are each built from one
origin chosen at boot, so a deployment reached under two names gives both sets
of users the same one name.

The compatibility layer is the exception, and it is the model for the rest of
this proposal. Login Flow v2 already resolves the request's `Host` against an
administrator-declared allowlist and answers with the origin the client
actually reached (`NcConfig::canonical_for_host`), so a phone enrolled on the
LAN is bound to the LAN name. That mechanism is correct and complete. Its
input list, `compat_alt_canonical_urls`, is reachable from `config.toml` only.

This proposal does two things:

1. Promotes the compat layer's origin list to a first-class, server-wide
   setting (`public_origins`), and puts share links and OIDC redirects under
   the same host-aware rule the compat layer already follows.
2. Fixes the server-settings admin screen so that list, and everything else on
   it, can actually be edited and un-edited. A field-by-field audit against
   `Config` found nine defects. Four of them have to be fixed before the first
   half can land at all: two make a setting permanently unreachable once it has
   been saved once, and two mean an administrator cannot tell what the running
   server is using or which file is being ignored.

The second half is not decoration on the first. An override written from that
screen beats `config.toml` on every boot and there is no way to remove it, so
adding one more editable key to a screen with that property makes the problem
worse rather than better. The reset path lands first.

## 2. Background & Motivation

### 2.1 Multiple names already work, up to the point where a URL is emitted

Reaching this server at more than one name is supported at every admission
layer, and none of it needed to change for this proposal:

- `middleware::host_guard` accepts any `app_hosts` entry case-insensitively,
  plus any private-range IP literal without it being listed at all.
- `app.rs` derives the CSRF `Origin` allowlist as `https://{host}` and
  `https://{host}:{port}` for every `app_hosts` entry, unless the operator set
  `allowed_origins` explicitly, in which case that list is used verbatim.
- `tls::san_entries` puts every `app_hosts` entry, the detected LAN address and
  the three loopback names into the generated certificate.

So a NAS listed as `files.example.com`, `nas.internal` and `192.168.1.10`
serves the web UI on all three today. The failure starts when the server has
to name itself:

```rust
// crates/sc-http/src/routes.rs
fn public_link_url(state: &AppState, token: &str) -> String {
    match &state.cfg.public_base_url {
        Some(base) => format!("{base}/s/{token}"),
        None => {
            let host = state.cfg.app_hosts.first().cloned().unwrap_or_default();
            format!("https://{host}/s/{token}")
        }
    }
}
```

There is no request in that signature. An administrator working on
`nas.internal` creates a share link and is handed a `files.example.com` URL,
or, with nothing declared, `https://` plus whichever `app_hosts` entry happens
to be first, with the port dropped and the scheme assumed.

`public_base_url` itself comes from `resolve_compat_canonical_url`, so the
value that governs every public share link is a key named `compat_*`. The
field's own doc comment admits the overlap. The name is wrong because the
concept it names was introduced for one consumer and then reused by another.

### 2.2 The compat layer solved this, in its own crate, for itself

```rust
// crates/sc-compat-nc/src/config.rs
pub fn canonical_for_host(&self, host: Option<&str>) -> &str {
    let Some(host) = host.map(str::trim).filter(|h| !h.is_empty()) else {
        return &self.canonical_url;
    };
    self.origins()
        .find(|o| authority_of(o).eq_ignore_ascii_case(host))
        .unwrap_or(&self.canonical_url)
}
```

The property that makes this safe is worth restating, because it is the
property the rest of the server has to inherit: the `Host` header never
*builds* a URL, it only *selects* one an administrator has already written
down. An attacker who controls `Host` can therefore only ever make the server
name a host it already serves. Anything unrecognised falls back to the
canonical origin.

The input list is `compat_alt_canonical_urls`, and it is `config.toml` only.
The stated reason is that `NetworkOverride` is full-replace, so a PATCH from
an older UI build would silently erase it. That reason is half right.
`NetworkPatch` carries no `#[serde(default)]` on any field, so a request
missing a field fails deserialization with 422 rather than erasing anything.
The silent-erasure risk exists only if the field is added with a serde
default. The real hazard is different and is covered in 2.4.

### 2.3 Single sign-on is single-origin by construction

`oidc.redirect_uri` is one absolute URL, used in both the authorization
request and the token exchange, and it has to match what is registered at the
IdP byte for byte. A user who starts on `nas.internal` is sent to the IdP and
comes back on `files.example.com`, where the session cookie lands. From their
point of view the login moved them to a different site.

Every IdP worth using accepts several registered redirect URIs. The server
does not, and there is no reason for that other than the field being a
`String`.

### 2.4 Nine defects in the settings screen

A field-by-field audit of the 50 rows the screen emits against the leaves of
`Config` found the following. The four marked **blocking** are the reason the
origin work cannot land first.

1. **`smb.server_name` is advertised as editable and has no control.**
   `snapshot_fields` emits it with `readonly_reason_key: None`, and the Rust
   test's `UI_EDITABLE_KEYS` lists it, but `ServerSettingsSection.svelte`'s
   own `EDITABLE_KEYS` does not and there is no input for it. It falls into
   the read-only "other" list with no reason attached. The hand-written Rust
   list is a second source of truth for something only the frontend knows,
   and nothing compares the two.
2. **Saving the SMB form freezes `smb.server_name` forever.** (blocking)
   `set_smb` reads the current value for the field the UI omits and writes it
   into `SmbOverride`, which beats `config.toml` on every subsequent boot. One
   save makes the key unreachable from both the screen and the file.
3. **An override cannot be removed.** (blocking) No code path sets any section
   of `SettingsOverrides` back to `None`. Every group is one-way: after the
   first save of a group, `config.toml` is dead for every key in it, and the
   only recovery is deleting `settings.db`.
4. **The documented precedence is wrong.** (blocking) `Config::load`'s doc
   says "env always wins", and `bootstrap()` applies the override store
   *after* `apply_env`. `SC_BIND`, `SC_TRUSTED_PROXIES`, `SC_DATA_DIR` and
   `SC_MASTER_KEY_FILE` are all silently overridden by one UI save, and
   `trusted_proxies` in particular gets an env-derived value frozen into the
   database.
5. **`index.content_enabled` is a dead key.** Nothing reads it. The screen
   shows it read-only with "managed in the index section", and that section
   toggles the name index only.
6. **Three rows show the boot value, not the effective one.**
   `index.name_enabled`, `upload.chunk_min_bytes` and
   `upload.chunk_default_bytes` are owned at runtime by `index.db` and
   `upload.db`. The settings screen reads `SettingsBridge`'s `Config` copy,
   which those stores never update, and hardcodes `source: config_file` for
   all three.
7. **Single sign-on can be enabled from the screen and cannot activate from
   the screen.** `oidc.client_secret_file` is `config.toml` only, on purpose,
   but nothing checks it at save time. Saving `enabled = true` reports success
   and the next boot logs "client_secret_file is not set" and stays off.
8. **Saved-but-pending is indistinguishable from in-effect.** (blocking)
   `restart_required` is a static property of the field, not a statement about
   this value. After saving a restart-required field the screen shows the new
   value with no indication that the running server is still on the old one.
9. **Two knobs accept a value that disables the feature.**
   `search.rate_per_minute = 0` is accepted and applied live, and every search
   from every user answers 429 from that moment. `archive.max_concurrent = 0`
   is clamped to 1 in the semaphore but stored and displayed as 0.

### 2.5 Two smaller findings from the same trace

- `tls::san_entries` takes `app_hosts` only, so a named `content_hosts` entry
  is missing from the generated certificate. The case where that bites is a
  server bound to 443 itself: the content URL resolves to its own listener, the
  handshake carries that name in SNI, and the certificate does not have it. A
  plain omission, fixed here.
- `content_url` builds `https://{content_hosts[0]}/c/{token}`: scheme
  hardcoded, no port. That looks like the bug already fixed for share links,
  and it is not the same thing. The share-link fix was possible because a
  declared origin carries its own port; a `content_hosts` entry cannot, since
  `host_guard` strips the port off the `Host` header before comparing, so an
  entry carrying one can never match. A named content origin therefore only
  works when something terminates 443 for that name. That is a real
  constraint, it is currently written down nowhere, and this proposal states
  it and reports it at startup rather than inventing a port to append.

## 3. Goals & Non-Goals

### 3.1 Goals

1. One declared list of externally reachable origins, `public_origins`, that
   accepts any number of domains and IP literals, with the first entry as the
   canonical fallback.
2. Every URL the server hands out names the origin the request arrived on when
   that origin is one an administrator declared, and a declared fallback
   otherwise. Login Flow v2 already works this way; share links join it against
   `public_origins`, and OIDC against its own `redirect_uris` (4.3), falling
   back to the first entry of whichever list applies.
3. `public_origins` is editable from the settings screen, as is every other
   editable key, with no key reachable from the API but absent from the
   screen.
4. Any settings group can be reverted to what `config.toml` and the
   environment say, from the same screen that changed it.
5. The screen tells the truth about three things it currently gets wrong: what
   the running server is using, where a value came from, and whether a saved
   change has taken effect.
6. Nothing in this proposal lets a request header supply a string that ends up
   in a URL. Selection from a declared list only.

### 3.2 Non-Goals

- **Per-host content origins.** `content_hosts` stays a list for the host
  guard and one entry for URL building. The content origin is a security
  boundary that never parses a cookie, and one of it is enough; pairing each
  app origin with its own content origin doubles the certificate and DNS work
  for no isolation gain. The certificate omission in 2.5 is still fixed, and
  the 443 constraint is still written down.
- **Per-host branding, per-host share policy, or anything else keyed on which
  name a user arrived by.** The origin is selected for URL construction and
  for nothing else.
- **Automatic certificate issuance for the declared origins.** They are already
  covered by the generated certificate, but transitively rather than by name:
  a declared origin has to be in `app_hosts` to be reachable, and `app_hosts`
  is what `san_entries` reads. Nothing is added to that function for them. A
  publicly trusted certificate is still the reverse proxy's job.
- **Writing `config.toml` from the admin UI.** The override store stays
  separate. This proposal makes the override *visible and reversible*, not
  authoritative.

  The reason usually given for the split does not survive a look at the script.
  `settings_store.rs` says the store exists because "`scripts/deploy.sh`
  overwrites [`config.toml`] on every push", and today it does not: the script
  pushes one binary to the dev unit and the source tree to a build context the
  Dockerfile turns into an image, and that Dockerfile copies `Cargo.toml`,
  `crates/`, `web/` and two licence files. No `sc.toml`, anywhere.

  The split is still right, on a reason that does hold: the file is the
  operator's, a server that rewrites it has to merge with whatever they were
  editing, and a comment-preserving TOML round-trip is a dependency and a class
  of bug this does not need. The stale comment is corrected in M1 alongside the
  precedence one, since propagating it is how it got into this document.
- **A compatibility fold for the old key names.** `compat_canonical_url` and
  `compat_alt_canonical_urls` are removed outright rather than accepted and
  translated. See 4.2 for the three outcomes available and why this is the one
  chosen, and for what replaces the fold: a startup refusal that names the new
  key, and a one-time migration of the admin override store, which is the one
  place the operator cannot edit by hand.

## 4. Technical Design

### 4.1 Architecture Overview

One list in `Config`, resolved once by the assembler, handed to two consumers
that each do their own matching:

```
config.toml / env / settings.db
        │
        ▼
  Config::public_origins  ──►  Config::resolve_public_origins()
        │                             │
        │                             ├──►  HttpConfig::origins : OriginSet
        │                             │        used by public_link_url
        │                             │
        │                             └──►  NcConfig::{canonical_url, alt_canonical_urls}
        │                                      used by login flow v2, capabilities
```

OIDC is not on this diagram on purpose. It selects by the same rule and from
its own list, `oidc.redirect_uris`, for the reason given in 4.3: a redirect URI
is registered at an identity provider and cannot be derived from anything here.

`sc-compat-nc` keeps its own matching function rather than importing
`OriginSet`. Its dependency list is deliberate: the core crates it consumes
(`sc-vfs`, `sc-acl`, `sc-meta`, `sc-core`, `sc-upload`) and nothing from the
HTTP assembly layer. Everything the layer needs from that side arrives as
plain data on `NcConfig`, which is why the session cookie name and the
reserved path prefixes are handed in instead of imported. Adding `sc-http` to
that list to save fifteen lines of authority comparison would spend a
structural property on a small duplication.

Note what does *not* justify it: `scripts/verify.sh`'s compat gate greps core
crates for compat wire vocabulary and enforces that no core crate depends on
this one. It says nothing about the reverse direction, and this crate already
depends on `axum` and `http`. The reason above is the design contract, not a
mechanical check, so the two implementations are held together by a shared
test vector instead: one table of `(declared origins, Host, expected origin)`
rows, asserted in both crates.

### 4.2 Data Model Changes

In the origins area, `Config` gains one field and loses two. It loses
`index.content_enabled` as well, for an unrelated reason given in 4.4.

```rust
/// Externally reachable origins, `scheme://host[:port]`, no trailing slash.
/// The first entry is canonical: it is what a request on an unrecognised
/// `Host` is answered with, and what a URL built outside a request uses.
///
/// A request's `Host` never builds a URL. It only selects among these.
#[serde(default)]
pub public_origins: Vec<String>,
```

`compat_canonical_url` and `compat_alt_canonical_urls` are removed. There is
no fold: `from_toml_str` refuses a document that still carries either key,
with an error naming `public_origins` and showing the one-line replacement.
The check is a pre-pass over the parsed `toml::Value`, because serde will not
raise it: the struct does not set `deny_unknown_fields`, and turning that on
globally would start rejecting every forward-compatible key an operator has,
which is a much larger change than this one.

Refusing is the point. Doing nothing would make an old file parse cleanly with
the old keys ignored, and that deployment comes back up with nothing declared.
What happens next depends on a value the operator was not thinking about, which
is what makes it the worst of the three available outcomes:

- with zero or several `app_hosts` entries, the compat layer stops mounting,
  and every enrolled client starts getting 404s from the endpoints it syncs
  against;
- with exactly one, it mounts and silently re-derives the canonical URL from
  that entry. If the removed key said anything else, the deployment now hands
  new enrolments a different origin and logs it as a normal derivation.

Share links fall back to a guess in both branches.

A fold is the middle outcome, and it is rejected for the ordinary reason: two
names for one setting, kept alive indefinitely because nothing ever forces the
removal, and a second code path to reason about every time the resolution rules
change.

The same treatment applies to `oidc.redirect_uri`, which becomes
`oidc.redirect_uris`.

**The admin override store is migrated, because nobody can hand-edit it.**
`NetworkOverride` is stored as JSON in `settings.db`, and its fields carry no
serde default, so a row written before this change would fail to deserialize.
`SettingsStore::from_conn` reads that blob with `unwrap_or_default()`, which
means the failure is not an error: it silently discards *every* override an
administrator has ever made, across all ten sections. That is the exact
failure `SettingsOverrides`'s own doc comment says the container-level
`#[serde(default)]` exists to prevent, and a field rename defeats it.

So the store gets a one-time migration, run in `from_conn` before
deserialization: read the raw JSON, and if `network.compat_canonical_url` is
present while `network.public_origins` is absent, move the value into a
one-element `public_origins` and drop the old key, then write the row back. The
field is `Option<String>` today, so a stored `null` becomes `[]` rather than a
one-element list holding nothing: an administrator who cleared that box meant
"no declared origin", and `[]` is what says that.
The migration is idempotent, runs once, and leaves no legacy key behind. It is
in the storage layer rather than the config parser, so the config parser keeps
exactly one vocabulary.

Derivation when `public_origins` is empty keeps today's rule exactly, because
it is the right one and it is already documented:

| `public_origins` | `app_hosts` | Result |
|---|---|---|
| non-empty | any | used verbatim, first is canonical |
| empty | exactly one entry | `https://{that entry}` derived, logged loudly |
| empty | zero or several | ambiguous: no canonical origin, compat layer does not mount, share links fall back to 4.3's last resort |

`SettingsOverrides` gains no new section: `public_origins` lives in
`NetworkOverride`, which already exists and is already full-replace. What it
does gain is the `clear` operation in 4.4.

`OidcConfig::redirect_uri: String` becomes `redirect_uris: Vec<String>`, and
`OidcOverride` gets the same store migration as `NetworkOverride` above, for
the same reason.

### 4.3 Origin resolution

```rust
// crates/sc-http/src/config.rs
#[derive(Clone, Debug, Default)]
pub struct OriginSet {
    /// Non-empty in every deployment that declared or derived one. Empty is
    /// the ambiguous case, where callers fall back to their own last resort.
    origins: Vec<String>,
}

impl OriginSet {
    pub fn canonical(&self) -> Option<&str>;
    /// The declared origin whose authority equals `host`, or the canonical
    /// one. Never anything derived from `host` itself.
    pub fn for_host(&self, host: Option<&str>) -> Option<&str>;
    pub fn url_for_host(&self, host: Option<&str>, path: &str) -> Option<String>;
}
```

Matching is on the authority (`host[:port]`) with the scheme and path cut off,
case-insensitively, with IPv6 literals kept intact by the existing
`split_host_port`. That is what the compat layer does now and it is the only
comparison that works against a `Host` header.

`HttpConfig::public_base_url` is replaced by `origins`, not kept alongside it.
Two fields answering "which origin" is how the core and compat sides came to
disagree in the first place. `sc-http` also needs a small `host_of(&HeaderMap)`
helper next to `split_host_port`: it has none today, and `sc-compat-nc`'s
`host_header` is on the other side of a crate boundary this proposal is not
crossing.

`public_link_url` takes the request's headers and loses its `public_base_url`
special case:

```rust
fn public_link_url(state: &AppState, headers: &HeaderMap, token: &str) -> String {
    if let Some(base) = state.cfg.origins.for_host(host_of(headers)) {
        return format!("{base}/s/{token}");
    }
    // Last resort, byte for byte what it does today: no declared origin means
    // nothing here knows the answer. See below for why appending the bind
    // port would not be an improvement.
    let host = state.cfg.app_hosts.first().cloned().unwrap_or_default();
    format!("https://{host}/s/{token}")
}
```

The fallback is deliberately left alone. It drops the port and assumes
`https`, which is right for the reverse-proxied deployment on 443 and wrong
for a direct one on 8443, and the server cannot tell which it is: `bind` is
the internal port, and behind a proxy it is not the port a recipient would
dial. Appending it would trade a broken link for direct deployments against a
broken link for proxied ones. The answer to both is one declared origin, which
is what `public_origins` is, so the improvement here is to make the
undeclared case visible rather than to guess better: startup diagnostics gains
a line when no origin is declared, and the settings screen says the same thing
next to the empty field.

**Four handlers, not two.** `with_link_url` has three call sites, not the one
it looks like it has, and `shares_create` reaches `public_link_url` directly:

| Handler | How it reaches the URL builder |
|---|---|
| `shares_list` | `with_link_url` over every row |
| `shares_get` | `with_link_url` |
| `shares_patch` | `with_link_url` |
| `shares_create` | `public_link_url` directly |

All four gain a `HeaderMap` extractor and `with_link_url` becomes a
three-argument function. Missing any of them is not a cosmetic gap: the create
dialog would name the origin the admin is on and the list beside it would name
a different one for the same link, which is worse than today's consistent
guess. One test drives all four with the same `Host` and asserts they agree.

OIDC selects by the same rule but **not from the same list.** `redirect_uris`
is its own origin set: the entry whose authority equals the request's `Host`
is used, and the first entry otherwise. It does not consult `public_origins`
at all.

That separation is deliberate and it is a correction, not a simplification. A
redirect URI has to match what is registered at the IdP byte for byte, which
is why proposal 0 made it explicit rather than derived, and it is independent
of whether this deployment has declared a public origin: OIDC works today with
`public_origins` unset, and coupling the two would break that deployment on
upgrade for no gain.

The one validation is against `app_hosts`, not against `public_origins`:
every `redirect_uris` entry must be `https://` and its authority must be a host
this server admits, since the callback comes back to us and nothing else can
answer it. `app_hosts` is what decides admission, so it is the honest thing to
check.

Which `app_hosts`, precisely: `Config::app_hosts`, the operator's own list, not
the effective set the guard uses. That set is wider in two ways, both
deliberately excluded here. `app.rs` injects the bind address into
`HttpConfig::app_hosts`, and `host_guard` additionally admits any private-range
IP literal without it being listed at all. Neither is a name an operator wrote
down, and a redirect URI is a string registered at an identity provider by
hand. Refusing a redirect URI on an address that merely happens to be admitted
is the stricter and more useful answer.

The check runs at save time and again in `OidcConfig::inactive_reason` at boot,
never per request, and a mismatch is a refusal carrying the reason rather than a
silent fallback. At save time it reads the *effective* `cfg.app_hosts` of 4.4,
not the booted one: an administrator who has just added a host and not yet
restarted is declaring intent, and both changes need the same restart before
either takes effect.

`NcConfig` keeps `canonical_url` and `alt_canonical_urls` as its input shape.
`app.rs` fills them from the same resolved list, so the compat layer's origin
behaviour is unchanged and every origin test in that crate keeps passing
untouched. The one change to `NcConfig` in this proposal is elsewhere and for
another reason: 6-3 removes `chunk_size_advisory` from it.

### 4.4 Making the settings screen reversible and honest

**Three configs, because there are three different questions.**
`SettingsBridge` currently holds one `Config` and cannot answer two of them.
It gains two more, and `bootstrap()` is what supplies the first:

| Held value | What it is | What it answers |
|---|---|---|
| `file_cfg` | `Config::load` plus `apply_env`, **before** `apply_settings_overrides` | what a reset reverts to |
| `boot_cfg` | what the process actually started with, file + env + overrides as of boot | whether a saved change has taken effect yet |
| `cfg` | the live effective config, updated by every patch | what the screen shows and what `render_live` uses |

This is the part the reset depends on and the part nothing in the current code
has: `bootstrap()` applies overrides in place and drops the pre-override value
on the floor, so a bridge built from the result cannot reconstruct
`config.toml` plus env. `bootstrap()` therefore clones the config before the
overlay and returns both.

**Reset.** `SettingsOverrides` gains a `clear(section)` operation and the API
gains `DELETE /api/admin/server-settings/{section}`. The handler drops that
section, persists, and rebuilds `cfg` as `file_cfg` with the *remaining*
overrides applied, so clearing one group never disturbs another. For a
live-appliable group it then pushes the restored values into the running
components exactly as the corresponding `set_*` would: the search limits and
the archive semaphore are reconfigured, and SMB is re-rendered. Otherwise a
reset would report success and leave the process running the value it just
discarded.

"Exactly as `set_smb` would" includes the step that is easy to miss:
`smb.totp_policy` is pushed into `AuthService` before the render, because both
enforcement sites read it from there rather than from the config, and a render
that runs first sees the old value. A reset restores the file's policy the same
way, and puts the previous one back if the render fails. The response is the same `ApplyOutcome` a patch returns, so a
restart-required group reports `restart_required: true` and the screen reacts
identically. This is listed first in the implementation plan because every
other editable key depends on it being possible to undo.

**Boot value versus current value.** `SettingsField` gains one field, computed
by comparing `cfg` against `boot_cfg`:

```rust
/// The value the running process is actually using, when it differs from
/// `value` because the change needs a restart that has not happened.
/// `None` when the two agree, which is the ordinary case.
pub running_value: Option<serde_json::Value>,
```

The screen renders a "pending restart" badge for exactly those rows and the
restart section says how many there are. A per-field flag is used rather than
one global "something is pending" bit because an administrator needs to know
*which* change is waiting.

**Live values for the three delegated rows.** `SettingsBridge` takes
`Arc<IndexSettingsStore>` and the upload engine handle, and reads
`index.name_enabled`, `upload.chunk_min_bytes` and `upload.chunk_default_bytes`
from them instead of from its own `Config` copy.

Reporting `source` correctly needs one small addition on each store, because
neither can currently answer it: `IndexSettingsStore::from_conn` reads
`Option<bool>` from the table and immediately collapses it with
`unwrap_or(default_enabled)`, so by the time anyone asks, "stored" and
"defaulted to the config value" are the same bool. Both stores keep that
`Option` and expose whether a row exists. `source` is then `AdminOverride` when
one does, and `ConfigFile`/`BuiltinDefault` when it does not, instead of the
current hardcoded `ConfigFile`.

**`index.content_enabled` is deleted.** Not marked read-only with a better
reason: removed from `Config`, from the snapshot and from the test fixtures.
Nothing reads it, content indexing is not built, and a setting that does
nothing is worse than an absent one because it looks like a supported switch.

Unlike the two origin keys, this one is *not* added to 4.2's startup refusal.
The rule there is about behaviour changing under an operator who did not touch
anything, and this key has no behaviour to change: a file that still sets it
does exactly what it did before, which is nothing. Refusing to start over a
dead key would be an outage bought with no information.

**Validation added at save time**, each because the current behaviour is a
foot-gun with no feedback:

| Field | Rule | Why |
|---|---|---|
| `search.rate_per_minute` | `>= 1` | 0 rejects every search from every user, live, immediately |
| `archive.max_concurrent` | `>= 1` | the semaphore already clamps, the stored and displayed value did not |
| `oidc.enabled` | `client_secret_file` must be set and readable | otherwise the save succeeds and SSO silently never activates |
| `oidc.redirect_uris` | each `https://`, each authority in `app_hosts` | a callback to a host we do not admit is an unrecoverable login |
| `public_origins` | each absolute `http(s)://` with a non-empty authority | the rule `resolve_compat_alt_canonical_urls` applies today |

Malformed `public_origins` entries are refused at save time, unlike the boot
path, which drops them with a warning. Refusing at boot would take the server
down over a typo; refusing at save costs an administrator one correction while
they are looking at the field.

Duplicates are not in that table because they are not a refusal. A repeated
entry is collapsed on both paths, which is what `resolve_compat_alt_canonical_urls`
does today, and it is the right treatment: two identical origins express one
intention, and rejecting the second would be pedantry aimed at a paste.

The boot path needs one rule the single-value version did not: **a malformed
first entry invalidates the whole list.** Dropping it and letting the second
entry become canonical would silently change what every future client is told
to bind to, which is the failure `compat_canonical_url` was introduced to stop.
A bad entry anywhere else is dropped with a warning; a bad entry at position
one yields `Invalid`.

`Invalid` is not the same as absent, and the difference is load-bearing.
`resolve_compat_canonical_url` returns `Invalid` from inside the
`if let Some(v)` branch, so it never reaches the derivation arm below it: a
malformed explicit value refuses outright and the compat layer does not mount,
while an *absent* value with exactly one `app_hosts` entry derives happily.
That asymmetry is correct and is kept verbatim. Treating a typo as "nothing
declared" would let a single-host deployment silently derive an origin the
operator never wrote, which is the same silent-substitution failure the whole
key exists to prevent.

**Precedence, stated correctly.** `Config::load`'s doc comment is corrected to
say what the code does: file, then env, then the admin override store. The
startup diagnostics block gains one line per active override section, naming
any env variable it shadows. An operator who sets `SC_BIND` and does not see
it take effect currently has nothing to read.

`settings_store.rs`'s module doc is corrected in the same pass, for the reason
3.2 gives: it justifies the store with a `deploy.sh` behaviour that script does
not have.

**The drift test.** The hand-written `UI_EDITABLE_KEYS` in
`settings_bridge.rs` is replaced by a list parsed from the Svelte source at
test time:

```rust
const UI_SOURCE: &str = include_str!(
    "../../../web/src/lib/ui/admin/ServerSettingsSection.svelte"
);
```

The test extracts the `EDITABLE_KEYS` array and asserts it equals the set of
snapshot fields with no `readonly_reason_key`. A Rust test is chosen over a
frontend test because the frontend suite cannot run on every development
machine here, and this particular check has to fail on the machine making the
change. Reaching outside the crate directory is safe to do because the
workspace sets `publish = false`; it would not be in a crate that is packaged.

What it catches and what it does not: `EDITABLE_KEYS` is the list the screen
excludes from its read-only section, so the test catches a key that falls into
that section without a reason, which is exactly how `smb.server_name` got lost.
It cannot see whether a listed key has an input rendered for it. There is one
key where those differ on purpose, `oidc.smb_policy`, which has a single
accepted value: a select with one option is a control that cannot be used, so
the form sends the constant and the hint under it says what the value means.
That is the one documented exception, and goal 3 is read against it: no key is
absent from the screen, and one key is present without being choosable.

### 4.5 Frontend

Two groups grow a field, network and SMB, and every group grows one button.

- **Public origins**: a comma-separated list, consistent with the four list
  fields already in that form, with a hint saying the first entry is the one
  used when a request arrives on an unrecognised name. Ordering is meaningful,
  which is why this stays a text field rather than becoming an unordered chip
  set. Two states get a message under the field rather than a refusal: empty,
  which means share links fall back to a guess (4.3), and an entry whose
  authority is not in `app_hosts`, which means the server will not answer on
  the name it is about to hand out. Neither is refused, because the operator
  may be about to fill in the other field, and neither is silent.
- **`smb.server_name`**: the missing text field, placed after the workgroup.
- **Revert to config.toml**: one outlined button per group, enabled only when
  that group's `source` is `admin_override`, with a confirmation dialog naming
  the values it will restore. Each of the ten carries its own accessible name
  from the catalogue, naming the group it reverts, rather than ten copies of
  one generic label.
- **Pending restart**: rows whose `running_value` is present render the
  running value under the input with a badge. The badge carries text, not
  colour alone.
- **OIDC**: the save path pre-checks what the browser can actually see, which
  is the existing https rule plus the new one that each redirect authority
  appears in the `app_hosts` field on the same screen. The secret file is not
  among them: only the server can tell whether a path is readable, so that one
  comes back as `settings.oidc_secret_file_missing` and is rendered from the
  catalogue like any other server refusal.

`web/src/lib/api/mock.ts` mirrors every one of these, including the reset
endpoint, since it is what the frontend suite runs against.

## 5. API Design

### 5-1. New / Modified

| Method | Path | Change |
|---|---|---|
| `GET` | `/api/admin/server-settings` | `SettingsField` gains `running_value`; `index.content_enabled` row removed; three delegated rows now report live values and true sources |
| `PATCH` | `/api/admin/server-settings/network` | `NetworkPatch` gains `public_origins: Vec<String>`; loses `compat_canonical_url` |
| `PATCH` | `/api/admin/server-settings/smb` | `SmbPatch::server_name` becomes required rather than `Option` |
| `PATCH` | `/api/admin/server-settings/oidc` | `redirect_uri: String` becomes `redirect_uris: Vec<String>` |
| `DELETE` | `/api/admin/server-settings/{section}` | **new**: drop this group's override and fall back to `config.toml` + env. Answers with the same `ApplyOutcome` a patch does |

`{section}` is one of `network`, `db`, `symlink-policy`, `homes`, `smb`,
`search`, `archive`, `watch`, `paths`, `oidc`. An unknown section is 404, not
a silent no-op.

Every patch field stays required. No `#[serde(default)]` is added to any patch
struct: a client that does not know a field must fail loudly rather than send
a default that erases a value it never saw. This is the rule that makes
full-replace groups safe, and it is what 2.2 concluded the existing comment
got wrong.

### 5-2. Error Handling

All refusals use the existing catalogue-key envelope, so the browser renders
them in the reader's language:

| Key | Status | Condition |
|---|---|---|
| `settings.invalid_origin` | 422 | a `public_origins` entry with no scheme or no authority |
| `settings.must_be_at_least_one` | 422 | `search.rate_per_minute` or `archive.max_concurrent` below 1. Existing key, already used by `set_watch` and already carrying a `{field}` placeholder |
| `settings.oidc_secret_file_missing` | 422 | `enabled` with no readable `client_secret_file` |
| `settings.oidc_redirect_uri_must_be_https` | 422 | a redirect URI that is not `https://`. Existing key, today fired for the single value and now per entry |
| `settings.oidc_redirect_host_not_served` | 422 | a redirect URI whose authority is not in `app_hosts` |
| `settings.unknown_section` | 404 | `DELETE` with a section name that does not exist |
| `settings.stale_client` | 422 | a patch body missing a field, which means the page predates this release (6-2) |

`settings.readonly_alt_canonical_urls` is deleted along with the read-only row
it explained.

## 6. Compatibility impact

A renamed origin key and a host-aware URL builder both sit directly under the
compatibility layer, where a wrong value is not an error message but a client
permanently bound to a server it cannot reach. This section is the trace of
what that change does and does not touch.

### 6-1. What does not break, and why

- **Enrolled compat clients keep working.** A client stores the `server` it
  was given at enrolment and keeps using it. Nothing in this proposal re-binds
  an enrolled client, and `canonical_for_host`'s behaviour is unchanged: only
  the name of the list feeding it changes, and only where it is edited from.
- **OCS share URLs never used `public_base_url`.** `SharePort::link_url` takes
  the origin as a parameter, resolved by the caller with `canonical_for_host`,
  and the implementation is `format!("{origin}/s/{token}")`. The compat side
  has been host-aware since it was written. What changes is that the core share
  API stops disagreeing with it.
- **`instance_id` is untouched**, so no client discards its sync journal.
  That is the one change in this area that costs terabytes, and nothing here
  goes near it.
- **WebDAV is unaffected.** PROPFIND hrefs are path-relative and `sc-dav`
  builds no absolute URL; the only `https://` in that crate is
  `copymove.rs` parsing an incoming `Destination` header.
- **Existing share tokens keep resolving.** A link's URL is derived per
  response by `with_link_url`, never stored, so changing how it is derived
  cannot invalidate a link already handed out. Any origin that stays in
  `app_hosts` keeps serving `/s/{token}` exactly as before.
- **Content URLs do not move.** `content_url` is unchanged: relative in the
  single-origin deployment, `content_hosts[0]` otherwise.

### 6-2. What breaks, and what each one costs

| # | Break | Blast radius | Handling |
|---|---|---|---|
| 1 | An `sc.toml` carrying a removed key refuses to start | whole server, until the file is edited | deliberate, see 4.2; the error prints the replacement line |
| 2 | A pre-rename `settings.db` row would fail to deserialize | every admin override, silently discarded | one-time store migration, 4.2, same commit as the rename |
| 3 | An open tab running the previous SPA build sends an old patch shape | one admin, one save | 422, fails closed; mapped to a readable envelope, below |
| 4 | `public_origins` becomes an admin override, so a UI save pins it and `config.toml` stops being read for it | new enrolments and new share links | M1's reset ships first; the row shows `admin_override` |
| 5 | Removing an origin from the list | new enrolments on that name | the name keeps serving as long as it is in `app_hosts`; the confirm dialog says so |
| 6 | Changing the first entry | the fallback for an unrecognised `Host` | confirm dialog states it explicitly |

**Break 1 has an ordering requirement worth stating.** For this repo the file
is edited by hand in the guest, once, before the rollout: `deploy.sh` pushes a
binary and a build context and never the config (3.2), so nothing will do it
for the operator and nothing will undo it afterwards. Any deployment that does
template its config must move the two together, since an old file at a new
binary refuses to start.

It is every entry point, not just `serve`: `gc` and `smb-sync` both go through
`bootstrap()`, so a scheduled `gc` on an unedited file fails too, and fails
where nobody is watching.

The one config file this repo does own, `deploy/testbed/sc.dev.toml`, gains
`public_origins` in the same commit. It sets neither removed key, so it would
not refuse to start; it declares no origin at all, which is why the testbed has
never mounted the compat layer. Declaring one there is a small improvement
taken while the file is open.

**Break 3 gets an envelope.** A missing field in a patch body is an axum `Json`
rejection, which answers 422 with the framework's own body rather than the
error envelope the SPA knows how to render, so the admin sees an unhelpful
string on a screen this proposal is otherwise making legible. The settings
routes map that rejection to `settings.stale_client`, whose catalogue text says
to reload the page. The request still fails, which is the point of keeping
every patch field required.

**Breaks 5 and 6 are the two the confirm dialog exists for.** Neither
disconnects an enrolled client: admission is `app_hosts`, and these edits only
change which name the server *offers*. The dialog says which names are being
removed and which one is becoming canonical, because "the phone still syncs but
a newly enrolled one binds elsewhere" is not something an operator will infer
from a text field.

### 6-3. One compat defect this review found, fixed here

`PATCH /api/admin/upload-settings` changes the live chunk limits, and the core
`/api/capabilities` reports them live (`state.uploads.chunk_limits()`). The
compat layer does not: `NcConfig::chunk_size_advisory` is a `u64` copied out of
`cfg.upload.chunk_default_bytes` once at `App::build`, so compat clients keep
being advertised the boot-time number until the process restarts.

It is advisory only, so nothing rejects an upload over it. The cost is that an
administrator who raised the chunk size to get past an intermediary saw the
screen agree and got no change in the clients that were actually failing.

This proposal has to fix it rather than note it, because M2 starts showing the
live value on the settings screen: leaving it would turn a silent divergence
into a visible one with no explanation next to it.

The fix keeps the compat crate's shape. `capabilities(cfg, host)` is a pure
function over its two arguments and stays one: the value arrives as a third
argument from the OCS handler, which already holds `Deps` and therefore
`Arc<dyn UploadEngine>`. That port gains an accessor, the handler passes what
it returns, and `NcConfig::chunk_size_advisory` is removed. No new dependency,
no port reference inside a function that renders a document.

`chunk_parallel_advisory` is left where it is, and it is worth being exact
about what that means: it is not a `config.toml` key at all. `nc.rs` builds
`NcConfig` with `..Default::default()` and never sets it, so the advertised 4
is the compat crate's own default. Nothing at runtime changes it, nothing on
the settings screen shows it, and giving it a live source would be inventing a
knob to solve a problem nobody has.

## 7. Implementation Plan

### 7-1. Milestones

**M1. Reversibility.** `bootstrap()` keeping the pre-override config and
`SettingsBridge` holding all three (4.4), which everything else in this
milestone rests on; `clear(section)`; the `DELETE` endpoint and its live
re-push; the revert button; the two corrected doc comments (precedence in
`Config::load`, the `deploy.sh` claim in `settings_store.rs`) and the startup
override log. Nothing else can land safely first: every later milestone either
adds a key or makes an existing one editable, and without this milestone each
of those is a one-way door.

**M2. Settings-screen truth.** `running_value`, computed against the `boot_cfg`
M1 already put in place; live values and true sources for the three delegated
rows; deletion of `index.content_enabled`; the three validations whose fields
already exist (`search.rate_per_minute`, `archive.max_concurrent`, the OIDC
secret file); the `smb.server_name` control; the drift test that would have
caught it; the live chunk advisory for compat capabilities (6-3), which has to
land with the row that starts showing the live value.

The other two validations ship with the fields they guard: `public_origins` in
M3, `redirect_uris` in M4.

**M3. Origins, core.** `public_origins`, the refusal on the two removed keys,
and the `settings.db` migration; `OriginSet`; `app.rs` feeding both
`HttpConfig` and `NcConfig` from it; `public_link_url` becoming host-aware;
`content_hosts` added to the certificate SAN list; the two new startup
diagnostic lines (no declared origin, named content origin without 443);
`deploy/testbed/sc.dev.toml` declaring an origin so the testbed mounts the
compat layer at all.

The store migration ships in the same commit as the rename. Splitting them
leaves a window where a deployment that restarts in between loses every admin
override, silently.

**M4. Origins, OIDC.** `redirect_uris` as a list with host selection and the
`app_hosts` check, plus the `OidcOverride` store migration.

**M5. The rest of the screen.** The `public_origins` field with its two hints,
the pending-restart badge, the OIDC client-side pre-check, the `mock.ts`
counterpart of every endpoint touched, and the i18n entries for every new
string in both catalogues.

The frontend is deliberately not all in M5. Each milestone carries the UI for
what it adds, because a milestone that ships a server change with no way to
reach it is a milestone that cannot be tested by hand: the revert button is in
M1, the `smb.server_name` field in M2. M5 is what is left over once those have
shipped.

M1 and M2 are independent of M3 and M4 and can be reviewed separately. M4 is
independent of M3 as well: its list is its own, validated against `app_hosts`
(4.3). M5 renders fields M3 and M4 introduce, so it comes after both. The only
ordering that is not negotiable is M1 before everything, and each store
migration inside the same commit as its own rename.

### 7-2. Dependencies

- `sc-compat-nc` keeps its own resolver and its dependency list: nothing from
  the HTTP assembly layer is added to it, which is the property 4.1 is
  protecting. It is not untouched, though, and 6-3 is why: `NcConfig` loses
  `chunk_size_advisory`, `ports::UploadEngine` gains an accessor, and
  `capabilities` takes a third argument. Origin handling itself changes only in
  where `app.rs` reads the list from, plus the shared test vector.
- Every deployment's `sc.toml` has to be edited before the release containing
  M3 will start, and again for M4 if it sets `oidc.redirect_uri`. Each is one
  line, the error names which one, and it is the deliberate cost of removing
  the fold.
- `scripts/verify.sh` gains no new gate. The drift test is an ordinary
  `cargo test`.
- The existing i18n gate does cover the new keys, and covering them is work,
  not a freebie. A key the server sends never appears at a `t()` call site, so
  `web/tools/i18n-check.mjs` only sees it through the `/* i18n */` declarations
  in `web/src/lib/api/error-text.ts`. Every new key needs a line there and an
  entry in both catalogues, and `settings.readonly_alt_canonical_urls` has to
  leave all three in one commit: the gate fails on a key with no catalogue
  entry and equally on a catalogue entry no call site uses.
- The self-signed certificate is regenerated when its SAN list stops matching,
  which the existing code already handles, so adding `content_hosts` to that
  list needs no migration step. The declared origins are not added separately:
  a name has to be in `app_hosts` to be reachable at all, and `app_hosts` is
  already in the SAN list.

## 8. Known limitations

- **Every extra name is an extra interstitial.** The generated certificate is
  self-signed, so each browser shows the warning once per name. This is the
  existing trade-off documented in `tls.rs`, and declaring more origins
  multiplies it rather than changing it.
- **A name still has to be in `app_hosts` to be reachable at all.** Declaring
  an origin does not admit it. The two lists are separate because they answer
  different questions, and an operator who adds an origin without the matching
  host gets a 421 with the host named in the log. The settings screen warns
  when a declared origin's authority is not covered by `app_hosts`, but it
  does not add it: silently widening a security allowlist as a side effect of
  editing a display setting is worse than the warning.
- **OIDC still requires every redirect URI to be registered at the IdP.**
  Nothing here can verify that, and a URI the IdP does not know produces an
  error at the IdP, not here. The save-time check confirms only that the URI
  is `https://` and names a host `app_hosts` admits.
- **The content origin stays single, and needs 443.** See 3.2 and 2.5. A
  named `content_hosts` entry produces `https://{host}/c/{token}` with no
  port, and cannot carry one, because the host guard compares the `Host`
  header with its port stripped. Startup says so; nothing here changes it.
- **A deployment with no declared origin still gets a guessed share link.**
  The fallback assumes `https` on the default port. It is right for the
  deployment shape this is designed for and wrong for a direct LAN one, and
  the server has no way to tell them apart. Declaring an origin is the fix,
  and the startup line and the settings screen both say so.
- **Upgrading is a breaking config change, twice.** A file carrying
  `compat_canonical_url` or `compat_alt_canonical_urls` refuses to start from
  M3, and one carrying `oidc.redirect_uri` refuses from M4. The admin override
  store migrates itself; the file does not.
- **`data_dir` and `master_key_file` remain the one group whose reset is not
  symmetric.** `DELETE /paths` restores the file's paths, but if the data has
  physically moved, that restores a path with no data behind it. The existing
  `set_paths` validation covers the forward direction; the reset dialog states
  the risk and the reset is still permitted, because the alternative is an
  unreachable override.
- **Nothing here fixes a wrong `Host` from a misconfigured proxy.** A proxy
  that forwards its own hostname makes every user look like they arrived on
  one origin. That is correct behaviour for this design and the reason the
  canonical entry exists.

## 9. References

- `crates/sc-server/src/config.rs`: `Config`, `resolve_compat_canonical_url`,
  `resolve_compat_alt_canonical_urls`, `SettingsOverrides`,
  `apply_settings_overrides`
- `crates/sc-server/src/settings_bridge.rs`: `snapshot_fields`, every `set_*`,
  `UI_EDITABLE_KEYS`
- `crates/sc-server/src/settings_store.rs`: the override row, and the module
  doc whose `deploy.sh` claim 3.2 corrects
- `scripts/deploy.sh` and `Dockerfile`: what a rollout actually pushes, which
  is a binary, a build context, and no config file
- `crates/sc-server/src/lib.rs`: `bootstrap`, override application order
- `crates/sc-server/src/app.rs`: `HttpConfig` assembly, `allowed_origins`
  derivation, `public_base_url`
- `crates/sc-server/src/tls.rs`: `san_entries`
- `crates/sc-server/src/diagnostics.rs`: the startup report the two new lines
  join
- `crates/sc-http/src/config.rs`: `HttpConfig`, `split_host_port`,
  `is_self_lan_origin`
- `crates/sc-http/src/middleware.rs`: `host_guard`, the CSRF origin check
- `crates/sc-http/src/routes.rs`: `public_link_url`, the admin settings
  handlers
- `crates/sc-http/src/settings_api.rs`: `SettingsField`, every patch type
- `crates/sc-http/src/content.rs`: `content_url`
- `crates/sc-compat-nc/src/config.rs`: `NcConfig::canonical_for_host`
- `web/src/lib/ui/admin/ServerSettingsSection.svelte`: `EDITABLE_KEYS`
- `docs/proposals/stowcloud-0-oidc-login.md` §4.3, §6-4
- `docs/proposals/stowcloud-6-preview-sharing.md`: the content origin
- `docs/proposals/stowcloud-8-compat.md`: the isolation contract
