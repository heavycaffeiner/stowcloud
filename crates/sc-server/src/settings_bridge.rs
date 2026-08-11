//! Binds `sc_http::settings_api::SettingsApi` to the real config/store/live
//! components — the server-settings admin screen's counterpart to
//! `bridge.rs`'s `CoreBridge`/`UploadBridge` (kept in its own file rather
//! than added to `bridge.rs` to avoid touching that file at all while it has
//! concurrent edits in flight elsewhere).
//!
//! Every patch method does the same two things, in this order (same
//! ordering `SettingsStore::mutate`/`IndexSettingsStore::set_name_enabled`
//! use): persist to `settings.db` first, then update whatever live
//! in-process state exists for that field. A disk-write failure this way
//! never lets a live value drift from what's on disk / would survive a
//! restart.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use sc_http::core_api::CoreError;
use sc_http::rate_limit::KeyedTokenBucket;
use sc_http::search_limits::{SearchConcurrency, SearchLimitsConfig};
use sc_http::settings_api::{
    ApplyOutcome, ArchivePatch, DbPatch, HomesPatch, NetworkPatch, PathsPatch, SearchPatch,
    SettingsApi, SettingsField, SettingsSection, SettingsSnapshot, SettingsSource, SmbPatch,
    SymlinkPatch, WatchPatch,
};
use sc_http::state::ResizableSemaphore;
use parking_lot::Mutex;
use serde_json::json;

use crate::config::{
    Config, DbOverride, HomesOverride, NetworkOverride, OidcOverride, PathsOverride, SearchOverride,
    SmbOverride, WatchBackend, WatchOverride,
};
use crate::settings_store::SettingsStore;

fn store_err(e: anyhow::Error) -> CoreError {
    CoreError::Internal(e.to_string())
}

/// Every refusal on this screen goes out as a catalogue key plus its
/// placeholders. The admin UI is the only consumer, and it renders in
/// whatever language the reader picked — so the wording lives in
/// `web/src/lib/i18n/*.json`, not here. `message` is the English log line.
fn reject(key: &str, params: serde_json::Value, message: impl Into<String>) -> CoreError {
    CoreError::Invalid { key: key.to_string(), params, message: message.into() }
}

fn totp_policy_from_wire(s: &str) -> Result<sc_smb::TotpPolicy, CoreError> {
    match s {
        "require_separate" => Ok(sc_smb::TotpPolicy::RequireSeparate),
        "block" => Ok(sc_smb::TotpPolicy::Block),
        other => Err(reject(
            "settings.unknown_totp_policy",
            json!({ "value": other }),
            format!("unknown totp_policy value: {other}"),
        )),
    }
}

fn totp_policy_to_wire(p: sc_smb::TotpPolicy) -> &'static str {
    match p {
        sc_smb::TotpPolicy::RequireSeparate => "require_separate",
        sc_smb::TotpPolicy::Block => "block",
    }
}

fn symlink_policy_from_wire(s: &str) -> Result<crate::config::SymlinkPolicyCfg, CoreError> {
    use crate::config::SymlinkPolicyCfg;
    match s {
        "deny" => Ok(SymlinkPolicyCfg::Deny),
        "within_share" => Ok(SymlinkPolicyCfg::WithinShare),
        "follow" => Ok(SymlinkPolicyCfg::Follow),
        other => Err(reject(
            "settings.unknown_symlink_policy",
            json!({ "value": other }),
            format!("unknown symlink_policy value: {other}"),
        )),
    }
}

fn symlink_policy_to_wire(p: crate::config::SymlinkPolicyCfg) -> &'static str {
    use crate::config::SymlinkPolicyCfg;
    match p {
        SymlinkPolicyCfg::Deny => "deny",
        SymlinkPolicyCfg::WithinShare => "within_share",
        SymlinkPolicyCfg::Follow => "follow",
    }
}

fn oidc_smb_policy_to_wire(p: crate::config::OidcSmbPolicy) -> &'static str {
    match p {
        crate::config::OidcSmbPolicy::Block => "block",
    }
}

/// One accepted value, and anything else is a rejection with the reason
/// rather than a silent fallback to `block`. §4.3.6: `require_separate`
/// exists for TOTP and cannot be honoured here, so an operator who copies it
/// across from `smb.totp_policy` has to be told, not quietly given something
/// else.
fn oidc_smb_policy_from_wire(s: &str) -> Result<crate::config::OidcSmbPolicy, CoreError> {
    match s {
        "block" => Ok(crate::config::OidcSmbPolicy::Block),
        other => Err(reject(
            "settings.unknown_oidc_smb_policy",
            json!({ "value": other }),
            format!("unknown oidc.smb_policy value: {other} (the only supported value is \"block\")"),
        )),
    }
}

fn oidc_local_password_login_to_wire(p: crate::config::OidcLocalPasswordLoginCfg) -> &'static str {
    use crate::config::OidcLocalPasswordLoginCfg as P;
    match p {
        P::Allow => "allow",
        P::Deny => "deny",
    }
}

fn watch_backend_from_wire(s: &str) -> Result<WatchBackend, CoreError> {
    match s {
        "auto" => Ok(WatchBackend::Auto),
        "hotset" => Ok(WatchBackend::Hotset),
        "inotify_full" => Ok(WatchBackend::InotifyFull),
        "fanotify" => Ok(WatchBackend::Fanotify),
        other => Err(reject(
            "settings.unknown_watch_backend",
            json!({ "value": other }),
            format!("unknown watch.backend value: {other}"),
        )),
    }
}

fn watch_backend_to_wire(b: WatchBackend) -> &'static str {
    match b {
        WatchBackend::Auto => "auto",
        WatchBackend::Hotset => "hotset",
        WatchBackend::InotifyFull => "inotify_full",
        WatchBackend::Fanotify => "fanotify",
    }
}

/// A real write, not a permission-bit guess: `Metadata::permissions()
/// .readonly()` says nothing useful about a directory on Unix and nothing at
/// all about a read-only mount, which is the failure this is here to catch —
/// a path the next start cannot write to is a server that does not come back.
fn probe_writable(dir: &Path) -> Result<(), std::io::Error> {
    let probe = dir.join(".sc-settings-write-probe");
    std::fs::write(&probe, b"")?;
    std::fs::remove_file(&probe)
}

fn require_dir(path: &Path, label: &str) -> Result<(), CoreError> {
    if !path.is_absolute() {
        return Err(reject(
            "settings.path_must_be_absolute",
            json!({ "field": label, "path": path.display().to_string() }),
            format!("{label}: must be an absolute path ({})", path.display()),
        ));
    }
    if !path.is_dir() {
        return Err(reject(
            "settings.dir_does_not_exist",
            json!({ "field": label, "path": path.display().to_string() }),
            format!("{label}: directory does not exist ({}); the server does not create it", path.display()),
        ));
    }
    probe_writable(path).map_err(|e| {
        reject(
            "settings.dir_not_writable",
            json!({ "field": label, "path": path.display().to_string(), "error": e.to_string() }),
            format!("{label}: directory is not writable ({}): {e}", path.display()),
        )
    })
}

