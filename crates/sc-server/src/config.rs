//! Configuration: TOML file + environment overrides. Every default here is
//! also stated in the proposal for the subsystem that owns it; if one
//! changes, the other has to change with it.

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// 5 MiB — hard floor on chunk size for `sc.toml` at startup
/// (if a config file asks for less, `Config::load`
/// clamps up rather than honoring it). Defined in terms of
/// `sc_upload::CHUNK_MIN_BYTES_FLOOR`, the same floor `UploadEngine::
/// set_chunk_settings` enforces for an admin's runtime write, so the two
/// never drift apart.
pub const CHUNK_MIN_BYTES_FLOOR: u64 = sc_upload::CHUNK_MIN_BYTES_FLOOR;
/// 10 MiB — the documented default chunk size.
pub const CHUNK_DEFAULT_BYTES_DEFAULT: u64 = 10 * 1024 * 1024;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WatchBackend {
    #[default]
    Auto,
    Hotset,
    InotifyFull,
    Fanotify,
}

/// Mirrors `sc_vfs::SymlinkPolicy` with `serde` support (the upstream type
/// intentionally doesn't derive it — see `sc-vfs/src/types.rs`). Kept as a
/// distinct type rather than newtyping so this crate compiles even if
/// `sc-vfs`'s enum shape changes; `From` bridges the two explicitly.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SymlinkPolicyCfg {
    /// "default is least privilege" — symlinks denied by default.
    #[default]
    Deny,
    WithinShare,
    Follow,
}

impl From<SymlinkPolicyCfg> for sc_vfs::SymlinkPolicy {
    fn from(v: SymlinkPolicyCfg) -> Self {
        match v {
            SymlinkPolicyCfg::Deny => sc_vfs::SymlinkPolicy::Deny,
            SymlinkPolicyCfg::WithinShare => sc_vfs::SymlinkPolicy::WithinShare,
            SymlinkPolicyCfg::Follow => sc_vfs::SymlinkPolicy::Follow,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct DbConfig {
    /// off by default. Enabling it does not turn on
    /// any automatic remediation — crossing `max_bytes` only flips
    /// `Diagnostics::degraded_reasons()` to include `"db_size_guard_tripped"`,
    /// which `GET /api/health` can then report as a bare degraded/not-degraded
    /// signal (an earlier draft specified a
    /// five-step automatic degrade ladder — reap, halve audit retention,
    /// incremental vacuum, tighten allocation, block writes — none of which
    /// runs). Reclaiming space is the always-manual `sc-server gc`. Size/growth
    /// is always observable via `/api/admin/storage` regardless of this flag.
    pub size_guard: bool,
    pub max_bytes: u64,
    /// Always-on safety net (§4.4), independent of `size_guard`.
    pub min_free_bytes: u64,
}

impl Default for DbConfig {
    fn default() -> Self {
        Self {
            size_guard: false,
            max_bytes: 4 * 1024 * 1024 * 1024,
            min_free_bytes: 1024 * 1024 * 1024,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct UploadConfig {
    pub chunk_min_bytes: u64,
    pub chunk_default_bytes: u64,
}

impl Default for UploadConfig {
    fn default() -> Self {
        Self {
            chunk_min_bytes: CHUNK_MIN_BYTES_FLOOR,
            chunk_default_bytes: CHUNK_DEFAULT_BYTES_DEFAULT,
        }
    }
}

impl UploadConfig {
    /// Enforce the hard floor regardless of what a config file requested.
    fn normalize(&mut self) {
        if self.chunk_min_bytes < CHUNK_MIN_BYTES_FLOOR {
            self.chunk_min_bytes = CHUNK_MIN_BYTES_FLOOR;
        }
        if self.chunk_default_bytes < self.chunk_min_bytes {
            self.chunk_default_bytes = self.chunk_min_bytes;
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct WatchConfig {
    pub backend: WatchBackend,
    pub hot_set_max: u32,
    pub full_threshold: u32,
}

impl Default for WatchConfig {
    fn default() -> Self {
        Self {
            backend: WatchBackend::Auto,
            hot_set_max: 4096,
            full_threshold: 50_000,
        }
    }
}

/// Off by default.
///
/// `content_enabled` used to sit here beside `name_enabled` and nothing ever
/// read it: content indexing is not built, so the key was a switch that
/// looked supported and did nothing. A file that still sets it parses
/// normally and keeps doing what it always did, which is nothing.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct IndexConfig {
    pub name_enabled: bool,
}

/// "user homes are opt-in; homes.enabled = false by default".
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct HomesConfig {
    pub enabled: bool,
    pub root: Option<PathBuf>,
}

/// the global concurrent-search cap and T2 walk
/// deadline, split per storage-class tier (fast: NVMe/SATA SSD; slow:
/// rotational/network — `sc_http::search_limits::StorageClass::tier`).
/// `search_rate` is the existing per-user rate limit (§8: 30/min), not new,
/// but pulled into config here alongside the two new knobs so all three
/// resource bounds this section governs live in one place.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct SearchConfig {
    pub max_concurrent_fast: u32,
    pub max_concurrent_slow: u32,
    pub walk_deadline_fast_ms: u64,
    pub walk_deadline_slow_ms: u64,
    pub rate_per_minute: u32,
}

impl Default for SearchConfig {
    fn default() -> Self {
        Self {
            max_concurrent_fast: 4,
            max_concurrent_slow: 2,
            walk_deadline_fast_ms: 3_000,
            walk_deadline_slow_ms: 8_000,
            rate_per_minute: 30,
        }
    }
}

impl From<&SearchConfig> for sc_http::search_limits::SearchLimitsConfig {
    fn from(c: &SearchConfig) -> Self {
        Self {
            max_concurrent_fast: c.max_concurrent_fast,
            max_concurrent_slow: c.max_concurrent_slow,
            walk_deadline_fast: std::time::Duration::from_millis(c.walk_deadline_fast_ms),
            walk_deadline_slow: std::time::Duration::from_millis(c.walk_deadline_slow_ms),
        }
    }
}

/// `POST /api/fs/archive`: each stream holds an
/// open fd and walks a tree for its whole duration, so the global cap is a
/// config-reachable resource bound just like search's.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct ArchiveConfig {
    pub max_concurrent: u32,
}

impl Default for ArchiveConfig {
    fn default() -> Self {
        Self { max_concurrent: 4 }
    }
}

/// What an OIDC-linked account may do over SMB
/// (`docs/proposals/stowcloud-0-oidc-login.md` §4.3.6).
///
/// One variant, and that is the honest shape rather than an oversight. The
/// draft offered `require_separate` too, mirroring `smb.totp_policy` -- but
/// the only self-service SMB API a user has is
/// `POST /api/auth/smb`'s two booleans, and no route in this product creates
/// a `NtSource::Dedicated` secret at all. A policy value nobody can satisfy
/// is a promise the code does not keep, so `block` is all there is: linking
/// deletes the account-derived NT hash and republishes the passdb, and an
/// account password stops being an SMB credential.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OidcSmbPolicy {
    #[default]
    Block,
}

/// Mirrors [`sc_auth::OidcLocalPasswordLogin`] with `serde` support, the same
/// way [`SymlinkPolicyCfg`] mirrors `sc_vfs::SymlinkPolicy`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OidcLocalPasswordLoginCfg {
    /// The default, and deliberately the less strict of the two. See
    /// `sc_auth::OidcLocalPasswordLogin::Allow` for why: `deny` has no
    /// recovery path if the IdP goes away, and refuses that
    /// class of unrecoverable state everywhere else.
    #[default]
    Allow,
    Deny,
}

impl From<OidcLocalPasswordLoginCfg> for sc_auth::OidcLocalPasswordLogin {
    fn from(v: OidcLocalPasswordLoginCfg) -> Self {
        match v {
            OidcLocalPasswordLoginCfg::Allow => sc_auth::OidcLocalPasswordLogin::Allow,
            OidcLocalPasswordLoginCfg::Deny => sc_auth::OidcLocalPasswordLogin::Deny,
        }
    }
}

/// `[oidc]` -- proposal §6-4.
///
/// **A plain struct with `#[serde(default)]` and an `enabled` flag, never
/// `Option<OidcConfig>`.** With an `Option`, `Config::default()` serializes
/// this section as JSON `null`, and `settings_bridge`'s `config_leaf_keys`
/// flattens a `null` to the single key `"oidc"` -- so
/// `every_config_field_is_reachable_from_the_settings_screen` would pass
/// while nine of these ten settings had no row on the screen at all. `smb` is
/// modelled the same way for the same reason.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct OidcConfig {
    /// The operator's intent. Not the same thing as OIDC being *active* --
    /// see [`OidcConfig::inactive_reason`], which is what actually decides.
    pub enabled: bool,
    /// The IdP's issuer identifier, e.g. `https://accounts.google.com`. The
    /// discovery document's own `issuer` field must equal this exactly, and
    /// so must every ID token's `iss`.
    pub issuer: String,
    pub client_id: String,
    /// Path to a file holding the client secret, never the secret itself. A
    /// secret in `config.toml` is a secret in whatever `scripts/deploy.sh`
    /// pushes and in every backup of it; the same reasoning keeps
    /// `master_key_file` a path.
    pub client_secret_file: Option<PathBuf>,
    /// The exact redirect URIs registered at the IdP, e.g.
    /// `https://cloud.example.com/api/auth/oidc/callback`. The entry whose
    /// authority equals the request's `Host` is used; the first entry
    /// otherwise.
    ///
    /// Configured, never derived. A redirect URI has to match what is
    /// registered byte for byte, and the authorization request and the token
    /// request have to carry the same value, so this is not a place for
    /// inference. It is its own list rather than a slice of
    /// [`Config::public_origins`] for the same reason: OIDC works in a
    /// deployment that has declared no public origin at all, and coupling the
    /// two would break that deployment on upgrade.
    pub redirect_uris: Vec<String>,
    /// `openid` is added if it is missing -- without it the IdP issues no ID
    /// token and the whole flow is pointless. Nothing else is required:
    /// §3.2's non-goals rule out reading `email`, `name` or
    /// `preferred_username`, so asking for `profile` or `email` would collect
    /// consent for claims this server discards.
    pub scopes: Vec<String>,
    /// What the login screen writes on the button. Empty leaves the wording
    /// to the frontend's own default.
    pub display_name: String,
    /// Permit IdP endpoints that resolve to private, loopback or link-local
    /// addresses (§4.3.4). Off by default because that is the SSRF guard;
    /// on for the real deployments that run Keycloak on the same network.
    pub allow_private_endpoints: bool,
    pub smb_policy: OidcSmbPolicy,
    /// **`config.toml` only, never the settings screen** (§4.3.5, M5).
    /// `apply_settings_overrides` lets a stored override win over the file on
    /// every boot, so an operator who set this to `deny` from the screen and
    /// then lost their IdP could not undo it by editing the file -- the
    /// unrecoverable state the `allow` default exists to avoid.
    pub local_password_login: OidcLocalPasswordLoginCfg,
}

impl Default for OidcConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            issuer: String::new(),
            client_id: String::new(),
            client_secret_file: None,
            redirect_uris: Vec::new(),
            scopes: vec!["openid".to_string()],
            display_name: String::new(),
            allow_private_endpoints: false,
            smb_policy: OidcSmbPolicy::default(),
            local_password_login: OidcLocalPasswordLoginCfg::default(),
        }
    }
}

