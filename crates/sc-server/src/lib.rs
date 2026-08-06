//! `sc-server` — the main binary. It assembles every other crate in the
//! workspace and owns nothing but the assembly:
//! config loading, startup diagnostics, master-key management, first-run
//! bootstrap, graceful shutdown, and a CLI.
//!
//! The interesting part is [`app`]: it is the only place where `sc-core`,
//! `sc-http`, `sc-dav`, `sc-upload`, `sc-watch` and (feature-gated)
//! `sc-compat-nc` are all in scope at once. [`bridge`] holds the trait impls
//! that join the protocol crates' storage contracts to the real core.

pub mod app;
pub mod bridge;
pub mod config;
pub mod dav_uploads;
pub mod diagnostics;
pub mod hardening;
pub mod masterkey;
#[cfg(feature = "compat-nc")]
pub mod nc;
// Not itself feature-gated: `build_oidc` has to exist either way so that
// `app.rs` has one call site, and with the `oidc` feature off it answers with
// the disabled relying party. The feature gate is inside, around the only
// code that mentions `sc-oidc`.
pub mod oidc;
pub mod passdb;
pub mod routes;
pub mod settings_bridge;
pub mod settings_store;
pub mod setup;
pub mod shutdown;
pub mod smb_cmd;
pub mod storage_class;

use std::path::PathBuf;

use anyhow::Context;
use clap::{Parser, Subcommand};

#[derive(Parser, Debug)]
#[command(name = "sc-server", about = "stowcloud main server binary")]
pub struct Cli {
    /// Path to the TOML config file. Defaults to `<data-dir>/sc.toml` if it
    /// exists, else built-in defaults. The data directory here is
    /// `SC_DATA_DIR`, or the built-in default when that is unset: the file
    /// cannot name the directory it is looked up in.
    #[arg(long, global = true)]
    pub config: Option<PathBuf>,

    #[command(subcommand)]
    pub command: Option<Command>,
}

#[derive(Subcommand, Debug, Clone)]
pub enum Command {
    /// Run the server (default if no subcommand is given).
    Serve,
    /// Print the kernel capability probe (same as the `sc-caps` binary).
    Caps,
    /// First-run bootstrap: print + persist a one-time setup token.
    Setup,
    /// Run housekeeping (DB incremental vacuum; see).
    Gc,
    /// Dump the route table. `--json` for machine-readable output (the
    /// Compat-isolation CI gate greps this).
    Routes {
        #[arg(long)]
        json: bool,
    },
    /// Render and write smb.conf/smbpasswd/passwd from config.
    SmbSync,
    /// Name-index (T3) maintenance: build, merge, or
    /// inspect the on-disk `.scindex/names` a share may have. Every
    /// subcommand refuses to run unless `[index] name_enabled = true` is set
    /// in the config — the same "off by default, never automatic" rule
    /// states for the feature as a whole; the flag
    /// existed in `Config` before this and nothing read it (`config.rs`'s
    /// `IndexConfig` doc comment), so this is what makes it live.
    ///
    /// A CLI subcommand rather than an HTTP route: this walks the whole
    /// tree (12 TB, per the documented hardware floor), so it belongs beside
    /// `gc` and `smb-sync` as an operator-triggered offline operation, not as
    /// a request a browser tab can kick off and walk away from. It also
    /// keeps the trigger inside this crate's ownership — an admin HTTP route
    /// would mean editing `sc-http`/`routes.rs`, which this change does not
    /// own.
    Index {
        #[command(subcommand)]
        action: IndexAction,
    },
    /// Master key management.
    Masterkey {
        #[command(subcommand)]
        action: MasterkeyAction,
    },
}

#[derive(Subcommand, Debug, Clone)]
pub enum MasterkeyAction {
    /// Generate a new master key, re-encrypt every SMB NT hash and TOTP
    /// secret under it, and swap the key file on disk.
    ///
    /// A CLI subcommand, not an HTTP route: a master key is exactly the kind
    /// of secret's "never over HTTP" rule for the key
    /// itself (not just its material) is written for — a browser tab has no
    /// business ever holding it, so rotation belongs at the same trust level
    /// as `smb-sync`/`gc`, run by whoever already has shell access to the
    /// data directory.
    Rotate,
}

#[derive(Subcommand, Debug, Clone)]
pub enum IndexAction {
    /// Build (or fully rebuild) the name index for one share, or every
    /// registered share when `--share` is omitted. A rebuild discards
    /// whatever segments already exist and recrawls from scratch
    /// ("initial activation") — the same primitive
    /// `SearchBridge::build_name_index` exposes to tests, now reachable in a
    /// running deployment.
    Build {
        #[arg(long)]
        share: Option<String>,
    },
    /// Fold accumulated delta+tombstone segments into a fresh base segment.
    /// Skipped for a share whose index is already
    /// within `merge_ratio` of its base, or has no index at all.
    Merge {
        #[arg(long)]
        share: Option<String>,
    },
    /// Print index stats (entry/segment counts, generation, on-disk bytes)
    /// for one share, or every share that has an index.
    Status {
        #[arg(long)]
        share: Option<String>,
    },
}