/// [`SettingsApi::set_paths`]'s entire refusal logic, as a pure function of
/// the current paths and the requested ones. Split out for the same reason
/// `snapshot_fields` is: these are the checks standing between an admin and
/// a server that will not restart, and they deserve tests that do not need a
/// `Core`/`AuthService` standing up first.
///
/// Returns the resolved `(data_dir, master_key_file, smb_config_dir)`.
fn validate_paths(
    cur_data_dir: &Path,
    cur_key_path: &Path,
    patch: &PathsPatch,
) -> Result<(PathBuf, Option<PathBuf>, PathBuf), CoreError> {
    let data_dir = PathBuf::from(patch.data_dir.trim());
    let smb_config_dir = PathBuf::from(patch.smb_config_dir.trim());
    let master_key_file = patch
        .master_key_file
        .as_ref()
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .map(PathBuf::from);

    require_dir(&data_dir, "data_dir")?;
    require_dir(&smb_config_dir, "smb.config_dir")?;
    if let Some(p) = &master_key_file {
        if !p.is_absolute() {
            return Err(reject(
                "settings.path_must_be_absolute",
                json!({ "field": "master_key_file", "path": p.display().to_string() }),
                format!("master_key_file: must be an absolute path ({})", p.display()),
            ));
        }
    }

    // Proof of migration. Without it the restart lands in first-run setup
    // with every account gone, because `masterkey::load_or_generate` mints a
    // fresh key when the file is absent and `AuthService::new` then cannot
    // decrypt anything that was stored under the old one.
    if data_dir != cur_data_dir
        && cur_data_dir.join("auth.db").exists()
        && !data_dir.join("auth.db").exists()
    {
        return Err(reject(
            "settings.data_dir_missing_auth_db",
            json!({ "path": data_dir.display().to_string() }),
            format!(
                "data_dir: {} has no auth.db; move the data first — pointing at an empty path \
                 brings the server back up in first-run setup with every account gone",
                data_dir.display()
            ),
        ));
    }

    // Relocation only, never substitution: identical bytes or nothing.
    let new_key_path = master_key_file
        .clone()
        .unwrap_or_else(|| data_dir.join("master.key"));
    if new_key_path != cur_key_path {
        let cur = std::fs::read(cur_key_path).map_err(|e| {
            reject(
                "settings.master_key_current_unreadable",
                json!({ "path": cur_key_path.display().to_string(), "error": e.to_string() }),
                format!("master_key_file: cannot read the current key ({}): {e}", cur_key_path.display()),
            )
        })?;
        let new = std::fs::read(&new_key_path).map_err(|e| {
            reject(
                "settings.master_key_target_unreadable",
                json!({ "path": new_key_path.display().to_string(), "error": e.to_string() }),
                format!(
                    "master_key_file: cannot read {}: {e}. Copy the current key file there first.",
                    new_key_path.display()
                ),
            )
        })?;
        if cur != new {
            return Err(reject(
                "settings.master_key_contents_differ",
                json!({ "path": new_key_path.display().to_string() }),
                format!(
                    "master_key_file: the contents of {} differ from the current master key. This \
                     setting only moves the key file — a different key decrypts none of the \
                     stored secrets.",
                    new_key_path.display()
                ),
            ));
        }
    }

    Ok((data_dir, master_key_file, smb_config_dir))
}

/// The three rows a store other than this one owns at runtime.
///
/// `index.name_enabled` lives in `index.db` and the two chunk sizes in
/// `upload.db`, and neither store ever writes back into the `Config` this
/// bridge holds. Reading them from the bridge's own copy therefore reported
/// the boot value and hardcoded `config_file` as the source, on three rows
/// that a different admin screen can change at any moment.
#[derive(Clone, Copy, Debug)]
pub struct DelegatedValues {
    pub name_enabled: bool,
    /// A row exists in `index.db`, so this is an admin override rather than
    /// the config file's value having been passed through.
    pub name_enabled_overridden: bool,
    pub chunk_min_bytes: u64,
    pub chunk_default_bytes: u64,
    pub chunk_overridden: bool,
}

/// Fill in `running_value` wherever the saved value and the one this process
/// booted with disagree.
///
/// Restart-required rows only. A live-applied field's saved value *is* its
/// running value, so flagging it would announce a pending change that has
/// already happened — which is the same lie, from the other direction, as the
/// one this field exists to stop.
fn mark_pending(fields: &mut [SettingsField], booted: &[SettingsField]) {
    for f in fields.iter_mut() {
        if !f.restart_required {
            continue;
        }
        if let Some(b) = booted.iter().find(|b| b.key == f.key) {
            if b.value != f.value {
                f.running_value = Some(b.value.clone());
            }
        }
    }
}

pub struct SettingsBridge {
    store: Arc<SettingsStore>,
    /// `config.toml` plus the environment, **before** any stored override was
    /// applied. What a revert restores, and the reason `bootstrap()` keeps a
    /// copy: applying the overlay in place drops this on the floor, and a
    /// bridge built from the result cannot reconstruct it.
    file_cfg: Config,
    /// What this process actually started with: file + env + the overrides as
    /// of boot. Only used to answer whether a saved change has taken effect
    /// yet (`SettingsField::running_value`).
    boot_cfg: Config,
    /// Effective config, seeded from `App::build`'s already-overlaid `cfg`
    /// and kept current as patches land — this is what `snapshot()` reads
    /// and what `set_smb` hands to `smb_cmd::render_live`.
    cfg: Mutex<Config>,
    search_concurrency: Arc<SearchConcurrency>,
    search_rate: Arc<KeyedTokenBucket>,
    archive_concurrency: Arc<ResizableSemaphore>,
    /// The live owners of the three delegated rows above.
    index_settings: Arc<sc_search::IndexSettingsStore>,
    uploads: Arc<sc_upload::UploadEngine>,
    core: Arc<sc_core::Core>,
    auth: Arc<sc_auth::AuthService>,
    restart_signal: Arc<tokio::sync::Notify>,
    smb_public_bind_warning: AtomicBool,
    /// Latest render's overgrant list, for the settings screen. A
    /// `Mutex<Vec<_>>` rather than an atomic because it is a list, and it
    /// is only ever written by a render.
    smb_overgrants: Mutex<Vec<crate::smb_cmd::SmbOvergrant>>,
}

/// Everything [`SettingsBridge::new`] needs. A struct rather than a parameter
/// list because five of these are `Arc`s the caller holds side by side and
/// two of the configs have the same type, which is exactly the shape an
/// argument-order mistake hides in.
pub struct SettingsBridgeInputs {
    pub store: Arc<SettingsStore>,
    pub file_cfg: Config,
    pub cfg: Config,
    pub search_concurrency: Arc<SearchConcurrency>,
    pub search_rate: Arc<KeyedTokenBucket>,
    pub archive_concurrency: Arc<ResizableSemaphore>,
    pub index_settings: Arc<sc_search::IndexSettingsStore>,
    pub uploads: Arc<sc_upload::UploadEngine>,
    pub core: Arc<sc_core::Core>,
    pub auth: Arc<sc_auth::AuthService>,
    pub restart_signal: Arc<tokio::sync::Notify>,
}

impl SettingsBridge {
    pub fn new(inputs: SettingsBridgeInputs) -> Self {
        let SettingsBridgeInputs {
            store,
            file_cfg,
            cfg,
            search_concurrency,
            search_rate,
            archive_concurrency,
            index_settings,
            uploads,
            core,
            auth,
            restart_signal,
        } = inputs;
        Self {
            store,
            file_cfg,
            boot_cfg: cfg.clone(),
            cfg: Mutex::new(cfg),
            search_concurrency,
            search_rate,
            archive_concurrency,
            index_settings,
            uploads,
            core,
            auth,
            restart_signal,
            smb_public_bind_warning: AtomicBool::new(false),
            smb_overgrants: Mutex::new(Vec::new()),
        }
    }

    fn delegated(&self) -> DelegatedValues {
        let (min, default) = self.uploads.chunk_settings();
        DelegatedValues {
            name_enabled: self.index_settings.name_enabled(),
            name_enabled_overridden: self.index_settings.has_stored_override(),
            chunk_min_bytes: min,
            chunk_default_bytes: default,
            chunk_overridden: self.uploads.chunk_settings_overridden(),
        }
    }

    fn field(
        key: &str,
        value: serde_json::Value,
        source: SettingsSource,
        restart_required: bool,
    ) -> SettingsField {
        SettingsField {
            key: key.to_string(),
            value,
            source,
            restart_required,
            running_value: None,
            readonly_reason_key: None,
        }
    }

    /// `reason_key` is a catalogue key, not a sentence — the admin screen
    /// renders it in whatever language the reader picked.
    fn readonly(
        key: &str,
        value: serde_json::Value,
        source: SettingsSource,
        reason_key: &str,
    ) -> SettingsField {
        SettingsField {
            key: key.to_string(),
            value,
            source,
            restart_required: false,
            running_value: None,
            readonly_reason_key: Some(reason_key.to_string()),
        }
    }