/// Why one `oidc.redirect_uris` entry cannot be used, or `None` when it can.
///
/// The `app_hosts` half is checked against the operator's own list, not the
/// wider set the host guard admits: `app.rs` injects the bind address and the
/// guard additionally lets through any private-range IP literal, and neither
/// is a name somebody typed into an identity provider's console by hand.
pub fn redirect_uri_problem(uri: &str, app_hosts: &[String]) -> Option<String> {
    let uri = uri.trim();
    if uri.is_empty() {
        return Some("must not be empty".into());
    }
    let Some(rest) = uri.strip_prefix("https://") else {
        return Some(format!("must start with https:// (got {uri})"));
    };
    let authority = rest.split(['/', '?', '#']).next().unwrap_or("");
    if authority.is_empty() {
        return Some(format!("names no host ({uri})"));
    }
    let (host, _) = sc_http::config::split_host_port(authority);
    if !app_hosts.iter().any(|h| h.trim().eq_ignore_ascii_case(host)) {
        return Some(format!(
            "{host} is not in app_hosts, so the callback would be refused with 421"
        ));
    }
    None
}

impl OidcConfig {
    /// Why OIDC will not activate, or `None` when it will.
    ///
    /// An empty or unusable `redirect_uris` means OIDC does not come up,
    /// **and everything else on the server keeps working**. The reason is
    /// returned rather than logged here so that the one caller
    /// (`crate::oidc::build_oidc`) can log it once, with the same wording an
    /// operator will search for.
    pub fn inactive_reason(&self, app_hosts: &[String]) -> Option<String> {
        if !self.enabled {
            return Some("oidc.enabled is false".into());
        }
        if self.issuer.trim().is_empty() {
            return Some("oidc.issuer is empty".into());
        }
        if self.client_id.trim().is_empty() {
            return Some("oidc.client_id is empty".into());
        }
        if self.redirect_uris.iter().all(|u| u.trim().is_empty()) {
            return Some("oidc.redirect_uris is empty".into());
        }
        for uri in &self.redirect_uris {
            if let Some(problem) = redirect_uri_problem(uri, app_hosts) {
                return Some(format!("oidc.redirect_uris: {problem}"));
            }
        }
        match &self.client_secret_file {
            None => Some("oidc.client_secret_file is not set".into()),
            Some(p) if !p.is_file() => Some(format!(
                "oidc.client_secret_file does not exist: {}",
                p.display()
            )),
            Some(_) => None,
        }
    }