impl IndexAction {
    fn share_filter(&self) -> Option<&str> {
        match self {
            IndexAction::Build { share }
            | IndexAction::Merge { share }
            | IndexAction::Status { share } => share.as_deref(),
        }
    }
}

pub fn print_kernel_caps() {
    let caps = sc_vfs::detect_kernel_caps();
    let detail = diagnostics_openat2_status();
    println!("sc-vfs kernel capability probe:");
    println!("  openat2:         {:?} ({detail:?})", caps.openat2);
    println!("  statx.btime:     {:?}", caps.statx_btime);
    println!("  renameat2:       {:?}", caps.renameat2);
    println!("  copy_file_range: {:?}", caps.copy_file_range);
    println!("  landlock:        {:?}", caps.landlock);
}

// Re-expose the detailed openat2 probe without making `diagnostics`'s
// internals public API surface beyond what it already is.
fn diagnostics_openat2_status() -> diagnostics::OpenAt2Status {
    // The probe is side-effect-free and cheap; run it fresh here rather
    // than threading a second copy of `Diagnostics` through just for this.
    diagnostics::run(
        &config::Config::default(),
        &masterkey::MasterKeyResult {
            key: [0u8; 32],
            inside_data_dir: false,
            generated: false,
        },
        0,
        Ok(()),
        true,
    )
    .openat2_detail
}

/// Load config + master key, run diagnostics, and return both. Shared by
/// `serve`, `gc`, and `smb-sync` so every entry point sees the same
/// derived state.
pub fn bootstrap(cli: &Cli) -> anyhow::Result<(config::Config, masterkey::MasterKeyResult)> {
    let (mut cfg, from) = config::Config::load_from(cli.config.as_deref())?;
    // Printed rather than logged: this runs before the startup diagnostics
    // block and answers the question an operator asks when a setting appears
    // to have been ignored, which is "did it read my file at all".
    match &from {
        Some(p) => println!("[sc] config: {}", p.display()),
        None => println!(
            "[sc] config: built-in defaults (no --config, and no {})",
            config::Config::default_config_path().display()
        ),
    }
    std::fs::create_dir_all(&cfg.data_dir)?;
    // Fold in the server-settings admin screen's persisted overrides
    // (`settings_store.rs`) *after* the file+env config and *before*
    // anything else derives from `cfg` — this is the single insertion point
    // that covers `cmd_serve`, `cmd_gc`, and `cmd_smb_sync` alike, since all
    // three call this function. `cmd_masterkey_rotate` deliberately does not
    // call `bootstrap` (see its own doc), so it never sees these overrides —
    // it doesn't build an `App` or serve, so it has nothing to apply them to.
    let settings_db = cfg.data_dir.join("settings.db");
    match settings_store::SettingsStore::open(&settings_db) {
        Ok(store) => cfg.apply_settings_overrides(&store.load()),
        Err(e) => tracing::warn!(error = %e, "settings overrides not loaded; using config.toml as-is"),
    }
    let key = masterkey::load_or_generate(&cfg)?;
    Ok((cfg, key))
}

pub fn run_diagnostics_and_print(cfg: &config::Config, key: &masterkey::MasterKeyResult) {
    let db_path = cfg.data_dir.join("meta.db");
    let db_bytes = sc_meta::MetaStore::open(&db_path)
        .and_then(|m| m.size_bytes())
        .unwrap_or(0);

    let smb_bind_result = if cfg.smb.enabled {
        let orch = sc_smb::SmbOrchestrator::new(cfg.smb.clone());
        let ifaces = diagnostics::local_interface_addrs();
        orch.validate_bind(&ifaces).map_err(|e| e.to_string())
    } else {
        Ok(())
    };

    let d = diagnostics::run(
        cfg,
        key,
        db_bytes,
        smb_bind_result,
        cfg.content_hosts.is_empty(),
    );
    diagnostics::print(&d);

    if d.any_share_rejected() {
        tracing::error!("one or more configured shares were rejected at startup (see above); they will not be registered");
    }

    // Keep it: `GET /api/health` has to answer from something, and this was
    // being printed and dropped. `set` rather than overwrite — `gc` and
    // `smb-sync` run this too, and the serving process's snapshot is the one
    // a request should be answered from.
    let _ = diagnostics::SNAPSHOT.set(d);
}