    /// Read-only *and* restart-required, which is not the same thing as
    /// [`Self::readonly`]. That one is for a field some other screen owns and
    /// applies live; this is for a field only `config.toml` can change, where
    /// editing the file does nothing until the process restarts. Saying
    /// otherwise would leave an operator waiting for a change that has not
    /// happened.
    fn readonly_needing_restart(
        key: &str,
        value: serde_json::Value,
        source: SettingsSource,
        reason_key: &str,
    ) -> SettingsField {
        SettingsField {
            key: key.to_string(),
            value,
            source,
            restart_required: true,
            running_value: None,
            readonly_reason_key: Some(reason_key.to_string()),
        }
    }

    /// The whole field list, as a pure function of the effective config and
    /// the persisted overrides. Split out of [`SettingsApi::snapshot`] so
    /// `every_config_field_is_reachable` can assert the completeness
    /// requirement — every `Config` field either editable here or read-only
    /// with a stated reason — without standing up a `Core`/`AuthService`.
    fn snapshot_fields(
        cfg: &Config,
        o: &crate::config::SettingsOverrides,
        live: DelegatedValues,
    ) -> Vec<SettingsField> {
        let src = |present: bool, cfg_val_is_default: bool| {
            if present {
                SettingsSource::AdminOverride
            } else if cfg_val_is_default {
                SettingsSource::BuiltinDefault
            } else {
                SettingsSource::ConfigFile
            }
        };
        let default = Config::default();
        let mut fields = Vec::new();

        // --- network (restart-required) ---
        let net_src = src(
            o.network.is_some(),
            cfg.bind == default.bind && cfg.app_hosts == default.app_hosts,
        );
        fields.push(Self::field(
            "bind",
            json!(cfg.bind.to_string()),
            net_src,
            true,
        ));
        fields.push(Self::field(
            "app_hosts",
            json!(cfg.app_hosts),
            net_src,
            true,
        ));
        fields.push(Self::field(
            "content_hosts",
            json!(cfg.content_hosts),
            net_src,
            true,
        ));
        fields.push(Self::field(
            "allowed_origins",
            json!(cfg.allowed_origins),
            net_src,
            true,
        ));
        fields.push(Self::field(
            "trusted_proxies",
            json!(cfg.trusted_proxies),
            net_src,
            true,
        ));
        fields.push(Self::field(
            "public_origins",
            json!(cfg.public_origins),
            net_src,
            true,
        ));

        // --- db (restart-required) ---
        let db_src = src(
            o.db.is_some(),
            cfg.db.max_bytes == default.db.max_bytes && cfg.db.size_guard == default.db.size_guard,
        );
        fields.push(Self::field(
            "db.size_guard",
            json!(cfg.db.size_guard),
            db_src,
            true,
        ));
        fields.push(Self::field(
            "db.max_bytes",
            json!(cfg.db.max_bytes),
            db_src,
            true,
        ));
        fields.push(Self::field(
            "db.min_free_bytes",
            json!(cfg.db.min_free_bytes),
            db_src,
            true,
        ));

        // --- symlink policy (restart-required) ---
        let sym_src = src(
            o.symlink_policy.is_some(),
            cfg.symlink_policy == default.symlink_policy,
        );
        fields.push(Self::field(
            "symlink_policy",
            json!(symlink_policy_to_wire(cfg.symlink_policy)),
            sym_src,
            true,
        ));

        // --- homes (restart-required) ---
        let homes_src = src(
            o.homes.is_some(),
            cfg.homes.enabled == default.homes.enabled,
        );
        fields.push(Self::field(
            "homes.enabled",
            json!(cfg.homes.enabled),
            homes_src,
            true,
        ));
        fields.push(Self::field(
            "homes.root",
            json!(cfg.homes.root.as_ref().map(|p| p.display().to_string())),
            homes_src,
            true,
        ));

        // --- smb: `enabled` needs a restart (CoreBridge bakes it in at
        // `App::build`); everything else is live via `render_live`. ---
        let smb_src = src(
            o.smb.is_some(),
            cfg.smb.enabled == default.smb.enabled
                && cfg.smb.workgroup == default.smb.workgroup
                && cfg.smb.server_name == default.smb.server_name,
        );
        fields.push(Self::field(
            "smb.enabled",
            json!(cfg.smb.enabled),
            smb_src,
            true,
        ));
        fields.push(Self::field(
            "smb.workgroup",
            json!(cfg.smb.workgroup),
            smb_src,
            false,
        ));
        fields.push(Self::field(
            "smb.server_name",
            json!(cfg.smb.server_name),
            smb_src,
            false,
        ));
        // Config file only, on purpose. This is what holds smbd to one address
        // when the sidecar runs on the host's network, and an address that
        // stops resolving takes SMB down until someone edits a file anyway.
        fields.push(Self::readonly_needing_restart(
            "smb.interfaces",
            json!(cfg.smb.interfaces),
            SettingsSource::ConfigFile,
            "settings.readonly_smb_interfaces",
        ));
        fields.push(Self::field(
            "smb.service_user",
            json!(cfg.smb.service_user),
            smb_src,
            false,
        ));
        fields.push(Self::field(
            "smb.allow_public_bind",
            json!(cfg.smb.allow_public_bind),
            smb_src,
            false,
        ));
        fields.push(Self::field(
            "smb.totp_policy",
            json!(totp_policy_to_wire(cfg.smb.totp_policy)),
            smb_src,
            false,
        ));
        fields.push(Self::field(
            "smb.service_uid",
            json!(cfg.smb_service_uid),
            smb_src,
            false,
        ));
        fields.push(Self::field(
            "smb.service_gid",
            json!(cfg.smb_service_gid),
            smb_src,
            false,
        ));
        // --- bootstrap paths (restart-required). Grouped with `data_dir`/
        // `master_key_file` below rather than with the rest of SMB: the three
        // are validated together by `set_paths`, since `master_key_file`
        // defaults to a path *inside* `data_dir`. ---
        let paths_src = src(
            o.paths.is_some(),
            cfg.data_dir == default.data_dir && cfg.smb.config_dir == default.smb.config_dir,
        );
        fields.push(Self::field(
            "smb.config_dir",
            json!(cfg.smb.config_dir.display().to_string()),
            paths_src,
            true,
        ));

        // --- search (fully live) ---
        let search_src = src(
            o.search.is_some(),
            cfg.search.max_concurrent_fast == default.search.max_concurrent_fast,
        );
        fields.push(Self::field(
            "search.max_concurrent_fast",
            json!(cfg.search.max_concurrent_fast),
            search_src,
            false,
        ));
        fields.push(Self::field(
            "search.max_concurrent_slow",
            json!(cfg.search.max_concurrent_slow),
            search_src,
            false,
        ));
        fields.push(Self::field(
            "search.walk_deadline_fast_ms",
            json!(cfg.search.walk_deadline_fast_ms),
            search_src,
            false,
        ));
        fields.push(Self::field(
            "search.walk_deadline_slow_ms",
            json!(cfg.search.walk_deadline_slow_ms),
            search_src,
            false,
        ));
        fields.push(Self::field(
            "search.rate_per_minute",
            json!(cfg.search.rate_per_minute),
            search_src,
            false,
        ));

        // --- archive (fully live) ---
        let archive_src = src(
            o.archive_max_concurrent.is_some(),
            cfg.archive.max_concurrent == default.archive.max_concurrent,
        );
        fields.push(Self::field(
            "archive.max_concurrent",
            json!(cfg.archive.max_concurrent),
            archive_src,
            false,
        ));

        // --- the other two bootstrap paths, same group as `smb.config_dir`
        // above (restart-required). ---
        fields.push(Self::field(
            "data_dir",
            json!(cfg.data_dir.display().to_string()),
            paths_src,
            true,
        ));
        fields.push(Self::field(
            "master_key_file",
            json!(cfg
                .master_key_file
                .as_ref()
                .map(|p| p.display().to_string())),
            paths_src,
            true,
        ));

        // --- watch (restart-required) ---
        let watch_src = src(
            o.watch.is_some(),
            cfg.watch.backend == default.watch.backend
                && cfg.watch.hot_set_max == default.watch.hot_set_max
                && cfg.watch.full_threshold == default.watch.full_threshold,
        );
        // The wire name, not `{:?}` — `WatchBackend` is `rename_all =
        // "snake_case"`, so the `Debug` form the snapshot used to emit
        // ("Auto") is not a value `WatchPatch` would accept back.
        fields.push(Self::field(
            "watch.backend",
            json!(watch_backend_to_wire(cfg.watch.backend)),
            watch_src,
            true,
        ));
        fields.push(Self::field(
            "watch.hot_set_max",
            json!(cfg.watch.hot_set_max),
            watch_src,
            true,
        ));
        fields.push(Self::field(
            "watch.full_threshold",
            json!(cfg.watch.full_threshold),
            watch_src,
            true,
        ));

        // --- oidc (restart-required, all of it: the relying party, its TLS
        // client and its two caches are assembled once in `App::build`).
        // §6-4's classification table, row for row. ---
        let oidc_src = src(
            o.oidc.is_some(),
            cfg.oidc.enabled == default.oidc.enabled && cfg.oidc.issuer == default.oidc.issuer,
        );
        fields.push(Self::field("oidc.enabled", json!(cfg.oidc.enabled), oidc_src, true));
        fields.push(Self::field("oidc.issuer", json!(cfg.oidc.issuer), oidc_src, true));
        fields.push(Self::field("oidc.client_id", json!(cfg.oidc.client_id), oidc_src, true));
        fields.push(Self::field(
            "oidc.redirect_uris",
            json!(cfg.oidc.redirect_uris),
            oidc_src,
            true,
        ));
        fields.push(Self::field("oidc.scopes", json!(cfg.oidc.scopes), oidc_src, true));
        fields.push(Self::field(
            "oidc.display_name",
            json!(cfg.oidc.display_name),
            oidc_src,
            true,
        ));
        fields.push(Self::field(
            "oidc.allow_private_endpoints",
            json!(cfg.oidc.allow_private_endpoints),
            oidc_src,
            true,
        ));
        fields.push(Self::field(
            "oidc.smb_policy",
            json!(oidc_smb_policy_to_wire(cfg.oidc.smb_policy)),
            oidc_src,
            true,
        ));
        // The two the settings screen must never be able to write. Both need
        // a restart as well, so they say so -- `Self::readonly` hardcodes
        // `restart_required: false`, which is right for a field another
        // screen owns and wrong for one only `config.toml` can change.
        fields.push(Self::readonly_needing_restart(
            "oidc.client_secret_file",
            json!(cfg
                .oidc
                .client_secret_file
                .as_ref()
                .map(|p| p.display().to_string())),
            oidc_src,
            "settings.readonly_secret_file_path",
        ));
        fields.push(Self::readonly_needing_restart(
            "oidc.local_password_login",
            json!(oidc_local_password_login_to_wire(cfg.oidc.local_password_login)),
            oidc_src,
            "settings.readonly_local_password_login",
        ));

        // --- owned by another section of the admin UI — shown read-only with
        // why, never hidden, and with the value that section is *currently*
        // serving rather than the one this process booted with. ---
        let delegated_src = |overridden: bool, is_default: bool| {
            if overridden {
                SettingsSource::AdminOverride
            } else if is_default {
                SettingsSource::BuiltinDefault
            } else {
                SettingsSource::ConfigFile
            }
        };
        fields.push(Self::readonly(
            "index.name_enabled",
            json!(live.name_enabled),
            delegated_src(
                live.name_enabled_overridden,
                cfg.index.name_enabled == default.index.name_enabled,
            ),
            "settings.readonly_owned_by_index_section",
        ));
        let chunk_is_default = cfg.upload.chunk_min_bytes == default.upload.chunk_min_bytes
            && cfg.upload.chunk_default_bytes == default.upload.chunk_default_bytes;
        fields.push(Self::readonly(
            "upload.chunk_min_bytes",
            json!(live.chunk_min_bytes),
            delegated_src(live.chunk_overridden, chunk_is_default),
            "settings.readonly_owned_by_upload_section",
        ));
        fields.push(Self::readonly(
            "upload.chunk_default_bytes",
            json!(live.chunk_default_bytes),
            delegated_src(live.chunk_overridden, chunk_is_default),
            "settings.readonly_owned_by_upload_section",
        ));
        fields.push(Self::readonly(
            "smb_shares",
            json!(cfg.smb_shares.len()),
            SettingsSource::ConfigFile,
            "settings.readonly_static_smb_shares",
        ));
        fields.push(Self::readonly(
            "shares",
            json!(cfg.shares.len()),
            SettingsSource::ConfigFile,
            "settings.readonly_static_bootstrap_shares",
        ));

        fields
    }