    /// The registered redirect URI for the origin this request arrived on,
    /// falling back to the first entry. Selection only: the `Host` header
    /// never contributes a byte to the result.
    pub fn redirect_uri_for_host(&self, host: Option<&str>) -> Option<&str> {
        sc_http::config::select_by_authority(&self.redirect_uris, host)
    }
}

/// A share to bootstrap at first run from static config. Real deployments
/// manage shares dynamically through the admin API once `sc-core` exists;
/// this is only here so startup diagnostics has
/// something concrete to probe.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ShareBootstrap {
    pub name: String,
    pub host_path: PathBuf,
    /// Another service (Jellyfin, a Samba consumer) also reads and writes this
    /// directory, so the UI warns before a destructive action. Only the operator knows this — nothing on the filesystem says a
    /// directory is co-owned.
    #[serde(default)]
    pub shared_externally: bool,
}

/// A statically-configured Samba share. models real
/// per-user access control as a dynamic Share/Grant registry — that lives in
/// `sc-core`/`sc-acl` and isn't wired yet (see `smb_cmd.rs`), so this is the
/// interim: an admin lists exactly what `sc-smb` should render, one entry
/// per Samba `[share]` (one per distinct subpath grant,
/// "subpath grant").
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SmbShareBootstrap {
    pub name: String,
    pub path: String,
    #[serde(default)]
    pub valid_users: Vec<String>,
    #[serde(default)]
    pub read_list: Vec<String>,
    #[serde(default)]
    pub write_list: Vec<String>,
    #[serde(default)]
    pub shared_externally: bool,
}

impl From<&SmbShareBootstrap> for sc_smb::SmbShareDef {
    fn from(b: &SmbShareBootstrap) -> Self {
        sc_smb::SmbShareDef {
            name: b.name.clone(),
            path: b.path.clone(),
            valid_users: b.valid_users.clone(),
            read_list: b.read_list.clone(),
            write_list: b.write_list.clone(),
            shared_externally: b.shared_externally,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct Config {
    pub data_dir: PathBuf,
    /// The one socket this server listens on, and it always speaks TLS
    /// (`tls.rs` generates the certificate on first run).
    ///
    /// There is deliberately no plaintext listener, on any interface, for any
    /// caller. A reverse proxy terminating the public name therefore talks
    /// `https://` upstream and skips verification of this self-signed
    /// certificate, which costs the proxy one line of config and removes the
    /// class of accident where a port meant for loopback ends up published.
    ///
    /// This was two listeners briefly, plaintext for the proxy and TLS for the
    /// LAN. The split is what made "which port is safe to expose" a question an
    /// operator had to keep answering correctly, and the answer was already
    /// wrong once in this repo's own compose file.
    pub bind: SocketAddr,
    /// Hostnames this server answers to for the application UI and API.
    /// The Host header carries whatever literal the client dialled, so the
    /// bind address is added automatically (`app.rs`) — otherwise a fresh
    /// server 421s every request from its own address.
    pub app_hosts: Vec<String>,
    /// Hostname(s) serving user content. **Empty means single-origin**: user
    /// content is served from the app origin, which
    /// permits but which gives up the XSS isolation the split exists for.
    /// Startup says so out loud.
    pub content_hosts: Vec<String>,
    /// Origins the CSRF check accepts. **Empty means
    /// derive from `app_hosts`**, which is right for every ordinary
    /// deployment — an origin is a scheme plus a host we already answer for.
    /// Set it explicitly only when the browser's origin is something the
    /// server never sees in a `Host` header.
    #[serde(default)]
    pub allowed_origins: Vec<String>,
    /// Externally reachable origins, `scheme://host[:port]`, no trailing
    /// slash. The first entry is canonical: it is what a request on an
    /// unrecognised `Host` is answered with, and what a URL built outside a
    /// request uses.
    ///
    /// **A request's `Host` never builds a URL. It only selects among these.**
    /// That is what makes host-awareness safe here: an attacker who controls
    /// `Host` can only ever make this server name a host it already serves,
    /// and anything unrecognised falls back to the canonical entry.
    ///
    /// Everything the server hands out reads this list: the compat layer's
    /// Login Flow v2 `login`/`poll.endpoint` URLs a real device's system
    /// browser opens and then binds to **permanently**, the `theming.url`
    /// capability, and every public share link.
    ///
    /// Set this explicitly in production. Left unset:
    /// - exactly one `app_hosts` entry: still auto-derived as
    ///   `https://{that entry}` (unambiguous — there is only one thing it
    ///   could mean), logged loudly at startup so the derivation is visible
    ///   rather than assumed;
    /// - zero or more-than-one `app_hosts` entries: **ambiguous, and the
    ///   compatibility layer does not mount at all** rather than guess which
    ///   host a real client should be told to bind to. Every other surface
    ///   (the native web UI, WebDAV, the plain API) is completely unaffected,
    ///   and share links fall back to a guess that startup says out loud.
    ///
    /// A malformed entry after the first is dropped with a warning; a
    /// malformed *first* entry invalidates the list, because letting the
    /// second entry silently become canonical is the failure this key exists
    /// to prevent.
    #[serde(default)]
    pub public_origins: Vec<String>,
    /// Path to the master key file. Populated from `SC_MASTER_KEY_FILE` if
    /// unset in the file (`masterkey.rs`). Never the key material itself.
    pub master_key_file: Option<PathBuf>,
    pub db: DbConfig,
    pub upload: UploadConfig,
    pub watch: WatchConfig,
    pub index: IndexConfig,
    pub search: SearchConfig,
    pub archive: ArchiveConfig,
    pub symlink_policy: SymlinkPolicyCfg,
    pub homes: HomesConfig,
    pub smb: sc_smb::SmbConfig,
    /// Force-user uid/gid Samba runs every connection as ( `force user`/`force group`; §6.1 default compose `user: "1000:1000"`).
    pub smb_service_uid: u32,
    pub smb_service_gid: u32,
    pub smb_shares: Vec<SmbShareBootstrap>,
    pub trusted_proxies: Vec<String>,
    pub shares: Vec<ShareBootstrap>,
    pub oidc: OidcConfig,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            data_dir: PathBuf::from("/var/lib/sc"),
            bind: SocketAddr::new(IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)), 8443),
            app_hosts: sc_http::config::HttpConfig::default().app_hosts,
            content_hosts: Vec::new(),
            allowed_origins: Vec::new(),
            public_origins: Vec::new(),
            master_key_file: None,
            db: DbConfig::default(),
            upload: UploadConfig::default(),
            watch: WatchConfig::default(),
            index: IndexConfig::default(),
            search: SearchConfig::default(),
            archive: ArchiveConfig::default(),
            symlink_policy: SymlinkPolicyCfg::default(),
            homes: HomesConfig::default(),
            smb: sc_smb::SmbConfig::default(),
            smb_service_uid: 1000,
            smb_service_gid: 1000,
            smb_shares: Vec::new(),
            trusted_proxies: Vec::new(),
            shares: Vec::new(),
            oidc: OidcConfig::default(),
        }
    }
}

