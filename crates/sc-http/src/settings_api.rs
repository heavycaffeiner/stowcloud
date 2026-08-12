//! Trait boundary for the server-settings admin screen: every operator-
//! settable config field, in one place, with live-apply where possible and
//! restart-required where not.
//!
//! This crate does not depend on `sc-server::config` (same reasoning as
//! `upload_api`'s module doc: the trait is the narrow waist between the wire
//! and the real config), so the snapshot this trait returns is already
//! flattened into wire-shaped rows rather than a `Config` struct crossing
//! the seam. `sc-server`'s `SettingsBridge` builds that flattening from the
//! real `Config` plus whatever the running process has live-reconfigured
//! since boot.

use crate::core_api::CoreError;

/// Where a field's current effective value came from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SettingsSource {
    /// Built-in default; neither `config.toml` nor an admin override set it.
    BuiltinDefault,
    /// Set by the operator's `config.toml` (or `SC_*` env).
    ConfigFile,
    /// Changed from this admin screen since the config file was last
    /// deployed; persisted outside `config.toml` (the same precedent as
    /// `upload_chunk_settings` and `ShareStore` — never rewrites the
    /// operator's file, which `scripts/deploy.sh` overwrites on every push).
    AdminOverride,
}

/// One settings group, as the wire names it.
///
/// The vocabulary of `DELETE /api/admin/server-settings/{section}` and of
/// `SettingsOverrides`'s own `clear`/`is_set`. Spelled here rather than in
/// `sc-server` because it is a wire name before it is a config concept, and
/// an unknown one has to be a 404 rather than a silent no-op.
#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum SettingsSection {
    Network,
    Db,
    SymlinkPolicy,
    Homes,
    Smb,
    Search,
    Archive,
    Watch,
    Paths,
    Oidc,
}

impl SettingsSection {
    pub const ALL: &'static [SettingsSection] = &[
        Self::Network,
        Self::Db,
        Self::SymlinkPolicy,
        Self::Homes,
        Self::Smb,
        Self::Search,
        Self::Archive,
        Self::Watch,
        Self::Paths,
        Self::Oidc,
    ];

    pub fn from_wire(s: &str) -> Option<Self> {
        Some(match s {
            "network" => Self::Network,
            "db" => Self::Db,
            "symlink-policy" => Self::SymlinkPolicy,
            "homes" => Self::Homes,
            "smb" => Self::Smb,
            "search" => Self::Search,
            "archive" => Self::Archive,
            "watch" => Self::Watch,
            "paths" => Self::Paths,
            "oidc" => Self::Oidc,
            _ => return None,
        })
    }

    pub fn as_wire(self) -> &'static str {
        match self {
            Self::Network => "network",
            Self::Db => "db",
            Self::SymlinkPolicy => "symlink-policy",
            Self::Homes => "homes",
            Self::Smb => "smb",
            Self::Search => "search",
            Self::Archive => "archive",
            Self::Watch => "watch",
            Self::Paths => "paths",
            Self::Oidc => "oidc",
        }
    }
}

/// One row on the settings screen.
#[derive(Clone, Debug, serde::Serialize)]
pub struct SettingsField {
    /// Dotted config path, e.g. `"search.rate_per_minute"` — matches
    /// `config.toml`'s own `[section] key` shape so an operator can cross-
    /// reference the two.
    pub key: String,
    pub value: serde_json::Value,
    pub source: SettingsSource,
    /// Changing this field only takes effect after a restart.
    ///
    /// A static property of the field, not a statement about this value. What
    /// the running process is actually on is [`Self::running_value`].
    pub restart_required: bool,
    /// The value the running process is actually using, when it differs from
    /// `value` because the change needs a restart that has not happened.
    /// `None` when the two agree, which is the ordinary case.
    ///
    /// Per-field rather than one global "something is pending" bit, because
    /// an administrator needs to know *which* change is waiting.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub running_value: Option<serde_json::Value>,
    /// `None` if this screen can change it; `Some(key)` — a catalogue key the
    /// browser renders in the reader's language — if not. A field the admin
    /// cannot safely change here is shown read-only with the reason, never
    /// hidden.
    pub readonly_reason_key: Option<String>,
}