    /// Rewrite Samba's files from the live registry and record what the
    /// settings screen has to show about the result. The caller holds the
    /// `cfg` lock, since what gets rendered has to be the config as of this
    /// moment and not a copy taken before some other patch landed.
    fn render_live_and_record_warning(&self, cfg: &Config) -> anyhow::Result<()> {
        let out = crate::smb_cmd::render_live(cfg, &self.core, &self.auth)?;
        self.record_render_outcome(out);
        Ok(())
    }

    fn record_render_outcome(&self, out: crate::smb_cmd::RenderOutcome) {
        self.smb_public_bind_warning
            .store(out.public_bind_warning, Ordering::Relaxed);
        *self.smb_overgrants.lock() = out.overgrants;
    }
}

/// The settings bridge is also what the passdb publisher renders through
/// (`passdb.rs`), because it is the only holder of the *live* config: an
/// admin can change `smb.workgroup` or the service user at runtime, and a
/// republish triggered by an NT-hash change has to use those values, not the
/// ones this process booted with.
///
/// `render_live` already handles `smb.enabled = false` by removing the
/// rendered files, so a deployment that turns SMB off mid-run keeps
/// converging rather than leaving a file behind.
impl crate::passdb::PassdbRender for SettingsBridge {
    fn render_passdb(&self) -> anyhow::Result<()> {
        let cfg = self.cfg.lock();
        self.render_live_and_record_warning(&cfg)
    }
}

impl SettingsApi for SettingsBridge {
    fn snapshot(&self) -> SettingsSnapshot {
        let overrides = self.store.load();
        let live = self.delegated();
        let mut fields = Self::snapshot_fields(&self.cfg.lock(), &overrides, live);
        let booted = Self::snapshot_fields(&self.boot_cfg, &overrides, live);
        mark_pending(&mut fields, &booted);
        SettingsSnapshot {
            fields,
            smb_public_bind_warning: self.smb_public_bind_warning.load(Ordering::Relaxed),
            smb_overgrants: self
                .smb_overgrants
                .lock()
                .iter()
                .map(|o| sc_http::settings_api::SmbOvergrantWire {
                    share: o.share.clone(),
                    user: o.user.clone(),
                    key: o.kind_key().to_string(),
                    detail: o.detail(),
                })
                .collect(),
            // Off the startup probe rather than re-`statfs`ing per request: a
            // share does not change filesystem while the process runs, and the
            // probe already ran. Empty when diagnostics never ran, which is
            // only true in tests.
            tmpfs_shares: crate::diagnostics::SNAPSHOT
                .get()
                .map(|d| d.tmpfs_shares())
                .unwrap_or_default(),
        }
    }