impl Config {
    /// Parse a TOML document on top of every documented default — any key
    /// the file omits keeps its default (`#[serde(default)]` all the way
    /// down), so a one-line config file is valid.
    ///
    /// Two keys are refused rather than ignored: see [`refuse_removed_keys`].
    pub fn from_toml_str(s: &str) -> anyhow::Result<Config> {
        let doc: toml::Value = toml::from_str(s)?;
        refuse_removed_keys(&doc)?;
        let mut cfg: Config = doc.try_into()?;
        cfg.upload.normalize();
        Ok(cfg)
    }

    /// Where `load` looks when `--config` was not given: `sc.toml` in the
    /// data directory, which is the one path a deployment already has to
    /// mount for anything else to work.
    ///
    /// `SC_DATA_DIR` is read here rather than after parsing because this
    /// decides *which file* to parse, and the file cannot name its own
    /// location. That matches the env-wins rule everywhere else, and the
    /// compose reference sets exactly that variable.
    pub fn default_config_path() -> PathBuf {
        let data_dir = match std::env::var("SC_DATA_DIR") {
            Ok(v) if !v.is_empty() => PathBuf::from(v),
            _ => Config::default().data_dir,
        };
        data_dir.join("sc.toml")
    }

    /// Load from `path` if given and existing, else from
    /// [`Config::default_config_path`] if that exists, else pure defaults;
    /// then apply environment overrides.
    ///
    /// **Precedence, as the code actually behaves: file, then env, then the
    /// admin override store.** This function does the first two; `bootstrap()`
    /// applies the third on top, so a settings-screen save beats `SC_BIND`,
    /// `SC_TRUSTED_PROXIES`, `SC_DATA_DIR` and `SC_MASTER_KEY_FILE` on every
    /// subsequent boot. That is reversible from the same screen
    /// (`DELETE /api/admin/server-settings/{section}`) and the startup report
    /// names every active override and the variable it shadows, but "env
    /// always wins", which this comment used to claim, was never true.
    ///
    /// The default location is not decoration. `--config`'s help text
    /// promised it from the start and nothing implemented it, so a
    /// deployment that wrote `<data_dir>/sc.toml` and restarted got a server
    /// with no shares, no `public_origins`, and no indication that the
    /// file it had just written was never opened. Returns the path actually
    /// used alongside the config so the caller can say so out loud.
    pub fn load(path: Option<&Path>) -> anyhow::Result<Config> {
        Ok(Self::load_from(path)?.0)
    }

    /// [`Config::load`], plus which file it came from (`None` for built-in
    /// defaults).
    pub fn load_from(path: Option<&Path>) -> anyhow::Result<(Config, Option<PathBuf>)> {
        let chosen = match path {
            Some(p) if p.exists() => Some(p.to_path_buf()),
            // An explicit `--config` that does not exist is a typo, not an
            // invitation to fall back to something else and start anyway.
            Some(p) => anyhow::bail!("config file {} does not exist", p.display()),
            None => {
                let d = Self::default_config_path();
                d.exists().then_some(d)
            }
        };
        let mut cfg = match &chosen {
            Some(p) => {
                let text = std::fs::read_to_string(p)
                    .map_err(|e| anyhow::anyhow!("reading {}: {e}", p.display()))?;
                Config::from_toml_str(&text)?
            }
            None => Config::default(),
        };
        cfg.apply_env();
        cfg.upload.normalize();
        Ok((cfg, chosen))
    }

    /// Environment overrides. Deliberately narrow: only the handful of
    /// values's compose example actually sets via env.
    /// `SC_MASTER_KEY` (the key material) is intentionally *not* among
    /// these — see `masterkey.rs`.
    fn apply_env(&mut self) {
        if let Ok(v) = std::env::var("SC_DATA_DIR") {
            if !v.is_empty() {
                self.data_dir = PathBuf::from(v);
            }
        }
        if let Ok(v) = std::env::var("SC_MASTER_KEY_FILE") {
            if !v.is_empty() {
                self.master_key_file = Some(PathBuf::from(v));
            }
        }
        if let Ok(v) = std::env::var("SC_TRUSTED_PROXIES") {
            if !v.is_empty() {
                self.trusted_proxies = v.split(',').map(|s| s.trim().to_string()).collect();
            }
        }
        if let Ok(v) = std::env::var("SC_BIND") {
            if let Ok(addr) = v.parse::<SocketAddr>() {
                self.bind = addr;
            }
        }
    }

    pub fn master_key_path(&self) -> PathBuf {
        self.master_key_file
            .clone()
            .unwrap_or_else(|| self.data_dir.join("master.key"))
    }

    /// Where per-user home directories live when
    /// `homes.enabled`. `homes.root` unset defaults under `data_dir`, same
    /// convention as `master_key_path` above.
    pub fn homes_root(&self) -> PathBuf {
        self.homes.root.clone().unwrap_or_else(|| self.data_dir.join("homes"))
    }

    /// Resolve [`Config::public_origins`] against [`Config::app_hosts`],
    /// alongside the malformed entries that were dropped so the caller can
    /// warn about them.
    ///
    /// Shared by `app.rs` (decides whether the compat layer mounts at all,
    /// and what every share link is built from) and `diagnostics.rs` (reports
    /// the outcome at startup, the same way `single_origin`/`trusted_proxies`
    /// already do) — one function, so the two can never disagree about what
    /// "configured" means.
    pub fn resolve_public_origins(&self) -> (PublicOrigins, Vec<String>) {
        let declared: Vec<&String> = self
            .public_origins
            .iter()
            .filter(|v| !v.trim().is_empty())
            .collect();
        if declared.is_empty() {
            let resolved = match self.app_hosts.as_slice() {
                [single] => PublicOrigins::Derived(vec![format!("https://{single}")]),
                other => PublicOrigins::Ambiguous {
                    app_host_count: other.len(),
                },
            };
            return (resolved, Vec::new());
        }

        let mut good: Vec<String> = Vec::new();
        let mut bad: Vec<String> = Vec::new();
        for (i, raw) in declared.iter().enumerate() {
            let t = raw.trim().trim_end_matches('/');
            if !is_absolute_origin(t) {
                // Dropping the first entry would silently promote the second
                // to canonical, which is the one substitution this key exists
                // to prevent. Anywhere else costs that one name.
                if i == 0 {
                    return (PublicOrigins::Invalid((*raw).clone()), vec![(*raw).clone()]);
                }
                bad.push((*raw).clone());
            } else if !good.iter().any(|g| g == t) {
                good.push(t.to_string());
            }
        }
        (PublicOrigins::Configured(good), bad)
    }
}

