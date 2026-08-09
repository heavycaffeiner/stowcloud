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

/// /: both off by default.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct IndexConfig {
    pub name_enabled: bool,
    pub content_enabled: bool,
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
    /// The exact redirect URI registered at the IdP, e.g.
    /// `https://cloud.example.com/api/auth/oidc/callback`.
    ///
    /// Configured, never derived (§4.3.1). The existing derivation logic
    /// (`resolve_compat_canonical_url`) accepts `http://`, does not fully
    /// parse a URL, and is ambiguous when `app_hosts` has zero or several
    /// entries; a redirect URI has to match what is registered byte for byte,
    /// and the authorization request and the token request have to carry the
    /// same value, so this is not a place for inference.
    pub redirect_uri: String,
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
            redirect_uri: String::new(),
            scopes: vec!["openid".to_string()],
            display_name: String::new(),
            allow_private_endpoints: false,
            smb_policy: OidcSmbPolicy::default(),
            local_password_login: OidcLocalPasswordLoginCfg::default(),
        }
    }
}

impl OidcConfig {
    /// Why OIDC will not activate, or `None` when it will.
    ///
    /// §4.3.1: an empty or non-https `redirect_uri` means OIDC does not come
    /// up, **and everything else on the server keeps working**. The reason is
    /// returned rather than logged here so that the one caller
    /// (`crate::oidc::build_oidc`) can log it once, with the same wording an
    /// operator will search for.
    pub fn inactive_reason(&self) -> Option<String> {
        if !self.enabled {
            return Some("oidc.enabled is false".into());
        }
        if self.issuer.trim().is_empty() {
            return Some("oidc.issuer is empty".into());
        }
        if self.client_id.trim().is_empty() {
            return Some("oidc.client_id is empty".into());
        }
        let redirect = self.redirect_uri.trim();
        if redirect.is_empty() {
            return Some("oidc.redirect_uri is empty".into());
        }
        if !redirect.starts_with("https://") {
            return Some(format!(
                "oidc.redirect_uri must start with https:// (got {redirect})"
            ));
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
    pub bind: SocketAddr,
    /// Where the LAN HTTPS listener binds, with a self-signed certificate the
    /// server writes for itself (`tls.rs`). `None` disables it.
    ///
    /// Separate from `bind` rather than replacing it, because the two serve
    /// different callers: a reverse proxy terminating the public name talks
    /// plaintext to `bind` over loopback or the container bridge, while a
    /// browser on the LAN needs `https://` or it cannot keep the `__Host-`
    /// session cookie at all. Folding them into one listener would force the
    /// proxy onto an upstream certificate it has no reason to verify.
    #[serde(default)]
    pub tls_bind: Option<SocketAddr>,
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
    /// The externally-reachable `https://host[:port]` origin the (feature-
    /// gated) compat layer builds every client-facing URL
    /// from: the Login Flow v2 `login`/`poll.endpoint` URLs a real device's
    /// system browser opens and then binds to **permanently**, every public
    /// share link, and the `theming.url` capability. `sc_compat_nc::config::
    /// NcConfig`'s own doc comment explains why this may never be derived
    /// from a request's `Host` header (host-header trust would let anyone who
    /// can reach the server hand a client a poll endpoint on a host they
    /// control); the question this field answers is where it comes from
    /// *instead*.
    ///
    /// Before this field existed the answer was "whichever `app_hosts` entry
    /// happens to be listed first" (`app.rs`) — silent and order-dependent:
    /// reordering `app_hosts` for an unrelated reason (adding a bind alias,
    /// alphabetising) silently repoints every future client enrolment at a
    /// different origin, with nothing to say so. A client that binds to the
    /// wrong server this way is not a small bug an error message surfaces
    /// immediately; it is a permanent misconfiguration discovered only when a
    /// real handset fails, far away from whoever changed the config.
    ///
    /// Set this explicitly in production. Left unset:
    /// - exactly one `app_hosts` entry: still auto-derived as
    ///   `https://{that entry}` (unambiguous — there is only one thing it
    ///   could mean), logged loudly at startup so the derivation is visible
    ///   rather than assumed;
    /// - zero or more-than-one `app_hosts` entries: **ambiguous, and the
    ///   compatibility layer does not mount at all** (`app.rs::
    ///   resolve_compat_canonical_url`) rather than guess which host a real
    ///   client should be told to bind to. Every other surface (the native
    ///   web UI, WebDAV, the plain API) is completely unaffected — this only
    ///   withholds the compat-client surface until the
    ///   ambiguity is resolved.
    #[serde(default)]
    pub compat_canonical_url: Option<String>,
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
            bind: SocketAddr::new(IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)), 8080),
            tls_bind: None,
            app_hosts: sc_http::config::HttpConfig::default().app_hosts,
            content_hosts: Vec::new(),
            allowed_origins: Vec::new(),
            compat_canonical_url: None,
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
    pub fn from_toml_str(s: &str) -> anyhow::Result<Config> {
        let mut cfg: Config = toml::from_str(s)?;
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
    /// then apply environment overrides. This is `TOML + env` per the
    /// deployment contract, in that order: env always wins.
    ///
    /// The default location is not decoration. `--config`'s help text
    /// promised it from the start and nothing implemented it, so a
    /// deployment that wrote `<data_dir>/sc.toml` and restarted got a server
    /// with no shares, no `compat_canonical_url`, and no indication that the
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
        // Empty explicitly means "off", so a compose file can disable the LAN
        // listener without having to drop the variable entirely.
        if let Ok(v) = std::env::var("SC_TLS_BIND") {
            self.tls_bind = if v.trim().is_empty() { None } else { v.parse::<SocketAddr>().ok() };
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

    /// Resolve [`Config::compat_canonical_url`] against [`Config::app_hosts`].
    ///
    /// Shared by `app.rs` (decides whether the compat layer
    /// mounts at all) and `diagnostics.rs` (reports the outcome at startup,
    /// the same way `single_origin`/`trusted_proxies` already do) — one
    /// function, so the two can never disagree about what "configured" means.
    pub fn resolve_compat_canonical_url(&self) -> CompatCanonicalUrl {
        if let Some(v) = &self.compat_canonical_url {
            let trimmed = v.trim();
            if trimmed.is_empty() {
                return CompatCanonicalUrl::Ambiguous {
                    app_host_count: self.app_hosts.len(),
                };
            }
            // Not a full URL parse (no `url` crate dependency here) — just
            // enough to catch the realistic operator mistakes: a bare host
            // with no scheme, or a scheme this layer cannot hand a real
            // client (Login Flow v2's `login` URL is opened in a system
            // browser, which understands http(s) and nothing else).
            let has_scheme = trimmed.starts_with("https://") || trimmed.starts_with("http://");
            let host_part = trimmed.split("://").nth(1).unwrap_or("");
            if !has_scheme || host_part.is_empty() {
                return CompatCanonicalUrl::Invalid(trimmed.to_string());
            }
            return CompatCanonicalUrl::Configured(trimmed.trim_end_matches('/').to_string());
        }
        match self.app_hosts.as_slice() {
            [single] => CompatCanonicalUrl::Derived(format!("https://{single}")),
            other => CompatCanonicalUrl::Ambiguous {
                app_host_count: other.len(),
            },
        }
    }
}

// --------------------------------------------------------- admin overrides
// The server-settings admin screen's persisted state (`settings_store.rs`,
// `settings_bridge.rs`): changes made from the UI, kept outside
// `config.toml` because that file is the operator's and is overwritten on
// every `scripts/deploy.sh` push (same reasoning as `sc-upload`'s
// `upload_chunk_settings` and `sc-search`'s `IndexSettingsStore`). Each
// section is `Option` and full-replace (not per-field partial), matching
// `admin_set_upload_settings`'s existing precedent: the UI always sends the
// whole group it's editing.

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct NetworkOverride {
    pub bind: SocketAddr,
    /// `#[serde(default)]` so a `settings.db` written before this field existed
    /// still deserializes; without it every stored network override would be
    /// unreadable and the whole screen's saved state would silently revert.
    #[serde(default)]
    pub tls_bind: Option<SocketAddr>,
    pub app_hosts: Vec<String>,
    pub content_hosts: Vec<String>,
    pub allowed_origins: Vec<String>,
    pub trusted_proxies: Vec<String>,
    pub compat_canonical_url: Option<String>,
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
    pub redirect_uri: String,
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

impl Config {
    /// Fold a persisted admin override on top of the file+env config. Called
    /// once from `bootstrap()`, before any `App`/orchestrator is built, so
    /// `cmd_serve`/`cmd_gc`/`cmd_smb_sync` all see the same effective values
    /// with no special-casing per entry point.
    pub fn apply_settings_overrides(&mut self, o: &SettingsOverrides) {
        if let Some(n) = &o.network {
            self.bind = n.bind;
            self.tls_bind = n.tls_bind;
            self.app_hosts = n.app_hosts.clone();
            self.content_hosts = n.content_hosts.clone();
            self.allowed_origins = n.allowed_origins.clone();
            self.trusted_proxies = n.trusted_proxies.clone();
            self.compat_canonical_url = n.compat_canonical_url.clone();
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
            self.oidc.redirect_uri = oi.redirect_uri.clone();
            self.oidc.scopes = oi.scopes.clone();
            self.oidc.display_name = oi.display_name.clone();
            self.oidc.allow_private_endpoints = oi.allow_private_endpoints;
            self.oidc.smb_policy = oi.smb_policy;
        }
        self.upload.normalize();
    }
}

/// See [`Config::resolve_compat_canonical_url`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CompatCanonicalUrl {
    /// `compat_canonical_url` was set, and is a well-formed absolute
    /// `http(s)://host[:port]` origin (trailing slash stripped).
    Configured(String),
    /// Not configured, but `app_hosts` had exactly one entry — unambiguous,
    /// so it is safe to derive `https://{that entry}` automatically. Still
    /// reported loudly at startup (`diagnostics.rs`): implicit is not the
    /// same as wrong, but an operator relying on this for a production
    /// Login Flow v2 deployment should be told the derivation is happening.
    Derived(String),
    /// Not configured, and `app_hosts` had zero or more than one entry: no
    /// single value can be chosen without guessing which host a real client
    /// should permanently bind to. The compat layer does not
    /// mount at all in this state (`app.rs`) — every other surface (the
    /// native web UI, WebDAV, the plain API) is unaffected.
    Ambiguous { app_host_count: usize },
    /// `compat_canonical_url` was set, but is not an absolute `http(s)://`
    /// origin (missing scheme, or empty after it). Treated the same as
    /// `Ambiguous` for mounting purposes — a value this layer cannot even
    /// parse is worse than no value at all — but reported distinctly so the
    /// startup log points at the actual mistake instead of "app_hosts is
    /// ambiguous", which would be a red herring here.
    Invalid(String),
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_match_design_docs() {
        let cfg = Config::default();
        assert_eq!(cfg.upload.chunk_min_bytes, 5 * 1024 * 1024);
        assert_eq!(cfg.upload.chunk_default_bytes, 10 * 1024 * 1024);
        assert!(!cfg.db.size_guard);
        assert!(!cfg.index.name_enabled);
        assert!(!cfg.index.content_enabled);
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

    // --- `resolve_compat_canonical_url` ------------------------------------

    #[test]
    fn explicit_canonical_url_wins_over_app_hosts() {
        let cfg = Config {
            app_hosts: vec!["a.example".into(), "b.example".into()],
            compat_canonical_url: Some("https://cloud.example.com/".into()),
            ..Config::default()
        };
        assert_eq!(
            cfg.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Configured("https://cloud.example.com".into()),
            "trailing slash must be stripped, same as NcConfig::url() expects"
        );
    }

    #[test]
    fn single_app_host_is_derived_unambiguously() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            compat_canonical_url: None,
            ..Config::default()
        };
        assert_eq!(
            cfg.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Derived("https://cloud.example.com".into())
        );
    }

    #[test]
    fn zero_or_many_app_hosts_without_an_explicit_value_is_ambiguous() {
        let empty = Config {
            app_hosts: vec![],
            compat_canonical_url: None,
            ..Config::default()
        };
        assert_eq!(
            empty.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Ambiguous { app_host_count: 0 }
        );

        let many = Config {
            app_hosts: vec!["a.example".into(), "b.example".into(), "c.example".into()],
            compat_canonical_url: None,
            ..Config::default()
        };
        assert_eq!(
            many.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Ambiguous { app_host_count: 3 },
            "this is exactly the .dev/sc.toml shape before it set compat_canonical_url \
             explicitly: 'first entry wins' silently repoints Login Flow v2 if the list \
             is ever reordered, so it must not be chosen automatically"
        );
    }

    #[test]
    fn a_value_with_no_scheme_is_invalid_not_silently_ambiguous() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            compat_canonical_url: Some("cloud.example.com".into()),
            ..Config::default()
        };
        assert_eq!(
            cfg.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Invalid("cloud.example.com".into()),
            "a bare host is the realistic operator typo (forgetting the scheme); it must \
             be reported as its own mistake, not folded into the generic 'ambiguous' case"
        );
    }

    #[test]
    fn an_empty_explicit_value_falls_back_to_ambiguous_handling() {
        let cfg = Config {
            app_hosts: vec!["cloud.example.com".into()],
            compat_canonical_url: Some("   ".into()),
            ..Config::default()
        };
        assert_eq!(
            cfg.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Ambiguous { app_host_count: 1 }
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
                redirect_uri: "https://cloud.example.com/api/auth/oidc/callback".into(),
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

    #[test]
    fn resolve_compat_canonical_url_is_toml_reachable() {
        let cfg = Config::from_toml_str(
            r#"
            app_hosts = ["one.example", "two.example"]
            compat_canonical_url = "https://one.example"
            "#,
        )
        .unwrap();
        assert_eq!(
            cfg.resolve_compat_canonical_url(),
            CompatCanonicalUrl::Configured("https://one.example".into())
        );
    }
}