/// The service [`cmd_serve`] hands to `axum::serve`, and the only one it may.
///
/// `into_make_service_with_connect_info` is what puts the accepted socket's
/// peer address into request extensions. Serving the bare router — which is
/// what this used to do — does not, and then *nothing* in the
/// process knows where a request came from: `sc_http::middleware::trusted_proxy`
/// finds no peer, so it cannot treat anyone as a trusted proxy, so it discards
/// `CF-Connecting-IP` as well. Every IP-keyed control then collapses onto one
/// address for the entire internet: the login brute-force gate (`sc_auth`'s
/// `ip_gate`, which covers DAV and compat Basic auth too), the general API
/// rate limiter, `session.ip_first`, and the audit log's `ip` column.
///
/// The share-link and search limits are *not* in that list — they are keyed by
/// link token and by `UserId` respectively, on purpose (`sc_http::state`), so
/// they survive a bad peer address. Worth stating, because assuming they
/// collapsed too overstates the blast radius of the same bug.
///
/// It is a named function so `tests/client_ip.rs` can bind a real socket and
/// serve through this exact expression rather than a copy of it.
pub fn connect_info_service(
    router: axum::Router,
) -> axum::extract::connect_info::IntoMakeServiceWithConnectInfo<axum::Router, std::net::SocketAddr>
{
    router.into_make_service_with_connect_info::<std::net::SocketAddr>()
}

pub async fn cmd_serve(cli: &Cli) -> anyhow::Result<()> {
    let (cfg, key) = bootstrap(cli)?;
    run_diagnostics_and_print(&cfg, &key);

    #[cfg(target_os = "linux")]
    {
        let mut restrict: Vec<PathBuf> = vec![cfg.data_dir.clone()];
        restrict.extend(cfg.shares.iter().map(|s| s.host_path.clone()));
        // Samba's config directory is written *at runtime*, by the passdb
        // publisher thread — and that thread is spawned after this call, so
        // unlike the request handlers it really is inside the Landlock
        // domain. Without this rule every republish fails
        // `io error at /config/smb/smb.conf: Permission denied`, which means
        // a revoked grant is never withdrawn from the file smbd reads.
        //
        // The directory has to exist before the ruleset is built: a rule can
        // only name a path that opens, and `write_all`'s own `create_dir_all`
        // would itself be denied once the domain is in force.
        if cfg.smb.enabled {
            if let Err(e) = std::fs::create_dir_all(&cfg.smb.config_dir) {
                tracing::warn!(
                    path = %cfg.smb.config_dir.display(),
                    error = %e,
                    "smb: could not create the config directory; SMB rendering will fail"
                );
            }
            restrict.push(cfg.smb.config_dir.clone());
        }
        let h = hardening::apply(&restrict);
        tracing::info!(landlock = ?h.landlock, seccomp = ?h.seccomp, "process self-restriction applied (best-effort)");
    }

    let bind = cfg.bind;
    let data_dir = cfg.data_dir.clone();
    let min_free_bytes = cfg.db.min_free_bytes;
    let app = app::App::build(cfg, &key)?;

    // 's always-on floor and its size cap. Both have
    // to be sampled by something to be a floor and a cap at all:
    // `Diagnostics::volume_free` and `db_bytes` are single readings taken
    // before the server accepted its first request. Sampled once here so a
    // server that starts on an already-full volume is gated immediately rather
    // than 30 seconds in.
    diagnostics::sample_storage_once(&data_dir, min_free_bytes, &app.meta);
    let _storage_sampler =
        diagnostics::spawn_storage_sampler(data_dir, min_free_bytes, app.meta.clone());

    // Issuing the one-time token belongs to *starting to
    // serve*, not to building an `App`: it is what makes an otherwise
    // unreachable deployment reachable, and it is re-issued on every restart
    // for as long as — and only as long as — there is still no account.
    // `gc` and `smb-sync` build the same `App` and must not mint one.
    match app.setup.arm_for_first_run() {
        Ok(true) => tracing::warn!(
            "no account exists: POST the setup token above to /api/setup to create the \
             administrator. The route closes permanently once an account exists."
        ),
        Ok(false) => {}
        Err(e) => tracing::error!(error = %e, "could not issue a first-run setup token"),
    }

    // Same reasoning as the setup gate above: the
    // sink belongs to *serving*, not to building an `App`. `gc` and
    // `smb-sync` build the same `App`, and `smb-sync` renders the very files
    // this publisher writes.
    app.arm_passdb_publisher();

    // The 60 s expiry sweep only makes sense once there is a runtime to spawn
    // it on, so it starts here rather than in `App::build`.
    let _sweeper = app.dav.spawn_lock_sweeper();
    // Same reasoning for the Login Flow v2 row sweep (`nc.rs::Compat::
    // spawn_login_flow_sweeper`): nothing called `flow_sweep` before this,
    // so expired rows never left the store.
    #[cfg(feature = "compat-nc")]
    let _nc_login_sweeper = app.compat.as_ref().map(|c| c.spawn_login_flow_sweeper());
    // `audit_prune` existed with no periodic caller — the exact "wrote it,
    // never called it" gap the Login Flow v2 sweeper above already had
    // (`sc_auth::audit::AUDIT_RETENTION_NS`).
    let _audit_sweeper = app.auth.spawn_audit_sweeper();
    let router = app.router();

    let listener = tokio::net::TcpListener::bind(bind).await?;
    tracing::info!(bind = %bind, routes = routes::route_table().len(), "sc-server listening");

    let restart_signal = app.http.restart_signal.clone();
    let serve = axum::serve(listener, connect_info_service(router));
    let mut restart_requested = false;
    tokio::select! {
        res = serve => {
            if let Err(e) = res {
                tracing::error!(error = %e, "server exited with error");
            }
        }
        _ = shutdown::wait_for_shutdown_signal() => {
            tracing::info!("shutdown signal received");
        }
        // `POST /api/admin/restart` (`ServerSettingsSection.svelte`'s
        // notify-and-restart flow): a settings change that needs a restart
        // to take effect. Runs through the exact same graceful-shutdown
        // sequence as a normal SIGTERM, then exits with a code distinct from
        // 0 so `systemd`'s `Restart=on-failure` (confirmed set for both
        // `sc-prod.service`/`sc-dev.service`) brings the process back up —
        // a plain `exit(0)` would not trigger a restart at all.
        _ = restart_signal.notified() => {
            tracing::info!("restart requested from the admin settings screen");
            restart_requested = true;
        }
    }

    let steps = shutdown::run_shutdown_sequence(&app);
    tracing::info!(?steps, "shutdown complete");
    if restart_requested {
        std::process::exit(75);
    }
    Ok(())
}

