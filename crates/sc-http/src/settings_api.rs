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
    /// deployed; persisted outside `config.toml` (`DESIGN-*.md`'s
    /// `upload_chunk_settings`/`ShareStore` precedent — never rewrites the
    /// operator's file, which `scripts/deploy.sh` overwrites on every push).
    AdminOverride,
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
    pub restart_required: bool,
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
    pub compat_canonical_url: Option<String>,
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

/// The editable half of `[oidc]`
/// (`docs/proposals/stowcloud-0-oidc-login.md` §6-4).
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
    pub redirect_uri: String,
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
    /// reconfigure`) — `DESIGN-SEARCH.md` §8's own limits, the task's worked
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