/// Is this an absolute `http(s)://` origin with a non-empty authority?
///
/// Not a full URL parse (no `url` crate dependency here) — just enough to
/// catch the realistic operator mistakes: a bare host with no scheme, or a
/// scheme this server cannot hand a real client (Login Flow v2's `login` URL
/// is opened in a system browser, which understands http(s) and nothing else).
pub fn is_absolute_origin(s: &str) -> bool {
    let s = s.trim();
    let has_scheme = s.starts_with("https://") || s.starts_with("http://");
    has_scheme && s.split("://").nth(1).is_some_and(|h| !h.is_empty())
}

/// Keys a previous release accepted and this one does not. Refused rather
/// than ignored, because ignoring changes behaviour under an operator who
/// touched nothing: a deployment that comes back up with no declared origin
/// either stops mounting the compat layer (every enrolled client starts
/// getting 404s) or silently re-derives a different canonical URL and logs it
/// as a normal derivation.
const REMOVED_KEYS: &[(&str, &str, &str)] = &[
    (
        "",
        "compat_canonical_url",
        "public_origins = [\"https://cloud.example.com\"]",
    ),
    (
        "",
        "compat_alt_canonical_urls",
        "public_origins = [\"https://cloud.example.com\", \"https://nas.internal\"]",
    ),
    (
        "oidc",
        "redirect_uri",
        "redirect_uris = [\"https://cloud.example.com/api/auth/oidc/callback\"]",
    ),
];

/// A pre-pass over the parsed document, because serde will not raise this:
/// `Config` does not set `deny_unknown_fields`, and turning that on globally
/// would start rejecting every forward-compatible key an operator has.
fn refuse_removed_keys(doc: &toml::Value) -> anyhow::Result<()> {
    for (table, key, replacement) in REMOVED_KEYS {
        let present = if table.is_empty() {
            doc.get(key).is_some()
        } else {
            doc.get(table).and_then(|t| t.get(key)).is_some()
        };
        if present {
            let full = if table.is_empty() {
                (*key).to_string()
            } else {
                format!("[{table}] {key}")
            };
            anyhow::bail!(
                "`{full}` was removed. Replace it with:\n    {replacement}\n\
                 The server refuses to start rather than ignore it: a deployment that \
                 came back up with no declared origin would hand new clients a \
                 different one, silently."
            );
        }
    }
    Ok(())
}