pub fn cmd_gc(cli: &Cli) -> anyhow::Result<()> {
    let (cfg, key) = bootstrap(cli)?;
    let db_path = cfg.data_dir.join("meta.db");
    let meta = sc_meta::MetaStore::open(&db_path)?;
    let before = meta.size_bytes()?;

    // Step 1: reap `node` rows whose `(dev, ino)`
    // is gone. That needs a live share tree to check against, which means
    // opening the shares — so build the domain layer, not just the DB.
    let app = app::App::build(cfg, &key)?;
    let mut reaped = 0usize;
    for def in app.core.share_defs() {
        let Some(root) = app.core.share(def.id) else {
            continue;
        };
        // `gc_dead_nodes` asks "is this (dev, ino) still present?", which the
        // filesystem cannot answer in reverse — there is no lookup-by-inode.
        // So walk the share once and answer from the set. A walk failure must
        // abort this share's GC rather than shrink the live set: a partial
        // walk would reap rows for files that do exist, and a reissued fileid
        // is exactly the corruption forbids.
        let mut live = std::collections::HashSet::new();
        if let Err(e) = collect_live(&root, &sc_vfs::SafePath::root(), &mut live) {
            tracing::warn!(share = %def.name, error = %e, "share walk failed; skipping node GC for it");
            continue;
        }
        match app
            .meta
            .gc_dead_nodes(def.id, &|dev, ino| live.contains(&(dev, ino)))
        {
            Ok(n) => reaped += n,
            Err(e) => tracing::warn!(share = %def.name, error = %e, "node GC failed for share"),
        }
    }

    app.meta.incremental_vacuum(u32::MAX)?;
    let after = app.meta.size_bytes()?;
    bridge::DB_BYTES.store(after, std::sync::atomic::Ordering::Relaxed);
    println!("gc: db {before} -> {after} bytes ({reaped} dead node row(s) reaped)");
    Ok(())
}

/// Depth-first `(dev, ino)` census of one share. Bounded by `SafePath`'s
/// `max_depth`, so plain recursion cannot overflow the stack for any path
/// `join` would have accepted.
fn collect_live(
    root: &sc_vfs::ShareRoot,
    path: &sc_vfs::SafePath,
    out: &mut std::collections::HashSet<(u64, u64)>,
) -> anyhow::Result<()> {
    let max_depth = root.policy().max_depth;
    for e in root.read_dir(path)? {
        let p = path.join(&e.name, max_depth)?;
        let Ok(st) = root.stat(&p) else { continue };
        out.insert((st.dev, st.ino));
        if st.kind == sc_vfs::Kind::Dir {
            collect_live(root, &p, out)?;
        }
    }
    Ok(())
}