#[derive(Clone, Debug, serde::Serialize)]
pub struct SettingsSnapshot {
    pub fields: Vec<SettingsField>,
    /// `sc_smb::SmbOrchestrator::public_bind_warning_active()` — surfaced
    /// here so the settings screen can show the same permanent banner
    /// `smb-sync`'s own log line describes.
    pub smb_public_bind_warning: bool,
    /// Grants the last render handed Samba more permissively than the
    /// registry defines them, because `smb.conf` cannot express the
    /// difference. Empty in the ordinary case.
    #[serde(default)]
    pub smb_overgrants: Vec<SmbOvergrantWire>,
    /// Names of shares sitting on tmpfs, detected once at startup. A share
    /// whose contents vanish on reboot is a configuration mistake far more
    /// often than a deliberate choice, so it is said out loud rather than
    /// refused. Names only: the screen writes the sentence.
    #[serde(default)]
    pub tmpfs_shares: Vec<String>,
    /// What the SMB agent said about the last render it was handed. `None`
    /// when SMB is off or no agent has ever answered.
    ///
    /// Rendering the files is only half of publishing; the other half runs
    /// beside smbd, where this process can see neither the filesystem nor the
    /// network. Everything here is something only that side knows.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub smb_agent: Option<SmbAgentWire>,
}

/// [`SettingsSnapshot::smb_agent`]. `key` is a catalogue key for the headline
/// the screen writes; `detail` is the agent's own diagnostic text, which is
/// not translatable for the same reason `testparm`'s output is not — it comes
/// from another program.
#[derive(Clone, Debug, Default, serde::Serialize)]
pub struct SmbAgentWire {
    /// `smb.agent_applied`, `smb.agent_problem`, `smb.agent_unreachable` or
    /// `smb.agent_absent`.
    pub key: String,
    pub ok: bool,
    /// The `[section]` names smbd is serving right now.
    pub shares: Vec<String>,
    /// The scope as it ended up after detection, which `sc-core` renders
    /// closed and cannot otherwise see.
    pub interfaces: String,
    pub hosts_allow: String,
    /// `unchanged`, `reloaded`, `restarted`, `started`, `stopped`, `failed`.
    pub smbd: String,
    /// Share paths that do not exist where smbd runs. A client asking for one
    /// of these is told the network name is invalid.
    pub missing_paths: Vec<String>,
    /// Accounts that cannot authenticate because the passdb import produced
    /// no entry for them.
    pub missing_passdb: Vec<String>,
    pub detail: Option<String>,
}

/// One entry of [`SettingsSnapshot::smb_overgrants`]. `key` is a catalogue
/// key and `detail` its placeholders — the server never sends the sentence
/// (`scripts/verify.sh`'s "no Korean in crates/*/src" gate is the same rule
/// stated from the other side).
#[derive(Clone, Debug, serde::Serialize)]
pub struct SmbOvergrantWire {
    pub share: String,
    pub user: String,
    pub key: String,
    pub detail: Vec<String>,
}