// --------------------------------------------------------- admin overrides
// The server-settings admin screen's persisted state (`settings_store.rs`,
// `settings_bridge.rs`): changes made from the UI, kept outside
// `config.toml` because that file is the operator's, and a server that
// rewrote it would have to merge with whatever they were editing — a
// comment-preserving TOML round-trip is a dependency and a class of bug this
// does not need. Same reasoning as `sc-upload`'s `upload_chunk_settings` and
// `sc-search`'s `IndexSettingsStore`. Each section is `Option` and
// full-replace (not per-field partial), matching `admin_set_upload_settings`'s
// existing precedent: the UI always sends the whole group it's editing, and
// `DELETE /api/admin/server-settings/{section}` puts it back to `None`.

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct NetworkOverride {
    pub bind: SocketAddr,
    pub app_hosts: Vec<String>,
    pub content_hosts: Vec<String>,
    pub allowed_origins: Vec<String>,
    pub trusted_proxies: Vec<String>,
    pub public_origins: Vec<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct DbOverride {
    pub size_guard: bool,
    pub max_bytes: u64,
    pub min_free_bytes: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct HomesOverride {
    pub enabled: bool,
    pub root: Option<PathBuf>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SmbOverride {
    pub enabled: bool,
    pub workgroup: String,
    /// Defaulted, because overrides stored before this field existed have no
    /// key for it and must still deserialize.
    #[serde(default)]
    pub server_name: String,
    pub service_user: String,
    pub allow_public_bind: bool,
    pub totp_policy: sc_smb::TotpPolicy,
    pub service_uid: u32,
    pub service_gid: u32,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SearchOverride {
    pub max_concurrent_fast: u32,
    pub max_concurrent_slow: u32,
    pub walk_deadline_fast_ms: u64,
    pub walk_deadline_slow_ms: u64,
    pub rate_per_minute: u32,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct WatchOverride {
    pub backend: WatchBackend,
    pub hot_set_max: u32,
    pub full_threshold: u32,
}

/// The editable half of `[oidc]`.
///
/// `client_secret_file`, `local_password_login` and the secret itself are
/// deliberately absent, and that absence is the point (§4.3.5, M5).
/// [`Config::apply_settings_overrides`] lets a stored override beat
/// `config.toml` on every boot, so anything reachable from the settings
/// screen is a value an operator can only undo from the settings screen. That
/// is fine for a hostname and fatal for the two settings whose wrong value
/// locks everyone, administrators included, out of the deployment that would
/// have to be used to fix them.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct OidcOverride {
    pub enabled: bool,
    pub issuer: String,
    pub client_id: String,
    pub redirect_uris: Vec<String>,
    pub scopes: Vec<String>,
    pub display_name: String,
    pub allow_private_endpoints: bool,
    pub smb_policy: OidcSmbPolicy,
}

/// The three bootstrap paths. Grouped because they are read together exactly
/// once, before anything else exists, and because two of them constrain each
/// other: `master_key_file` defaults to `<data_dir>/master.key`, so moving
/// `data_dir` moves the key too unless the file is named explicitly.
/// `SettingsBridge::set_paths` refuses any combination that would not start.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct PathsOverride {
    pub data_dir: PathBuf,
    pub master_key_file: Option<PathBuf>,
    pub smb_config_dir: PathBuf,
}

/// One row in `settings.db`, loaded once at [`Config::apply_settings_overrides`]
/// (called from `bootstrap()`, so every entry point — `serve`, `gc`,
/// `smb-sync` — sees the same effective config) and re-applied live by
/// `SettingsBridge` for the fields that don't need a restart.
///
/// `#[serde(default)]` is load-bearing, not decoration: `SettingsStore` reads
/// this blob with `unwrap_or_default()`, so a field added in a later release
/// would make every already-persisted row fail to deserialize and silently
/// reset *every* override an admin had made. With it, an old row just leaves
/// the new section `None`.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct SettingsOverrides {
    pub network: Option<NetworkOverride>,
    pub db: Option<DbOverride>,
    pub symlink_policy: Option<SymlinkPolicyCfg>,
    pub homes: Option<HomesOverride>,
    pub smb: Option<SmbOverride>,
    pub archive_max_concurrent: Option<u32>,
    pub search: Option<SearchOverride>,
    pub watch: Option<WatchOverride>,
    pub paths: Option<PathsOverride>,
    pub oidc: Option<OidcOverride>,
}

impl SettingsOverrides {
    /// Drop one section, so `config.toml` and the environment decide it again.
    ///
    /// Without this every group was a one-way door: after the first save of a
    /// group, the file was dead for every key in it and the only recovery was
    /// deleting `settings.db`, which discards the other nine groups with it.
    pub fn clear(&mut self, section: sc_http::settings_api::SettingsSection) {
        use sc_http::settings_api::SettingsSection as S;
        match section {
            S::Network => self.network = None,
            S::Db => self.db = None,
            S::SymlinkPolicy => self.symlink_policy = None,
            S::Homes => self.homes = None,
            S::Smb => self.smb = None,
            S::Search => self.search = None,
            S::Archive => self.archive_max_concurrent = None,
            S::Watch => self.watch = None,
            S::Paths => self.paths = None,
            S::Oidc => self.oidc = None,
        }
    }

    /// Is this section currently overriding the file? Drives the settings
    /// screen's per-group revert button and the startup log line.
    pub fn is_set(&self, section: sc_http::settings_api::SettingsSection) -> bool {
        use sc_http::settings_api::SettingsSection as S;
        match section {
            S::Network => self.network.is_some(),
            S::Db => self.db.is_some(),
            S::SymlinkPolicy => self.symlink_policy.is_some(),
            S::Homes => self.homes.is_some(),
            S::Smb => self.smb.is_some(),
            S::Search => self.search.is_some(),
            S::Archive => self.archive_max_concurrent.is_some(),
            S::Watch => self.watch.is_some(),
            S::Paths => self.paths.is_some(),
            S::Oidc => self.oidc.is_some(),
        }
    }
}

impl Config {
    /// Fold a persisted admin override on top of the file+env config. Called
    /// once from `bootstrap()`, before any `App`/orchestrator is built, so
    /// `cmd_serve`/`cmd_gc`/`cmd_smb_sync` all see the same effective values
    /// with no special-casing per entry point.
    pub fn apply_settings_overrides(&mut self, o: &SettingsOverrides) {
        if let Some(n) = &o.network {
            self.bind = n.bind;
            self.app_hosts = n.app_hosts.clone();
            self.content_hosts = n.content_hosts.clone();
            self.allowed_origins = n.allowed_origins.clone();
            self.trusted_proxies = n.trusted_proxies.clone();
            self.public_origins = n.public_origins.clone();
        }
        if let Some(d) = &o.db {
            self.db = DbConfig {
                size_guard: d.size_guard,
                max_bytes: d.max_bytes,
                min_free_bytes: d.min_free_bytes,
            };
        }
        if let Some(p) = o.symlink_policy {
            self.symlink_policy = p;
        }
        if let Some(h) = &o.homes {
            self.homes.enabled = h.enabled;
            self.homes.root = h.root.clone();
        }
        if let Some(s) = &o.smb {
            self.smb.enabled = s.enabled;
            self.smb.workgroup = s.workgroup.clone();
            self.smb.server_name = s.server_name.clone();
            self.smb.service_user = s.service_user.clone();
            self.smb.allow_public_bind = s.allow_public_bind;
            self.smb.totp_policy = s.totp_policy;
            self.smb_service_uid = s.service_uid;
            self.smb_service_gid = s.service_gid;
        }
        if let Some(a) = o.archive_max_concurrent {
            self.archive.max_concurrent = a;
        }
        if let Some(se) = &o.search {
            self.search = SearchConfig {
                max_concurrent_fast: se.max_concurrent_fast,
                max_concurrent_slow: se.max_concurrent_slow,
                walk_deadline_fast_ms: se.walk_deadline_fast_ms,
                walk_deadline_slow_ms: se.walk_deadline_slow_ms,
                rate_per_minute: se.rate_per_minute,
            };
        }
        if let Some(w) = &o.watch {
            self.watch = WatchConfig {
                backend: w.backend,
                hot_set_max: w.hot_set_max,
                full_threshold: w.full_threshold,
            };
        }
        // `bootstrap()` opened `settings.db` at *config.toml's* `data_dir`
        // before calling this, so the override store itself never moves —
        // which is what makes a `data_dir` override reversible from the same
        // screen that set it, however wrong the new path turns out to be.
        if let Some(p) = &o.paths {
            self.data_dir = p.data_dir.clone();
            self.master_key_file = p.master_key_file.clone();
            self.smb.config_dir = p.smb_config_dir.clone();
        }
        // Seven of ten `[oidc]` keys. `client_secret_file` and
        // `local_password_login` are not here on purpose -- see
        // `OidcOverride`'s doc comment for the lockout this omission
        // prevents.
        if let Some(oi) = &o.oidc {
            self.oidc.enabled = oi.enabled;
            self.oidc.issuer = oi.issuer.clone();
            self.oidc.client_id = oi.client_id.clone();
            self.oidc.redirect_uris = oi.redirect_uris.clone();
            self.oidc.scopes = oi.scopes.clone();
            self.oidc.display_name = oi.display_name.clone();
            self.oidc.allow_private_endpoints = oi.allow_private_endpoints;
            self.oidc.smb_policy = oi.smb_policy;
        }
        self.upload.normalize();
    }
}

/// See [`Config::resolve_public_origins`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PublicOrigins {
    /// `public_origins` was set, and its first entry is a well-formed
    /// absolute `http(s)://host[:port]` origin (trailing slash stripped,
    /// duplicates collapsed). Malformed later entries are already dropped.
    Configured(Vec<String>),
    /// Not configured, but `app_hosts` had exactly one entry — unambiguous,
    /// so it is safe to derive `https://{that entry}` automatically. Still
    /// reported loudly at startup (`diagnostics.rs`): implicit is not the
    /// same as wrong, but an operator relying on this for a production
    /// Login Flow v2 deployment should be told the derivation is happening.
    Derived(Vec<String>),
    /// Not configured, and `app_hosts` had zero or more than one entry: no
    /// single value can be chosen without guessing which host a real client
    /// should permanently bind to. The compat layer does not mount at all in
    /// this state (`app.rs`) and share links fall back to a guess — every
    /// other surface (the native web UI, WebDAV, the plain API) is
    /// unaffected.
    Ambiguous { app_host_count: usize },
    /// The first `public_origins` entry is not an absolute `http(s)://`
    /// origin. Treated the same as `Ambiguous` for mounting purposes — a
    /// value this layer cannot even parse is worse than no value at all — but
    /// reported distinctly so the startup log points at the actual mistake
    /// instead of "app_hosts is ambiguous", which would be a red herring.
    ///
    /// Deliberately *not* the same as absent: an absent value with one
    /// `app_hosts` entry derives happily, while a typo refuses. Treating a
    /// typo as "nothing declared" would let a single-host deployment silently
    /// derive an origin the operator never wrote.
    Invalid(String),
}

impl PublicOrigins {
    /// Every declared origin, canonical first. Empty in the ambiguous and
    /// invalid cases, where callers fall back to their own last resort.
    pub fn origins(&self) -> &[String] {
        match self {
            Self::Configured(v) | Self::Derived(v) => v,
            Self::Ambiguous { .. } | Self::Invalid(_) => &[],
        }
    }

    pub fn canonical(&self) -> Option<&str> {
        self.origins().first().map(String::as_str)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A `settings.db` row written while `tls_bind` existed still loads, and
    /// cannot bring a second listener back.
    ///
    /// There was a brief release with a plaintext listener beside the TLS one,
    /// so rows naming `tls_bind` are out there. Serde ignores the unknown key,
    /// which is the whole point: "no plaintext listener on any network" has to
    /// be a property of the binary, not something a stored override can talk it
    /// out of.
    #[test]
    fn a_stored_override_from_the_two_listener_release_still_loads() {
        let stored: SettingsOverrides = serde_json::from_str(
            r#"{"network":{"bind":"0.0.0.0:8080","tls_bind":"0.0.0.0:8443",
                "app_hosts":["nas.local"],"content_hosts":[],"allowed_origins":[],
                "trusted_proxies":[],"public_origins":[]}}"#,
        )
        .expect("a row from the two-listener release must still deserialize");

        let mut cfg = Config::default();
        cfg.apply_settings_overrides(&stored);

        // The bind it names is now the TLS listener, because that is the only
        // kind there is.
        assert_eq!(cfg.bind, "0.0.0.0:8080".parse().unwrap());
        assert_eq!(cfg.app_hosts, vec!["nas.local".to_string()]);
    }

    #[test]
    fn defaults_match_design_docs() {
        let cfg = Config::default();
        assert_eq!(cfg.upload.chunk_min_bytes, 5 * 1024 * 1024);
        assert_eq!(cfg.upload.chunk_default_bytes, 10 * 1024 * 1024);
        assert!(!cfg.db.size_guard);
        assert!(!cfg.index.name_enabled);
        assert_eq!(cfg.watch.backend, WatchBackend::Auto);
        assert_eq!(cfg.watch.hot_set_max, 4096);
        assert_eq!(cfg.symlink_policy, SymlinkPolicyCfg::Deny);
        assert!(!cfg.homes.enabled);
        assert!(!cfg.smb.enabled);
        assert!(!cfg.smb.allow_public_bind);
        //
        assert_eq!(cfg.search.max_concurrent_fast, 4);
        assert_eq!(cfg.search.max_concurrent_slow, 2);
        assert_eq!(cfg.search.walk_deadline_fast_ms, 3_000);
        assert_eq!(cfg.search.walk_deadline_slow_ms, 8_000);
        assert_eq!(cfg.archive.max_concurrent, 4);
    }

    #[test]
    fn search_and_archive_config_are_toml_reachable() {
        let cfg = Config::from_toml_str(
            r#"
            [search]
            max_concurrent_fast = 8
            max_concurrent_slow = 1

            [archive]
            max_concurrent = 2
            "#,
        )
        .unwrap();
        assert_eq!(cfg.search.max_concurrent_fast, 8);
        assert_eq!(cfg.search.max_concurrent_slow, 1);
        // Untouched keys in a partially-specified section keep their default.
        assert_eq!(cfg.search.walk_deadline_fast_ms, 3_000);
        assert_eq!(cfg.archive.max_concurrent, 2);
    }

    #[test]
    fn empty_toml_keeps_defaults() {
        let cfg = Config::from_toml_str("").unwrap();
        assert_eq!(cfg.upload.chunk_default_bytes, 10 * 1024 * 1024);
    }

    #[test]
    fn partial_toml_only_overrides_given_keys() {
        let cfg = Config::from_toml_str(
            r#"
            [watch]
            hot_set_max = 8192
            "#,
        )
        .unwrap();
        assert_eq!(cfg.watch.hot_set_max, 8192);
        assert_eq!(cfg.watch.backend, WatchBackend::Auto);
        assert!(!cfg.db.size_guard);
    }

    #[test]
    fn chunk_min_floor_cannot_be_configured_below_5mib() {
        let cfg = Config::from_toml_str(
            r#"
            [upload]
            chunk_min_bytes = 1024
            chunk_default_bytes = 2048
            "#,
        )
        .unwrap();
        assert_eq!(cfg.upload.chunk_min_bytes, CHUNK_MIN_BYTES_FLOOR);
        assert_eq!(cfg.upload.chunk_default_bytes, CHUNK_MIN_BYTES_FLOOR);
    }

    // --- `resolve_public_origins` ------------------------------------------

    fn origins_of(cfg: &Config) -> PublicOrigins {
        cfg.resolve_public_origins().0
    }

    #[test]
    fn explicit_origins_win_over_app_hosts() {
        let cfg = Config {
            app_hosts: vec!["a.example".into(), "b.example".into()],
            public_origins: vec!["https://cloud.example.com/".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&cfg),
            PublicOrigins::Configured(vec!["https://cloud.example.com".into()]),
            "trailing slash must be stripped, same as NcConfig::url() expects"
        );
    }

    /// Ordering is meaningful: the first entry is what an unrecognised `Host`
    /// is answered with, so a duplicate is collapsed rather than refused and
    /// the rest keep their order.
    #[test]
    fn several_origins_keep_their_order_and_collapse_duplicates() {
        let cfg = Config {
            public_origins: vec![
                "https://cloud.example.com".into(),
                "https://nas.internal:8443/".into(),
                "https://cloud.example.com".into(),
            ],
            ..Config::default()
        };
        let (resolved, rejected) = cfg.resolve_public_origins();
        assert_eq!(
            resolved,
            PublicOrigins::Configured(vec![
                "https://cloud.example.com".into(),
                "https://nas.internal:8443".into(),
            ])
        );
        assert!(rejected.is_empty());
        assert_eq!(resolved.canonical(), Some("https://cloud.example.com"));
    }

    #[test]
    fn single_app_host_is_derived_unambiguously() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&cfg),
            PublicOrigins::Derived(vec!["https://cloud.example.com".into()])
        );
    }

    #[test]
    fn zero_or_many_app_hosts_without_an_explicit_value_is_ambiguous() {
        let empty = Config {
            app_hosts: vec![],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&empty),
            PublicOrigins::Ambiguous { app_host_count: 0 }
        );

        let many = Config {
            app_hosts: vec!["a.example".into(), "b.example".into(), "c.example".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&many),
            PublicOrigins::Ambiguous { app_host_count: 3 },
            "'first entry wins' silently repoints Login Flow v2 if the list is ever \
             reordered, so it must not be chosen automatically"
        );
    }

    #[test]
    fn a_value_with_no_scheme_is_invalid_not_silently_ambiguous() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            public_origins: vec!["cloud.example.com".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&cfg),
            PublicOrigins::Invalid("cloud.example.com".into()),
            "a bare host is the realistic operator typo (forgetting the scheme); it must \
             be reported as its own mistake, not folded into the generic 'ambiguous' case"
        );
    }

    /// A typo at position one is fatal to the list; anywhere else it costs
    /// that one name. Promoting the second entry to canonical would change
    /// what every future client is told to bind to, silently.
    #[test]
    fn a_bad_first_entry_invalidates_the_list_and_a_later_one_is_dropped() {
        let first_bad = Config {
            public_origins: vec!["cloud.example.com".into(), "https://ok.example".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&first_bad),
            PublicOrigins::Invalid("cloud.example.com".into())
        );

        let later_bad = Config {
            public_origins: vec!["https://ok.example".into(), "nas.internal".into()],
            ..Config::default()
        };
        let (resolved, rejected) = later_bad.resolve_public_origins();
        assert_eq!(
            resolved,
            PublicOrigins::Configured(vec!["https://ok.example".into()])
        );
        assert_eq!(rejected, vec!["nas.internal".to_string()]);
    }

    #[test]
    fn an_empty_explicit_value_falls_back_to_derivation() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            public_origins: vec!["   ".into()],
            ..Config::default()
        };
        assert_eq!(
            origins_of(&cfg),
            PublicOrigins::Derived(vec!["https://cloud.example.com".into()])
        );
    }

    /// An old file is refused with the replacement line, never parsed with
    /// the key ignored.
    #[test]
    fn a_config_file_carrying_a_removed_key_refuses_to_parse() {
        for (doc, expected) in [
            (
                "compat_canonical_url = \"https://cloud.example.com\"",
                "public_origins",
            ),
            (
                "compat_alt_canonical_urls = [\"https://nas.internal\"]",
                "public_origins",
            ),
            (
                "[oidc]\nredirect_uri = \"https://cloud.example.com/api/auth/oidc/callback\"",
                "redirect_uris",
            ),
        ] {
            let err = Config::from_toml_str(doc).unwrap_err().to_string();
            assert!(err.contains(expected), "{err}");
        }
    }

    /// `index.content_enabled` is *not* in that refusal, and this is the
    /// difference: it never had behaviour to change, so refusing to start
    /// over it would be an outage bought with no information.
    #[test]
    fn a_config_file_still_setting_the_dead_index_key_starts_normally() {
        let cfg = Config::from_toml_str("[index]\nname_enabled = true\ncontent_enabled = true")
            .expect("a dead key is ignored, not refused");
        assert!(cfg.index.name_enabled);
    }

    #[test]
    fn public_origins_are_toml_reachable() {
        let cfg = Config::from_toml_str(
            r#"
            app_hosts = ["one.example", "two.example"]
            public_origins = ["https://one.example", "https://two.example"]
            "#,
        )
        .unwrap();
        assert_eq!(
            origins_of(&cfg),
            PublicOrigins::Configured(vec![
                "https://one.example".into(),
                "https://two.example".into(),
            ])
        );
    }

    /// A redirect URI is checked against the operator's own `app_hosts`, not
    /// the wider set the guard admits: the callback comes back to us and a
    /// host we do not answer for is an unrecoverable login.
    #[test]
    fn a_redirect_uri_must_be_https_and_name_a_served_host() {
        let hosts = vec!["cloud.example.com".to_string()];
        assert_eq!(
            redirect_uri_problem("https://cloud.example.com/api/auth/oidc/callback", &hosts),
            None
        );
        assert!(
            redirect_uri_problem("http://cloud.example.com/cb", &hosts)
                .unwrap()
                .contains("https://")
        );
        assert!(
            redirect_uri_problem("https://elsewhere.example/cb", &hosts)
                .unwrap()
                .contains("app_hosts")
        );
        // A declared port does not change which host was declared.
        assert_eq!(
            redirect_uri_problem("https://cloud.example.com:8443/cb", &hosts),
            None
        );
    }

    /// §4.3.5's M5 resolution, as a property rather than a promise: the two
    /// `[oidc]` keys that can lock an operator out of their own deployment
    /// are not reachable from the settings screen, so a stored override
    /// cannot beat `config.toml` for either of them on the next boot.
    ///
    /// `OidcOverride` has no field for them, so this cannot regress without
    /// somebody adding one; that is what the test is guarding, and it is
    /// worth stating because the failure mode is a deployment nobody can log
    /// into, discovered by an administrator who is already locked out.
    #[test]
    fn an_oidc_override_cannot_reach_the_two_config_file_only_keys() {
        let secret = PathBuf::from("/etc/sc/oidc_client_secret");
        let mut cfg = Config {
            oidc: OidcConfig {
                client_secret_file: Some(secret.clone()),
                local_password_login: OidcLocalPasswordLoginCfg::Deny,
                ..OidcConfig::default()
            },
            ..Config::default()
        };
        let overrides = SettingsOverrides {
            oidc: Some(OidcOverride {
                enabled: true,
                issuer: "https://idp.example.com".into(),
                client_id: "sc".into(),
                redirect_uris: vec!["https://cloud.example.com/api/auth/oidc/callback".into()],
                scopes: vec!["openid".into()],
                display_name: "회사 계정".into(),
                allow_private_endpoints: true,
                smb_policy: OidcSmbPolicy::Block,
            }),
            ..SettingsOverrides::default()
        };
        cfg.apply_settings_overrides(&overrides);

        // The seven the screen owns did land.
        assert!(cfg.oidc.enabled);
        assert_eq!(cfg.oidc.issuer, "https://idp.example.com");
        assert_eq!(cfg.oidc.client_id, "sc");
        assert_eq!(cfg.oidc.display_name, "회사 계정");
        assert!(cfg.oidc.allow_private_endpoints);

        // The two it must not are exactly as `config.toml` left them.
        assert_eq!(cfg.oidc.client_secret_file, Some(secret));
        assert_eq!(cfg.oidc.local_password_login, OidcLocalPasswordLoginCfg::Deny);
    }

    /// Reverting one group leaves the other nine alone, which is the whole
    /// reason `clear` takes a section rather than the screen deleting
    /// `settings.db`.
    #[test]
    fn clearing_one_section_leaves_the_others_standing() {
        use sc_http::settings_api::SettingsSection;
        let mut o = SettingsOverrides {
            archive_max_concurrent: Some(8),
            symlink_policy: Some(SymlinkPolicyCfg::Follow),
            ..SettingsOverrides::default()
        };
        assert!(o.is_set(SettingsSection::Archive));
        o.clear(SettingsSection::Archive);
        assert!(!o.is_set(SettingsSection::Archive));
        assert!(o.is_set(SettingsSection::SymlinkPolicy));

        // And the file's value is what a cleared section resolves to again.
        let mut cfg = Config {
            archive: ArchiveConfig { max_concurrent: 2 },
            ..Config::default()
        };
        cfg.apply_settings_overrides(&o);
        assert_eq!(cfg.archive.max_concurrent, 2);
    }
}