    fn set_smb(&self, patch: SmbPatch) -> Result<ApplyOutcome, CoreError> {
        let totp_policy = totp_policy_from_wire(&patch.totp_policy)?;
        let mut cfg = self.cfg.lock();
        let server_name = patch.server_name.clone();
        let ov = SmbOverride {
            enabled: patch.enabled,
            workgroup: patch.workgroup.clone(),
            server_name: server_name.clone(),
            service_user: patch.service_user.clone(),
            allow_public_bind: patch.allow_public_bind,
            totp_policy,
            service_uid: patch.service_uid,
            service_gid: patch.service_gid,
        };
        let enabled_changed = cfg.smb.enabled != patch.enabled;

        // Applied to a candidate and rendered from that, before anything is
        // persisted. Rendering is the step that can fail — a rejected server
        // name, or an unwritable `config_dir` — and the store used
        // to be written first, so a failed enable left `smb.enabled = true`
        // durably recorded with nothing rendered anywhere. That survives a
        // restart as a deployment that reports SMB on, serves nothing, and
        // answers the next identical patch `applied_live` because by then the
        // config already agrees with it. Seen on the testbed 2026-08-03,
        // behind the EACCES the compose file's `./smbcfg` note describes.
        let mut candidate = cfg.clone();
        candidate.smb.enabled = patch.enabled;
        candidate.smb.workgroup = patch.workgroup;
        candidate.smb.server_name = server_name;
        candidate.smb.service_user = patch.service_user;
        candidate.smb.allow_public_bind = patch.allow_public_bind;
        candidate.smb.totp_policy = totp_policy;
        candidate.smb_service_uid = patch.service_uid;
        candidate.smb_service_gid = patch.service_gid;

        // Rendered even when disabling: `render_live` removes the rendered
        // files in that case, which is what keeps NT hashes off disk for a
        // disabled feature and tells the sidecar/bare-metal agent to tear
        // down. Guarding this on `enabled` left SMB
        // serving from stale files until someone restarted the server.
        // A refusal is a validation rejection with a reason rather than a
        // server fault, so it maps to 422 via `InvalidName` and not 500.
        // Both enforcement sites for `totp_policy` read it off `AuthService`,
        // so the render below sees the new value only if it is pushed first.
        // Restored on a failed render, which leaves nothing applied anywhere.
        let previous_policy = self.auth.smb_totp_policy();
        self.auth.set_smb_totp_policy(match totp_policy {
            sc_smb::TotpPolicy::RequireSeparate => sc_auth::SmbTotpPolicy::RequireSeparate,
            sc_smb::TotpPolicy::Block => sc_auth::SmbTotpPolicy::Block,
        });
        let out = match crate::smb_cmd::render_live(&candidate, &self.core, &self.auth) {
            Ok(out) => out,
            Err(e) => {
                self.auth.set_smb_totp_policy(previous_policy);
                return Err(CoreError::InvalidName(e.to_string()));
            }
        };

        self.store
            .mutate(|o| o.smb = Some(ov.clone()))
            .map_err(store_err)?;
        self.record_render_outcome(out);
        *cfg = candidate;

        Ok(ApplyOutcome {
            applied_live: !enabled_changed,
            restart_required: enabled_changed,
        })
    }

    fn set_search(&self, patch: SearchPatch) -> Result<ApplyOutcome, CoreError> {
        // Zero was accepted and applied live, and from that moment every
        // search from every user answered 429. Refused where it is typed.
        if patch.rate_per_minute == 0 {
            return Err(reject(
                "settings.must_be_at_least_one",
                json!({ "field": "search.rate_per_minute" }),
                "search.rate_per_minute: must be at least 1",
            ));
        }
        let ov = SearchOverride {
            max_concurrent_fast: patch.max_concurrent_fast,
            max_concurrent_slow: patch.max_concurrent_slow,
            walk_deadline_fast_ms: patch.walk_deadline_fast_ms,
            walk_deadline_slow_ms: patch.walk_deadline_slow_ms,
            rate_per_minute: patch.rate_per_minute,
        };
        self.store
            .mutate(|o| o.search = Some(ov.clone()))
            .map_err(store_err)?;

        let limits = SearchLimitsConfig {
            max_concurrent_fast: patch.max_concurrent_fast,
            max_concurrent_slow: patch.max_concurrent_slow,
            walk_deadline_fast: Duration::from_millis(patch.walk_deadline_fast_ms),
            walk_deadline_slow: Duration::from_millis(patch.walk_deadline_slow_ms),
        };
        self.search_concurrency.reconfigure(&limits);
        self.search_rate
            .reconfigure(patch.rate_per_minute, Duration::from_secs(60));

        let mut cfg = self.cfg.lock();
        cfg.search.max_concurrent_fast = patch.max_concurrent_fast;
        cfg.search.max_concurrent_slow = patch.max_concurrent_slow;
        cfg.search.walk_deadline_fast_ms = patch.walk_deadline_fast_ms;
        cfg.search.walk_deadline_slow_ms = patch.walk_deadline_slow_ms;
        cfg.search.rate_per_minute = patch.rate_per_minute;

        Ok(ApplyOutcome {
            applied_live: true,
            restart_required: false,
        })
    }

    fn set_archive(&self, patch: ArchivePatch) -> Result<ApplyOutcome, CoreError> {
        // The semaphore already clamps zero to one; the stored and displayed
        // value did not, so the screen showed a number the server was not
        // using.
        if patch.max_concurrent == 0 {
            return Err(reject(
                "settings.must_be_at_least_one",
                json!({ "field": "archive.max_concurrent" }),
                "archive.max_concurrent: must be at least 1",
            ));
        }
        self.store
            .mutate(|o| o.archive_max_concurrent = Some(patch.max_concurrent))
            .map_err(store_err)?;
        self.archive_concurrency
            .resize(patch.max_concurrent.max(1) as usize);
        self.cfg.lock().archive.max_concurrent = patch.max_concurrent;
        Ok(ApplyOutcome {
            applied_live: true,
            restart_required: false,
        })
    }