/// `sc-server setup` — mint a token without starting the server.
///
/// `serve` issues its own at boot, so this exists for the case where an
/// operator wants the token before the process is up. Two things it must not
/// do: mint one for a deployment that already has an account (setup is
/// permanently closed then — `setup.rs`'s module docs), and mislead an
/// operator whose server is *already running*, because that process holds its
/// own token in memory and will reject this one.
pub fn cmd_setup(cli: &Cli) -> anyhow::Result<()> {
    let (cfg, key) = bootstrap(cli)?;
    let auth = sc_auth::AuthService::new(
        &cfg.data_dir.join("auth.db"),
        sc_auth::AuthConfig::default(),
        key.key,
    )?;
    if !auth.list_users()?.is_empty() {
        setup::remove_token_file(&cfg.data_dir);
        println!("An account already exists — first-run setup is complete and cannot be reopened.");
        println!("Use `/api/auth/login`; to add accounts, sign in as an existing one.");
        return Ok(());
    }
    setup::generate(&cfg.data_dir)?;
    println!();
    println!("Note: a server that is *already running* issued its own token at startup and");
    println!("will not accept this one. Restart it, or use the token it printed.");
    Ok(())
}

pub fn cmd_routes(json: bool) {
    let table = routes::route_table();
    if json {
        println!(
            "{}",
            serde_json::to_string_pretty(&table).expect("RouteInfo serializes")
        );
    } else {
        for r in &table {
            println!("{:<8} {:<50} [{}]", r.method, r.path, r.group);
        }
    }
}

pub fn cmd_smb_sync(cli: &Cli) -> anyhow::Result<()> {
    let (cfg, key) = bootstrap(cli)?;
    smb_cmd::run(&cfg, &key.key)
}

/// `sc-server masterkey rotate`.
///
/// Deliberately does not call [`bootstrap`]: that generates a key when none
/// exists, which is the wrong behaviour here — rotating "into" a key that
/// never existed would just be first-run generation with extra steps, and
/// there is nothing to rotate that a fresh key would decrypt anyway. Loads
/// the config the same way `bootstrap` does, but the key via
/// [`masterkey::load_existing`], which errors instead.
pub fn cmd_masterkey_rotate(cli: &Cli) -> anyhow::Result<()> {
    let cfg = config::Config::load(cli.config.as_deref())?;
    std::fs::create_dir_all(&cfg.data_dir)?;
    let old_key = masterkey::load_existing(&cfg).context("loading the current master key")?;

    // Constructing `AuthService` with the *old* key doubles as the pre-flight
    // check: `AuthService::new` refuses to start (see its doc, and
    // `sc_auth::rotate::verify_master_key`) if this key cannot decrypt what
    // is already in `auth.db` — exactly the situation that would make
    // rotating from it meaningless.
    let auth = sc_auth::AuthService::new(&cfg.data_dir.join("auth.db"), sc_auth::AuthConfig::default(), old_key)
        .context("opening auth.db with the current master key")?;

    let mut new_key = [0u8; 32];
    getrandom::getrandom(&mut new_key).map_err(|e| anyhow::anyhow!("generating a new master key: {e}"))?;

    let report = auth
        .rotate_master_key(&new_key)
        .context("rotating the master key (no row was changed; auth.db is untouched)")?;

    // The key file on disk is swapped only *after* the database transaction
    // above has committed — see `masterkey::swap_key_file`'s doc for the
    // narrow crash window this ordering leaves open, and
    // `sc_auth::rotate`'s module doc for why that window ends in a loud
    // refusal to start rather than silent data loss.
    masterkey::swap_key_file(&cfg, &new_key).context("writing the new master key file")?;

    println!(
        "master key rotated: key_ver {} -> {} ({} SMB secret(s), {} TOTP secret(s) re-encrypted)",
        report.old_key_ver, report.new_key_ver, report.smb_secrets_rotated, report.totp_secrets_rotated
    );
    println!("restart sc-server for the new key to take effect.");
    Ok(())
}

/// `sc-server index build|merge|status` — see [`Command::Index`]'s doc
/// comment for why this is a CLI subcommand and why it is gated on
/// `[index] name_enabled`.
pub fn cmd_index(cli: &Cli, action: IndexAction) -> anyhow::Result<()> {
    let (cfg, key) = bootstrap(cli)?;
    ensure_name_index_enabled(&cfg)?;
    // Building/inspecting an index needs live `ShareRoot`s (to crawl, or to
    // find the host path an index would live under), so this needs the whole
    // domain layer — same reasoning `cmd_gc` gives for building an `App`
    // rather than opening just the metadata DB.
    let app = app::App::build(cfg, &key)?;
    run_index_action(&app, action)
}

/// both indexes are off by default, and turning one
/// on is a per-deployment decision, never automatic. Split out from
/// `cmd_index` so it is testable without a full `bootstrap`/`App::build`.
///
/// Consults `index.db`'s admin override, not just
/// `config.toml`'s `[index] name_enabled` — an admin who flipped the toggle
/// on from the web UI expects `sc-server index build` to agree with it
/// without also editing the config file, same as `App::build`'s own check.
fn ensure_name_index_enabled(cfg: &config::Config) -> anyhow::Result<()> {
    let enabled = sc_search::IndexSettingsStore::open(&cfg.data_dir.join("index.db"), cfg.index.name_enabled)
        .context("opening index.db to check the name index override")?
        .name_enabled();
    if !enabled {
        anyhow::bail!(
            "the name index is disabled (`[index] name_enabled = false` in config.toml, and no \
             admin override in index.db —: both indexes are off by \
             default). Enable it from the admin settings UI, or set `name_enabled = true` under \
             `[index]` in the config file, then re-run; nothing here plants an index a \
             deployment did not explicitly ask for."
        );
    }
    Ok(())
}