/// What applying a patch tells the caller, so the UI knows whether to run
/// the notify-and-restart flow or just confirm the change took effect.
#[derive(Clone, Copy, Debug, serde::Serialize)]
pub struct ApplyOutcome {
    pub applied_live: bool,
    pub restart_required: bool,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct SmbPatch {
    pub enabled: bool,
    pub workgroup: String,
    /// Required, like every other field here. It used to be optional because
    /// no screen edited it, and reading the current value for the absent
    /// field wrote it straight into the stored override: one save of any
    /// other SMB setting froze the NetBIOS name forever, out of reach of both
    /// the screen and `config.toml`. The screen has a control for it now.
    pub server_name: String,
    pub service_user: String,
    pub allow_public_bind: bool,
    /// `"require_separate"` | `"block"` — mirrors `sc_smb::TotpPolicy`
    /// without this crate depending on `sc-smb`.
    pub totp_policy: String,
    pub service_uid: u32,
    pub service_gid: u32,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct SearchPatch {
    pub max_concurrent_fast: u32,
    pub max_concurrent_slow: u32,
    pub walk_deadline_fast_ms: u64,
    pub walk_deadline_slow_ms: u64,
    pub rate_per_minute: u32,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct ArchivePatch {
    pub max_concurrent: u32,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct NetworkPatch {
    pub bind: String,
    pub app_hosts: Vec<String>,
    pub content_hosts: Vec<String>,
    pub allowed_origins: Vec<String>,
    pub trusted_proxies: Vec<String>,
    /// Absolute `http(s)://` origins, first canonical. Refused at save time
    /// if malformed, unlike the boot path which drops the entry with a
    /// warning: refusing at boot would take the server down over a typo, and
    /// refusing at save costs an administrator one correction while they are
    /// looking at the field.
    pub public_origins: Vec<String>,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct DbPatch {
    pub size_guard: bool,
    pub max_bytes: u64,
    pub min_free_bytes: u64,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct SymlinkPatch {
    /// `"deny"` | `"within_share"` | `"follow"`.
    pub policy: String,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct HomesPatch {
    pub enabled: bool,
    pub root: Option<String>,
}

#[derive(Clone, Debug, serde::Deserialize)]
pub struct WatchPatch {
    /// `"auto"` | `"hotset"` | `"inotify_full"` | `"fanotify"` — mirrors
    /// `sc-server::config::WatchBackend`'s snake_case serde names.
    pub backend: String,
    pub hot_set_max: u32,
    pub full_threshold: u32,
}

/// The editable half of `[oidc]`.
///
/// Three keys are missing from this patch and that is the point.
/// `client_secret_file` is a path to a secret and does not belong on a screen
/// anyone can reach with a session; `local_password_login` set to `deny` from
/// the screen would survive every `config.toml` edit, because a stored
/// override beats the file on every boot, so an operator who lost their IdP
/// could never undo it (§4.3.5). Both are `config.toml` only, and the
/// snapshot shows them read-only with that reason attached.
#[derive(Clone, Debug, serde::Deserialize)]
pub struct OidcPatch {
    pub enabled: bool,
    pub issuer: String,
    pub client_id: String,
    /// Every entry must be `https://` and name a host `app_hosts` admits: the
    /// callback comes back to us and nothing else can answer it.
    pub redirect_uris: Vec<String>,
    pub scopes: Vec<String>,
    pub display_name: String,
    pub allow_private_endpoints: bool,
    /// `"block"` -- the only value (§4.3.6: nothing in this product can create
    /// the dedicated SMB password that any other value would require).
    pub smb_policy: String,
}

/// The three bootstrap paths, as one group. Absolute paths only; the bridge
/// refuses anything the server would fail to start from — see
/// `SettingsBridge::set_paths`.
#[derive(Clone, Debug, serde::Deserialize)]
pub struct PathsPatch {
    pub data_dir: String,
    /// `None` means "`<data_dir>/master.key`", the same default
    /// `Config::master_key_path()` applies.
    pub master_key_file: Option<String>,
    pub smb_config_dir: String,
}

/// `POST /api/admin/server-settings/restart` body. `force` defaults to
/// `false` so an empty `{}` (or a client that forgets the field) never
/// bypasses the in-flight-work check in `routes::admin_restart_server` —
/// the check lives there, not in this trait, since it reads
/// `AppState::uploads`/`AppState::jobs`, which this crate-boundary trait
/// deliberately does not have access to.
#[derive(Clone, Copy, Debug, Default, serde::Deserialize)]
pub struct RestartPatch {
    #[serde(default)]
    pub force: bool,
}

/// Every method mirrors `UploadApi`'s pattern: a default body returning
/// [`not_wired`], overridden by the real implementation
/// (`sc-server::settings_bridge::SettingsBridge`) so `AppState` stays
/// constructible in tests without one.
pub trait SettingsApi: Send + Sync {
    fn snapshot(&self) -> SettingsSnapshot {
        SettingsSnapshot {
            fields: Vec::new(),
            smb_public_bind_warning: false,
            smb_overgrants: Vec::new(),
            tmpfs_shares: Vec::new(),
            smb_agent: None,
        }
    }

    /// Live-applies everything except `enabled` (in-process `smb.conf`
    /// regeneration via `smb_cmd::render_live`); `enabled` itself is staged
    /// as an admin override and requires a restart — `CoreBridge.smb_enabled`
    /// is a plain `bool` baked in once at `App::build`.
    fn set_smb(&self, _patch: SmbPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Fully live (`SearchConcurrency::reconfigure` + `KeyedTokenBucket::
    /// reconfigure`) — the search limits, the task's worked
    /// example of a setting that need not restart.
    fn set_search(&self, _patch: SearchPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Fully live (`ResizableSemaphore::resize`).
    fn set_archive(&self, _patch: ArchivePatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required: `bind`/`app_hosts`/`content_hosts`/`allowed_origins`
    /// are all baked into the listener and `HttpConfig` at `App::build` time.
    fn set_network(&self, _patch: NetworkPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required: `DbConfig` is read once by `Diagnostics` at boot.
    fn set_db(&self, _patch: DbPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required: baked into every `ShareRoot` at `App::build` time.
    fn set_symlink_policy(&self, _patch: SymlinkPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required: home directories are registered as shares once at
    /// boot (`app.rs`'s homes wiring).
    fn set_homes(&self, _patch: HomesPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required: `sc_watch::Watcher` is constructed once in
    /// `App::build` and its backend/limits are `Copy`ed into the running
    /// watcher there.
    fn set_watch(&self, _patch: WatchPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required, and the one group that can stop the server coming
    /// back up at all — the implementation validates far more than it
    /// deserializes (target must exist, must already hold the data, key file
    /// must be the *same* key). See `SettingsBridge::set_paths`.
    fn set_paths(&self, _patch: PathsPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// Restart-required, all of it: the relying party, its TLS client and its
    /// two caches are assembled once in `App::build` (`sc_server::oidc::
    /// build_oidc`), and the `redirect_uri` in particular has to match what
    /// is registered at the IdP, which is not a thing a live swap could
    /// verify.
    fn set_oidc(&self, _patch: OidcPatch) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// `DELETE /api/admin/server-settings/{section}` — drop this group's
    /// stored override so `config.toml` and the environment decide it again,
    /// and push the restored values into the running components exactly as
    /// the corresponding `set_*` would.
    ///
    /// Without this every group was one-way: after the first save, the file
    /// was dead for every key in it and the only recovery was deleting
    /// `settings.db`, which discards the other nine groups with it. Nothing
    /// else on this screen is safe to add until reverting one thing is
    /// possible.
    fn clear_section(&self, _section: SettingsSection) -> Result<ApplyOutcome, CoreError> {
        Err(not_wired())
    }

    /// `POST /api/admin/restart` — signals `cmd_serve`'s `tokio::select!` to
    /// run the graceful-shutdown sequence and exit with the restart-distinct
    /// code. Returns immediately; the actual shutdown/exit happens
    /// out-of-band so the HTTP response can still reach the client.
    fn request_restart(&self) -> Result<(), CoreError> {
        Err(not_wired())
    }
}

fn not_wired() -> CoreError {
    CoreError::Internal("settings backend not wired".into())
}

/// The default `AppState::settings`, used by tests and by any build that has
/// no settings store attached.
pub struct UnimplementedSettings;
impl SettingsApi for UnimplementedSettings {}