    fn set_network(&self, patch: NetworkPatch) -> Result<ApplyOutcome, CoreError> {
        let bind = patch
            .bind
            .parse()
            .map_err(|_| {
                reject(
                    "settings.invalid_bind_address",
                    json!({ "value": patch.bind }),
                    format!("invalid bind address: {}", patch.bind),
                )
            })?;
        // Refused here, unlike the boot path which drops a malformed entry
        // with a warning. Taking the server down over a typo is worse than
        // costing an administrator one correction while the field is in front
        // of them, and the two directions are asymmetric for that reason.
        let public_origins: Vec<String> = patch
            .public_origins
            .iter()
            .map(|s| s.trim().trim_end_matches('/').to_string())
            .filter(|s| !s.is_empty())
            .collect();
        for origin in &public_origins {
            if !crate::config::is_absolute_origin(origin) {
                return Err(reject(
                    "settings.invalid_origin",
                    json!({ "value": origin }),
                    format!("public_origins: {origin} is not an absolute http(s):// origin"),
                ));
            }
        }
        let ov = NetworkOverride {
            bind,
            app_hosts: patch.app_hosts.clone(),
            content_hosts: patch.content_hosts.clone(),
            allowed_origins: patch.allowed_origins.clone(),
            trusted_proxies: patch.trusted_proxies.clone(),
            public_origins: public_origins.clone(),
        };
        self.store
            .mutate(|o| o.network = Some(ov.clone()))
            .map_err(store_err)?;

        let mut cfg = self.cfg.lock();
        cfg.bind = bind;
        cfg.app_hosts = patch.app_hosts;
        cfg.content_hosts = patch.content_hosts;
        cfg.allowed_origins = patch.allowed_origins;
        cfg.trusted_proxies = patch.trusted_proxies;
        cfg.public_origins = public_origins;

        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    fn set_db(&self, patch: DbPatch) -> Result<ApplyOutcome, CoreError> {
        let ov = DbOverride {
            size_guard: patch.size_guard,
            max_bytes: patch.max_bytes,
            min_free_bytes: patch.min_free_bytes,
        };
        self.store
            .mutate(|o| o.db = Some(ov.clone()))
            .map_err(store_err)?;
        let mut cfg = self.cfg.lock();
        cfg.db.size_guard = patch.size_guard;
        cfg.db.max_bytes = patch.max_bytes;
        cfg.db.min_free_bytes = patch.min_free_bytes;
        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    fn set_symlink_policy(&self, patch: SymlinkPatch) -> Result<ApplyOutcome, CoreError> {
        let policy = symlink_policy_from_wire(&patch.policy)?;
        self.store
            .mutate(|o| o.symlink_policy = Some(policy))
            .map_err(store_err)?;
        self.cfg.lock().symlink_policy = policy;
        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    fn set_homes(&self, patch: HomesPatch) -> Result<ApplyOutcome, CoreError> {
        let root = patch.root.clone().map(std::path::PathBuf::from);
        let ov = HomesOverride {
            enabled: patch.enabled,
            root: root.clone(),
        };
        self.store
            .mutate(|o| o.homes = Some(ov.clone()))
            .map_err(store_err)?;
        let mut cfg = self.cfg.lock();
        cfg.homes.enabled = patch.enabled;
        cfg.homes.root = root;
        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    fn set_watch(&self, patch: WatchPatch) -> Result<ApplyOutcome, CoreError> {
        let backend = watch_backend_from_wire(&patch.backend)?;
        if patch.hot_set_max == 0 {
            return Err(reject(
                "settings.must_be_at_least_one",
                json!({ "field": "watch.hot_set_max" }),
                "watch.hot_set_max: must be at least 1",
            ));
        }
        if patch.full_threshold == 0 {
            return Err(reject(
                "settings.must_be_at_least_one",
                json!({ "field": "watch.full_threshold" }),
                "watch.full_threshold: must be at least 1",
            ));
        }
        // No `full_threshold >= hot_set_max` rule on purpose: a hot set larger
        // than the burst threshold is exactly the case `full_threshold`
        // exists for (sc-watch's own comment), so the two are independent.
        let ov = WatchOverride {
            backend,
            hot_set_max: patch.hot_set_max,
            full_threshold: patch.full_threshold,
        };
        self.store
            .mutate(|o| o.watch = Some(ov.clone()))
            .map_err(store_err)?;
        let mut cfg = self.cfg.lock();
        cfg.watch.backend = backend;
        cfg.watch.hot_set_max = patch.hot_set_max;
        cfg.watch.full_threshold = patch.full_threshold;
        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    /// The one setter that can stop the server coming back up, so it refuses
    /// far more than it accepts. Three failures are worth naming, because all
    /// three are silent at save time and fatal at restart:
    ///
    /// - a `data_dir` the process cannot write → no database opens at all;
    /// - a `data_dir` that does not already hold `auth.db` →
    ///   `masterkey::load_or_generate` mints a *fresh* key, and the server
    ///   comes up in first-run setup with every account gone;
    /// - a `master_key_file` pointing at different bytes → `AuthService::new`
    ///   cannot decrypt a single stored secret.
    ///
    /// So this treats both as *relocation* settings: the target must already
    /// contain the data, and the key must already be the same key. What it
    /// cannot check — that the Samba sidecar mounts the new
    /// `smb.config_dir` — is stated in the UI hint instead.
    ///
    /// Moving `smb.config_dir` also leaves the previously rendered `smb.conf`
    /// and NT-hash file behind at the old path: `render_live` writes (and, on
    /// disable, removes) wherever the live `Config` now points, so nothing
    /// cleans up the old directory. The UI hint says to delete it by hand.
    ///
    /// Recovery, if a change turns out wrong anyway: `bootstrap()` opens
    /// `settings.db` at *config.toml*'s `data_dir` before overrides apply, so
    /// the override store never moves and the change is always reversible
    /// from the same screen that made it.
    fn set_paths(&self, patch: PathsPatch) -> Result<ApplyOutcome, CoreError> {
        let (cur_data_dir, cur_key_path) = {
            let cfg = self.cfg.lock();
            (cfg.data_dir.clone(), cfg.master_key_path())
        };
        let (data_dir, master_key_file, smb_config_dir) =
            validate_paths(&cur_data_dir, &cur_key_path, &patch)?;

        let ov = PathsOverride {
            data_dir: data_dir.clone(),
            master_key_file: master_key_file.clone(),
            smb_config_dir: smb_config_dir.clone(),
        };
        self.store
            .mutate(|o| o.paths = Some(ov.clone()))
            .map_err(store_err)?;
        let mut cfg = self.cfg.lock();
        cfg.data_dir = data_dir;
        cfg.master_key_file = master_key_file;
        cfg.smb.config_dir = smb_config_dir;
        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    /// Restart-required in full. Nothing here is live-swappable: the relying
    /// party holds a TLS client and two caches built once at `App::build`,
    /// and `redirect_uri` has to equal what is registered at the IdP, which
    /// no in-process change could verify.
    ///
    /// The two `config.toml`-only keys are not reachable from here at all --
    /// this patch type does not carry them, so a stored override can never
    /// contain them and `apply_settings_overrides` can never re-apply one
    /// over the operator's file.
    fn set_oidc(&self, patch: sc_http::settings_api::OidcPatch) -> Result<ApplyOutcome, CoreError> {
        let smb_policy = oidc_smb_policy_from_wire(&patch.smb_policy)?;
        let redirect_uris: Vec<String> = patch
            .redirect_uris
            .iter()
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect();
        let mut cfg = self.cfg.lock();
        // Refused at save time rather than discovered as "the SSO button
        // stopped appearing" after the restart. Checked against the
        // *effective* `app_hosts`, not the booted one: an administrator who
        // has just added a host and not yet restarted is declaring intent,
        // and both changes need the same restart before either takes effect.
        if patch.enabled {
            for uri in &redirect_uris {
                if let Some(problem) = crate::config::redirect_uri_problem(uri, &cfg.app_hosts) {
                    let key = if uri.starts_with("https://") {
                        "settings.oidc_redirect_host_not_served"
                    } else {
                        "settings.oidc_redirect_uri_must_be_https"
                    };
                    return Err(reject(
                        key,
                        json!({ "value": uri }),
                        format!("oidc.redirect_uris: {problem}"),
                    ));
                }
            }
            // `client_secret_file` is `config.toml` only, deliberately, and
            // nothing checked it here: saving `enabled = true` reported
            // success and the next boot logged "client_secret_file is not
            // set" and stayed off.
            let readable = cfg
                .oidc
                .client_secret_file
                .as_ref()
                .is_some_and(|p| std::fs::File::open(p).is_ok());
            if !readable {
                return Err(reject(
                    "settings.oidc_secret_file_missing",
                    json!({
                        "path": cfg
                            .oidc
                            .client_secret_file
                            .as_ref()
                            .map(|p| p.display().to_string())
                            .unwrap_or_default()
                    }),
                    "oidc.client_secret_file is not set or cannot be read; single sign-on \
                     would stay off after the restart",
                ));
            }
        }
        let ov = OidcOverride {
            enabled: patch.enabled,
            issuer: patch.issuer.trim().to_string(),
            client_id: patch.client_id.trim().to_string(),
            redirect_uris,
            scopes: patch.scopes.clone(),
            display_name: patch.display_name.clone(),
            allow_private_endpoints: patch.allow_private_endpoints,
            smb_policy,
        };
        self.store
            .mutate(|o| o.oidc = Some(ov.clone()))
            .map_err(store_err)?;

        cfg.oidc.enabled = ov.enabled;
        cfg.oidc.issuer = ov.issuer;
        cfg.oidc.client_id = ov.client_id;
        cfg.oidc.redirect_uris = ov.redirect_uris;
        cfg.oidc.scopes = ov.scopes;
        cfg.oidc.display_name = ov.display_name;
        cfg.oidc.allow_private_endpoints = ov.allow_private_endpoints;
        cfg.oidc.smb_policy = smb_policy;

        Ok(ApplyOutcome {
            applied_live: false,
            restart_required: true,
        })
    }

    /// Drop one group's override and put the process back on what
    /// `config.toml` and the environment say.
    ///
    /// The restored config is rebuilt as `file_cfg` plus the *remaining*
    /// overrides, so reverting one group never disturbs another. For a
    /// live-appliable group the restored values are then pushed into the
    /// running components exactly as the corresponding `set_*` would —
    /// otherwise this would report success and leave the process running the
    /// value it just discarded.
    fn clear_section(&self, section: SettingsSection) -> Result<ApplyOutcome, CoreError> {
        let mut cfg = self.cfg.lock();
        let mut next = self.store.load();
        if !next.is_set(section) {
            // Already the file's. Reported as a no-op success rather than an
            // error: the button is disabled in that state, and a double click
            // must not read as a failure.
            return Ok(ApplyOutcome {
                applied_live: false,
                restart_required: false,
            });
        }
        next.clear(section);
        let mut candidate = self.file_cfg.clone();
        candidate.apply_settings_overrides(&next);

        // Everything that can fail happens before anything is persisted, the
        // same ordering `set_smb` uses and for the same reason: a failed
        // render must not leave `smb.enabled` durably recorded with nothing
        // rendered anywhere.
        let mut restored_render = None;
        if section == SettingsSection::Smb {
            // Both enforcement sites read `totp_policy` off `AuthService`, so
            // the render below sees the restored value only if it is pushed
            // first, and the old one goes back if the render fails.
            let previous = self.auth.smb_totp_policy();
            self.auth
                .set_smb_totp_policy(match candidate.smb.totp_policy {
                    sc_smb::TotpPolicy::RequireSeparate => sc_auth::SmbTotpPolicy::RequireSeparate,
                    sc_smb::TotpPolicy::Block => sc_auth::SmbTotpPolicy::Block,
                });
            match crate::smb_cmd::render_live(&candidate, &self.core, &self.auth) {
                Ok(out) => restored_render = Some(out),
                Err(e) => {
                    self.auth.set_smb_totp_policy(previous);
                    return Err(CoreError::InvalidName(e.to_string()));
                }
            }
        }

        self.store
            .mutate(|o| o.clear(section))
            .map_err(store_err)?;

        match section {
            SettingsSection::Search => {
                self.search_concurrency
                    .reconfigure(&SearchLimitsConfig::from(&candidate.search));
                self.search_rate.reconfigure(
                    candidate.search.rate_per_minute,
                    Duration::from_secs(60),
                );
            }
            SettingsSection::Archive => {
                self.archive_concurrency
                    .resize(candidate.archive.max_concurrent.max(1) as usize);
            }
            SettingsSection::Smb => {
                if let Some(out) = restored_render {
                    self.record_render_outcome(out);
                }
            }
            _ => {}
        }

        let smb_enabled_changed = cfg.smb.enabled != candidate.smb.enabled;
        *cfg = candidate;

        Ok(match section {
            SettingsSection::Search | SettingsSection::Archive => ApplyOutcome {
                applied_live: true,
                restart_required: false,
            },
            SettingsSection::Smb => ApplyOutcome {
                applied_live: !smb_enabled_changed,
                restart_required: smb_enabled_changed,
            },
            _ => ApplyOutcome {
                applied_live: false,
                restart_required: true,
            },
        })
    }

    fn request_restart(&self) -> Result<(), CoreError> {
        self.restart_signal.notify_one();
        Ok(())
    }
}

#[cfg(test)]
impl DelegatedValues {
    /// What the two delegated stores report for a deployment where neither
    /// has ever been written: the config file's own values.
    fn from_config(cfg: &Config) -> Self {
        Self {
            name_enabled: cfg.index.name_enabled,
            name_enabled_overridden: false,
            chunk_min_bytes: cfg.upload.chunk_min_bytes,
            chunk_default_bytes: cfg.upload.chunk_default_bytes,
            chunk_overridden: false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::SettingsOverrides;

    fn default_fields() -> Vec<SettingsField> {
        let cfg = Config::default();
        let live = DelegatedValues::from_config(&cfg);
        SettingsBridge::snapshot_fields(&cfg, &SettingsOverrides::default(), live)
    }

    /// `Config` serialized to JSON, flattened to the same dotted keys
    /// `snapshot_fields` emits. Derived from the struct itself rather than
    /// listed by hand — a hand-written list is exactly what goes stale the
    /// day someone adds a config field.
    fn config_leaf_keys(v: &serde_json::Value, prefix: &str, out: &mut Vec<String>) {
        match v {
            serde_json::Value::Object(map) => {
                for (k, inner) in map {
                    let key = if prefix.is_empty() {
                        k.clone()
                    } else {
                        format!("{prefix}.{k}")
                    };
                    config_leaf_keys(inner, &key, out);
                }
            }
            // A list (`shares`, `app_hosts`) is one setting, not one per element.
            _ => out.push(prefix.to_string()),
        }
    }

    /// The two keys whose wire name differs from their `Config` field name:
    /// `smb_service_uid`/`smb_service_gid` are flat on `Config` (they are
    /// sc-server's, not `sc_smb::SmbConfig`'s) but belong under the SMB
    /// group on screen.
    fn wire_key(config_key: &str) -> &str {
        match config_key {
            "smb_service_uid" => "smb.service_uid",
            "smb_service_gid" => "smb.service_gid",
            other => other,
        }
    }

    /// The requirement this whole screen exists for: every config-editable
    /// field is reachable from it — editable, or read-only with a stated
    /// reason. Adding a field to `Config` without deciding which of the two
    /// it is fails here rather than shipping a silently unreachable setting.
    #[test]
    fn every_config_field_is_reachable_from_the_settings_screen() {
        let cfg = Config::default();
        let mut config_keys = Vec::new();
        config_leaf_keys(&serde_json::to_value(&cfg).unwrap(), "", &mut config_keys);

        let live = DelegatedValues::from_config(&cfg);
        let fields = SettingsBridge::snapshot_fields(&cfg, &SettingsOverrides::default(), live);
        let covered: std::collections::HashSet<&str> =
            fields.iter().map(|f| f.key.as_str()).collect();

        let missing: Vec<&String> = config_keys
            .iter()
            .filter(|k| !covered.contains(wire_key(k)))
            .collect();
        assert!(
            missing.is_empty(),
            "config fields with no row on the settings screen: {missing:?}"
        );
    }

    /// `ServerSettingsSection.svelte`'s own source, read at compile time.
    ///
    /// The list this test needs is a fact only the frontend knows, and a
    /// hand-written copy of it here is a second source of truth with nothing
    /// comparing the two — which is exactly how `smb.server_name` came to be
    /// advertised as editable with no control anywhere. Reaching outside the
    /// crate directory is safe because this workspace sets `publish = false`;
    /// it would not be in a crate that is packaged.
    ///
    /// A Rust test rather than a frontend one because the frontend suite
    /// cannot run on every development machine here, and this particular
    /// check has to fail on the machine making the change.
    const UI_SOURCE: &str =
        include_str!("../../../web/src/lib/ui/admin/ServerSettingsSection.svelte");

    /// The `EDITABLE_KEYS` array from that source, as the screen means it:
    /// the keys it excludes from its read-only "other" list.
    fn ui_editable_keys() -> Vec<String> {
        let (_, rest) = UI_SOURCE
            .split_once("const EDITABLE_KEYS = new Set([")
            .expect("ServerSettingsSection.svelte must declare EDITABLE_KEYS");
        let (body, _) = rest.split_once("])").expect("EDITABLE_KEYS must be closed");
        body.split(',')
            .filter_map(|entry| {
                let e = entry.trim();
                // Skip comment lines and the trailing empty split.
                let e = e.lines().map(str::trim).find(|l| l.starts_with('\''))?;
                e.trim_matches(|c| c == '\'' || c == ',')
                    .to_string()
                    .into()
            })
            .collect()
    }

    /// What the screen can change and what the server says it can change,
    /// asserted against each other rather than against a copy.
    ///
    /// What this catches is a key that falls into the read-only section with
    /// no reason attached. What it cannot see is whether a listed key has an
    /// input rendered for it, and there is one key where those differ on
    /// purpose: `oidc.smb_policy` has a single accepted value, so the form
    /// sends the constant and the hint under it says what the value means.
    #[test]
    fn a_field_is_either_editable_in_the_ui_or_carries_a_reason() {
        let ui: std::collections::HashSet<String> = ui_editable_keys().into_iter().collect();
        assert!(!ui.is_empty(), "EDITABLE_KEYS parsed as empty");
        let fields = default_fields();
        let emitted: std::collections::HashSet<&str> =
            fields.iter().map(|f| f.key.as_str()).collect();

        for f in &fields {
            let has_control = ui.contains(f.key.as_str());
            assert_eq!(
                has_control,
                f.readonly_reason_key.is_none(),
                "{}: readonly_reason and the UI's control list disagree",
                f.key
            );
        }
        // And the other direction: a control for a key the server no longer
        // sends is a form field that saves nothing.
        let stale: Vec<&String> = ui.iter().filter(|k| !emitted.contains(k.as_str())).collect();
        assert!(stale.is_empty(), "UI controls for keys the server does not send: {stale:?}");
    }

    /// The classification an operator acts on. Wrong here is worse than
    /// absent: an "applies immediately" badge on a field that needs a restart
    /// means the change looks done and is not.
    #[test]
    fn only_the_genuinely_live_fields_are_advertised_as_live() {
        let fields = default_fields();
        let live: Vec<&str> = fields
            .iter()
            .filter(|f| f.readonly_reason_key.is_none() && !f.restart_required)
            .map(|f| f.key.as_str())
            .collect();
        assert_eq!(
            live,
            vec![
                // `smb_cmd::render_live` rewrites `smb.conf`/`passwd` and the
                // privileged agent reloads smbd.
                "smb.workgroup",
                "smb.server_name",
                "smb.service_user",
                "smb.allow_public_bind",
                "smb.totp_policy",
                "smb.service_uid",
                "smb.service_gid",
                // `SearchConcurrency::reconfigure` + `KeyedTokenBucket::reconfigure`
                // on the same `Arc`s `AppState` holds.
                "search.max_concurrent_fast",
                "search.max_concurrent_slow",
                "search.walk_deadline_fast_ms",
                "search.walk_deadline_slow_ms",
                "search.rate_per_minute",
                // `ResizableSemaphore::resize`.
                "archive.max_concurrent",
            ],
            "everything else is baked in at App::build and must say so"
        );
    }

    /// Three rows are owned at runtime by `index.db` and `upload.db`, and the
    /// screen used to show this bridge's own boot-time `Config` copy for all
    /// three with `config_file` hardcoded as the source.
    #[test]
    fn the_delegated_rows_report_the_live_value_and_a_real_source() {
        let cfg = Config::default();
        let live = DelegatedValues {
            name_enabled: true,
            name_enabled_overridden: true,
            chunk_min_bytes: 8 * 1024 * 1024,
            chunk_default_bytes: 32 * 1024 * 1024,
            chunk_overridden: true,
        };
        let fields = SettingsBridge::snapshot_fields(&cfg, &SettingsOverrides::default(), live);
        let row = |k: &str| fields.iter().find(|f| f.key == k).expect(k);

        assert_eq!(row("index.name_enabled").value, json!(true));
        assert_eq!(row("index.name_enabled").source, SettingsSource::AdminOverride);
        assert_eq!(row("upload.chunk_default_bytes").value, json!(32 * 1024 * 1024));
        assert_eq!(
            row("upload.chunk_min_bytes").source,
            SettingsSource::AdminOverride
        );

        // With no row in either store, the config file's own value is what is
        // reported and the source says so.
        let untouched = SettingsBridge::snapshot_fields(
            &cfg,
            &SettingsOverrides::default(),
            DelegatedValues::from_config(&cfg),
        );
        let row = |k: &str| untouched.iter().find(|f| f.key == k).expect(k);
        assert_eq!(row("index.name_enabled").value, json!(false));
        assert_eq!(
            row("index.name_enabled").source,
            SettingsSource::BuiltinDefault
        );
    }

    /// Saved-but-pending used to be indistinguishable from in-effect: the
    /// screen showed the new value with nothing to say the running server was
    /// still on the old one.
    #[test]
    fn a_saved_restart_required_change_reports_what_the_server_is_still_running() {
        let booted = Config::default();
        let saved = Config {
            public_origins: vec!["https://cloud.example.com".into()],
            search: crate::config::SearchConfig {
                rate_per_minute: 90,
                ..booted.search.clone()
            },
            ..booted.clone()
        };
        let live = DelegatedValues::from_config(&booted);
        let o = SettingsOverrides::default();
        let mut fields = SettingsBridge::snapshot_fields(&saved, &o, live);
        mark_pending(&mut fields, &SettingsBridge::snapshot_fields(&booted, &o, live));
        let row = |k: &str| fields.iter().find(|f| f.key == k).expect(k);

        assert_eq!(row("public_origins").value, json!(["https://cloud.example.com"]));
        assert_eq!(row("public_origins").running_value, Some(json!([])));
        // A live-applied field's saved value is its running value, so it must
        // not be flagged as waiting for anything.
        assert_eq!(row("search.rate_per_minute").value, json!(90));
        assert_eq!(row("search.rate_per_minute").running_value, None);
        // And an untouched restart-required row says nothing at all.
        assert_eq!(row("bind").running_value, None);
    }

    /// The dead key is gone from the screen entirely, not shown read-only
    /// with a better reason: nothing read it, so it looked like a supported
    /// switch that did nothing.
    #[test]
    fn the_dead_content_index_key_has_no_row_at_all() {
        assert!(!default_fields()
            .iter()
            .any(|f| f.key == "index.content_enabled"));
    }

    /// The snapshot's value has to be a value the patch would accept back —
    /// `WatchBackend` is `rename_all = "snake_case"`, so the `Debug` form
    /// this row used to emit ("Auto") round-trips to a 422.
    #[test]
    fn watch_backend_round_trips_between_snapshot_and_patch() {
        for b in [
            WatchBackend::Auto,
            WatchBackend::Hotset,
            WatchBackend::InotifyFull,
            WatchBackend::Fanotify,
        ] {
            let wire = watch_backend_to_wire(b);
            assert_eq!(watch_backend_from_wire(wire).unwrap(), b, "{wire}");
            // And it is the serde name, not something parallel that drifts.
            assert_eq!(serde_json::to_value(b).unwrap(), serde_json::json!(wire));
        }
        assert!(watch_backend_from_wire("Auto").is_err());
    }

    fn paths_patch(data_dir: &Path, key: Option<&Path>, smb: &Path) -> PathsPatch {
        PathsPatch {
            data_dir: data_dir.display().to_string(),
            master_key_file: key.map(|p| p.display().to_string()),
            smb_config_dir: smb.display().to_string(),
        }
    }

    /// A `data_dir` that does not already hold `auth.db` is the brick: the
    /// restart mints a fresh master key and comes up in first-run setup with
    /// every account gone. Refused at save time, not discovered at boot.
    #[test]
    fn set_paths_refuses_a_data_dir_that_has_not_been_migrated() {
        let tmp = tempfile::tempdir().unwrap();
        let cur = tmp.path().join("cur");
        let new = tmp.path().join("new");
        std::fs::create_dir_all(&cur).unwrap();
        std::fs::create_dir_all(&new).unwrap();
        std::fs::write(cur.join("auth.db"), b"x").unwrap();
        std::fs::write(cur.join("master.key"), [7u8; 32]).unwrap();

        let err = validate_paths(
            &cur,
            &cur.join("master.key"),
            &paths_patch(&new, None, tmp.path()),
        )
        .unwrap_err();
        assert!(format!("{err:?}").contains("auth.db"), "{err:?}");

        // With the data actually moved, the same change is accepted.
        std::fs::write(new.join("auth.db"), b"x").unwrap();
        std::fs::write(new.join("master.key"), [7u8; 32]).unwrap();
        validate_paths(
            &cur,
            &cur.join("master.key"),
            &paths_patch(&new, None, tmp.path()),
        )
        .unwrap();
    }

    /// `master_key_file` relocates the key; it never substitutes a different
    /// one. A different key means `AuthService::new` cannot decrypt a single
    /// stored secret after the restart.
    #[test]
    fn set_paths_refuses_a_master_key_that_is_not_the_current_key() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = tmp.path().to_path_buf();
        let cur_key = dir.join("master.key");
        std::fs::write(&cur_key, [7u8; 32]).unwrap();

        let other = dir.join("other.key");
        std::fs::write(&other, [9u8; 32]).unwrap();
        let err = validate_paths(&dir, &cur_key, &paths_patch(&dir, Some(&other), &dir)).unwrap_err();
        assert!(format!("{err:?}").contains("settings.master_key_contents_differ"), "{err:?}");

        // Absent is refused too — that is the path that mints a fresh key.
        let missing = dir.join("nope.key");
        assert!(validate_paths(&dir, &cur_key, &paths_patch(&dir, Some(&missing), &dir)).is_err());

        // A true copy is a relocation, and allowed.
        let copy = dir.join("copy.key");
        std::fs::write(&copy, [7u8; 32]).unwrap();
        validate_paths(&dir, &cur_key, &paths_patch(&dir, Some(&copy), &dir)).unwrap();
    }

    #[test]
    fn set_paths_refuses_a_directory_that_does_not_exist_or_is_relative() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = tmp.path().to_path_buf();
        let cur_key = dir.join("master.key");
        std::fs::write(&cur_key, [7u8; 32]).unwrap();

        let absent = dir.join("absent");
        assert!(validate_paths(&dir, &cur_key, &paths_patch(&absent, None, &dir)).is_err());
        assert!(validate_paths(&dir, &cur_key, &paths_patch(&dir, None, &absent)).is_err());

        let mut relative = paths_patch(&dir, None, &dir);
        relative.data_dir = "relative/path".into();
        assert!(validate_paths(&dir, &cur_key, &relative).is_err());
    }
}