/// The action logic proper, split out from `cmd_index` so tests can run it
/// against an `App` built directly (`app::App::build`, no config file / env
/// var dance) the same way `tests/first_run.rs` does.
fn run_index_action(app: &app::App, action: IndexAction) -> anyhow::Result<()> {
    let shares = index_target_shares(app, action.share_filter())?;

    match action {
        IndexAction::Build { .. } => {
            for def in shares {
                let idx = bridge::build_name_index(&app.core, def.id)
                    .with_context(|| format!("building name index for share {:?}", def.name))?;
                let st = idx.stats();
                println!(
                    "build  {:<20} entries={:<8} blocks={:<8} bytes={}",
                    def.name,
                    st.entries,
                    st.blocks,
                    idx.size_bytes()
                );
            }
        }
        IndexAction::Merge { .. } => {
            for def in shares {
                match bridge::open_existing_name_index(&app.core, def.id) {
                    None => println!("merge  {:<20} <no index — nothing to do>", def.name),
                    Some(idx) if !idx.needs_merge() => {
                        println!("merge  {:<20} <below merge_ratio — skipped>", def.name)
                    }
                    Some(idx) => {
                        // Unconditional gate: `app.rs::spawn_idle_merge`
                        // already runs this same check on a 10-minute timer,
                        // but an operator running this
                        // subcommand by hand has made the "now is a fine
                        // time" judgment themselves, so it isn't repeated
                        // here.
                        idx.merge(&|| true)?;
                        let st = idx.stats();
                        println!(
                            "merge  {:<20} generation={} bytes={}",
                            def.name,
                            st.generation,
                            idx.size_bytes()
                        );
                    }
                }
            }
        }
        IndexAction::Status { .. } => {
            for def in shares {
                match bridge::open_existing_name_index(&app.core, def.id) {
                    None => println!("{:<20} <no index>", def.name),
                    Some(idx) => {
                        let st = idx.stats();
                        println!(
                            "{:<20} entries={} base={} delta={} tombstones={} segments={} generation={} bytes={}",
                            def.name,
                            st.entries,
                            st.base_entries,
                            st.delta_entries,
                            st.tombstones,
                            st.delta_segments,
                            st.generation,
                            idx.size_bytes()
                        );
                    }
                }
            }
        }
    }
    Ok(())
}

/// Every registered share, or just the one named by `--share` (`Some`).
/// `Err` when a name is given but nothing registered matches it — silently
/// doing nothing for a typo'd `--share` would look like success.
fn index_target_shares(
    app: &app::App,
    filter: Option<&str>,
) -> anyhow::Result<Vec<sc_core::ShareDef>> {
    let defs = app.core.share_defs();
    match filter {
        None => Ok(defs),
        Some(name) => {
            let found: Vec<_> = defs.into_iter().filter(|d| d.name == name).collect();
            if found.is_empty() {
                anyhow::bail!("no registered share named {name:?}");
            }
            Ok(found)
        }
    }
}

/// Entry point shared by `main.rs`. A thin dispatcher so `main.rs` stays a
/// one-liner and everything above is unit-testable.
pub fn dispatch(cli: Cli) -> anyhow::Result<()> {
    let command = cli.command.clone().unwrap_or(Command::Serve);
    match command {
        Command::Serve => {
            let rt = tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()?;
            rt.block_on(cmd_serve(&cli_with_no_command(&cli)))
        }
        Command::Caps => {
            print_kernel_caps();
            Ok(())
        }
        Command::Setup => cmd_setup(&cli_with_no_command(&cli)),
        Command::Gc => cmd_gc(&cli_with_no_command(&cli)),
        Command::Routes { json } => {
            cmd_routes(json);
            Ok(())
        }
        Command::SmbSync => cmd_smb_sync(&cli_with_no_command(&cli)),
        Command::Index { action } => cmd_index(&cli_with_no_command(&cli), action),
        Command::Masterkey { action } => match action {
            MasterkeyAction::Rotate => cmd_masterkey_rotate(&cli_with_no_command(&cli)),
        },
    }
}

// `Cli`'s `command` field was consumed by the match above; the various
// `cmd_*` helpers only read `config`, so hand them a cheap clone rather than
// restructuring every helper to take `&Option<PathBuf>` directly.
fn cli_with_no_command(cli: &Cli) -> Cli {
    Cli {
        config: cli.config.clone(),
        command: None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    // `SC_DATA_DIR`/`SC_MASTER_KEY_FILE` are process-global; serialize every
    // test that sets them so two tests running in parallel in this binary
    // can't clobber each other's env var (same convention as
    // `masterkey::tests::ENV_LOCK`).
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    #[test]
    fn cli_parses_default_to_serve() {
        let cli = Cli::parse_from(["sc-server"]);
        assert!(cli.command.is_none());
    }

    #[test]
    fn cli_parses_masterkey_rotate() {
        let cli = Cli::parse_from(["sc-server", "masterkey", "rotate"]);
        assert!(matches!(
            cli.command,
            Some(Command::Masterkey {
                action: MasterkeyAction::Rotate
            })
        ));
    }

    #[test]
    fn cli_parses_routes_json_flag() {
        let cli = Cli::parse_from(["sc-server", "routes", "--json"]);
        match cli.command {
            Some(Command::Routes { json }) => assert!(json),
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn cli_parses_smb_sync() {
        let cli = Cli::parse_from(["sc-server", "smb-sync"]);
        assert!(matches!(cli.command, Some(Command::SmbSync)));
    }

    #[test]
    fn gc_runs_against_a_fresh_db() {
        let _g = ENV_LOCK.lock().unwrap();
        let dir = tempfile::tempdir().unwrap();
        let cli = Cli {
            config: None,
            command: None,
        };
        // Point at a temp data dir via env so `bootstrap` doesn't touch
        // `/var/lib/sc` on the test machine.
        std::env::set_var("SC_DATA_DIR", dir.path());
        std::env::set_var("SC_MASTER_KEY_FILE", dir.path().join("master.key"));
        let result = cmd_gc(&cli);
        std::env::remove_var("SC_DATA_DIR");
        std::env::remove_var("SC_MASTER_KEY_FILE");
        result.unwrap();
    }

    /// End-to-end proof for at the CLI layer itself (as
    /// opposed to `sc_auth::AuthService::rotate_master_key` directly, which
    /// `sc-auth`'s own tests already cover): a real account created through
    /// `bootstrap`'s config/key-file plumbing survives `cmd_masterkey_rotate`
    /// end to end — the key file on disk actually changes, and the account's
    /// SMB secret still authenticates through a fresh `AuthService` opened
    /// with the new key file's contents.
    #[test]
    fn masterkey_rotate_swaps_the_key_file_and_the_account_still_authenticates() {
        let _g = ENV_LOCK.lock().unwrap();
        let dir = tempfile::tempdir().unwrap();
        let key_file = dir.path().join("master.key");
        std::env::set_var("SC_DATA_DIR", dir.path());
        std::env::set_var("SC_MASTER_KEY_FILE", &key_file);

        let cli = Cli {
            config: None,
            command: None,
        };

        // First run: generates the key file and one account with an SMB
        // secret to rotate.
        let (cfg, key) = bootstrap(&cli).unwrap();
        let auth = sc_auth::AuthService::new(
            &cfg.data_dir.join("auth.db"),
            sc_auth::AuthConfig::default(),
            key.key,
        )
        .unwrap();
        let alice = auth
            .create_user("alice", &secrecy::SecretString::from("alicepassword1".to_string()))
            .unwrap();
        assert!(auth.nt_hash_present(alice).unwrap());
        drop(auth);
        let old_key = key.key;

        cmd_masterkey_rotate(&cli).unwrap();

        let new_key = masterkey::load_existing(&cfg).unwrap();
        assert_ne!(new_key, old_key, "the key file must actually change on disk");

        let auth2 = sc_auth::AuthService::new(&cfg.data_dir.join("auth.db"), sc_auth::AuthConfig::default(), new_key).unwrap();
        let smbpasswd = auth2.export_smbpasswd(1000).unwrap();
        assert!(
            smbpasswd.contains("alice"),
            "alice's SMB secret must still decrypt (and thus export) under the rotated key: {smbpasswd:?}"
        );

        // The pre-rotation key opens the database but can no longer read what
        // was re-sealed: an unreadable SMB secret warns rather than refusing,
        // because taking the whole server down over one Samba credential is
        // worse than losing SMB for that account (it downed production once).
        // Only TOTP — a second factor that would silently stop verifying —
        // still refuses. So assert what actually matters: the old key cannot
        // produce alice's secret any more.
        let stale = sc_auth::AuthService::new(&cfg.data_dir.join("auth.db"), sc_auth::AuthConfig::default(), old_key)
            .expect("an unreadable SMB secret alone must not stop startup");
        assert!(
            !stale.export_smbpasswd(1000).unwrap().contains("alice"),
            "the pre-rotation key must not still decrypt alice's re-sealed SMB secret"
        );

        std::env::remove_var("SC_DATA_DIR");
        std::env::remove_var("SC_MASTER_KEY_FILE");
    }

    #[test]
    fn cli_parses_index_build_with_share_filter() {
        let cli = Cli::parse_from(["sc-server", "index", "build", "--share", "photos"]);
        match cli.command {
            Some(Command::Index {
                action: IndexAction::Build { share },
            }) => {
                assert_eq!(share.as_deref(), Some("photos"));
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn cli_parses_index_status_without_share_filter() {
        let cli = Cli::parse_from(["sc-server", "index", "status"]);
        assert!(matches!(
            cli.command,
            Some(Command::Index {
                action: IndexAction::Status { share: None }
            })
        ));
    }

    #[test]
    fn name_index_is_disabled_by_default_and_the_gate_says_so() {
        // off by default. This is the test that
        // fails without `ensure_name_index_enabled` being wired into
        // `cmd_index` — before that, nothing in this binary ever consulted
        // `IndexConfig::name_enabled` at all (`config.rs`'s doc comment).
        //
        // `data_dir` is a temp dir, not `Config::default()`'s `/var/lib/sc`
        // — the gate now also opens `index.db` ('s admin
        // override), which needs a real writable directory.
        let dir = tempfile::tempdir().unwrap();
        let cfg = config::Config { data_dir: dir.path().to_path_buf(), ..config::Config::default() };
        assert!(!cfg.index.name_enabled);
        let err = ensure_name_index_enabled(&cfg).unwrap_err();
        assert!(err.to_string().contains("name_enabled"), "{err}");
    }

    #[test]
    fn name_index_gate_opens_once_configured_on() {
        let dir = tempfile::tempdir().unwrap();
        let mut cfg = config::Config { data_dir: dir.path().to_path_buf(), ..config::Config::default() };
        cfg.index.name_enabled = true;
        assert!(ensure_name_index_enabled(&cfg).is_ok());
    }

    #[test]
    fn name_index_gate_consults_the_admin_override_not_just_config_toml() {
        // An admin who flipped the toggle on via `PATCH /api/admin/index/
        // settings` expects the CLI to agree without touching config.toml
        // — and the reverse, an explicit off-override,
        // must not be masked by a `true` in the config file either.
        let dir = tempfile::tempdir().unwrap();
        let cfg = config::Config { data_dir: dir.path().to_path_buf(), ..config::Config::default() };
        assert!(!cfg.index.name_enabled);

        sc_search::IndexSettingsStore::open(&cfg.data_dir.join("index.db"), false)
            .unwrap()
            .set_name_enabled(true)
            .unwrap();
        assert!(ensure_name_index_enabled(&cfg).is_ok());
    }

    /// Builds an `App` directly over a temp data dir and one temp share, the
    /// same way `tests/first_run.rs` does — no config file, no env vars, no
    /// dependence on `bootstrap`'s search path.
    fn index_test_app() -> (app::App, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join("share")).unwrap();
        let cfg = config::Config {
            data_dir: dir.path().join("data"),
            shares: vec![config::ShareBootstrap {
                name: "root".into(),
                host_path: dir.path().join("share"),
                shared_externally: false,
            }],
            index: config::IndexConfig {
                name_enabled: true,
                content_enabled: false,
            },
            ..config::Config::default()
        };
        let key = masterkey::MasterKeyResult {
            key: [3u8; 32],
            inside_data_dir: false,
            generated: true,
        };
        let app = app::App::build(cfg, &key).expect("app builds");
        (app, dir)
    }

    #[test]
    fn index_build_then_status_report_the_same_share() {
        let (app, dir) = index_test_app();
        std::fs::write(dir.path().join("share/hello.txt"), b"hi").unwrap();

        // Before a build: `status` must say so, not error and not fabricate
        // numbers.
        run_index_action(&app, IndexAction::Status { share: None }).unwrap();
        assert!(bridge::open_existing_name_index(&app.core, sc_vfs::ShareId::new(1)).is_none());

        run_index_action(&app, IndexAction::Build { share: None }).unwrap();
        let idx = bridge::open_existing_name_index(&app.core, sc_vfs::ShareId::new(1))
            .expect("build must leave a queryable index behind");
        assert_eq!(idx.stats().entries, 1);

        // `status`/`merge` on the now-indexed share must not error either.
        run_index_action(&app, IndexAction::Status { share: None }).unwrap();
        run_index_action(&app, IndexAction::Merge { share: None }).unwrap();
    }

    #[test]
    fn index_build_with_an_unknown_share_name_errors_instead_of_silently_doing_nothing() {
        let (app, _dir) = index_test_app();
        let err = run_index_action(
            &app,
            IndexAction::Build {
                share: Some("nope".into()),
            },
        )
        .unwrap_err();
        assert!(err.to_string().contains("nope"), "{err}");
    }
}
