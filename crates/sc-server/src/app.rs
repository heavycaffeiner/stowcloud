//! Application assembly: turn a [`Config`] into every long-lived object the
//! server needs, and hand back the composed `axum::Router`.
//!
//! This is the only place in the workspace where the concrete crates meet.
//! Everything below it is a library that knows nothing about the others; the
//! wiring order here — storage, then ACL, then domain, then protocols — is
//! 's dependency direction made executable.

use std::sync::Arc;

use axum::response::IntoResponse;
use axum::Router;
use sc_vfs::{ShareId, UserId};

use crate::bridge::{CoreBridge, MetaBridge, UploadBridge};
use crate::config::Config;
use crate::masterkey::MasterKeyResult;

pub struct App {
    pub cfg: Config,
    pub meta: Arc<sc_meta::MetaStore>,
    pub acl: Arc<sc_acl::AclEngine>,
    pub core: Arc<sc_core::Core>,
    pub auth: Arc<sc_auth::AuthService>,
    /// First-run administrator bootstrap. Held as the
    /// concrete type so `cmd_serve` can arm it — building an `App` never
    /// issues a token, only starting to serve does. `http.setup` is the same
    /// object behind `sc_http`'s trait.
    pub setup: Arc<crate::setup::SetupGate>,
    /// Held as the concrete type for the same reason `setup` is: the passdb
    /// publisher renders through it (`passdb.rs`), and `http.settings` is the
    /// same object behind `sc_http`'s trait.
    pub settings: Arc<crate::settings_bridge::SettingsBridge>,
    /// The live `smbpasswd` publisher. Empty until
    /// [`App::arm_passdb_publisher`] fills it, and left empty for a
    /// deployment with `smb.enabled = false`: see that method for why the
    /// CLI paths never arm it.
    pub passdb: std::sync::OnceLock<crate::passdb::PassdbPublisher>,
    pub uploads: Arc<sc_upload::UploadEngine>,
    pub dav: Arc<sc_dav::DavService>,
    pub http: sc_http::AppState,
    /// Kept alive for as long as the server runs; dropping it stops watching.
    /// Shared (not owned outright) because `CoreBridge` and `AclReadCheck`
    /// also hold a handle — `fs_list`/`fs_stat` LRU-touch it, and WebSocket
    /// subscribe/unsubscribe register/release its OS-level watches.
    pub watcher: Option<Arc<sc_watch::Watcher>>,
    /// Periodic upload-GC sweep thread. `shutdown.rs`
    /// stops it before calling `UploadApi::drain()` so the two never race on
    /// the same `UploadEngine::gc()` call.
    pub upload_gc: UploadGcHandle,
    /// Periodic idle-triggered name-index segment merge.
    /// `shutdown.rs` stops it at shutdown, same reasoning as `upload_gc`.
    pub idle_merge: IndexMergeHandle,
    #[cfg(feature = "compat-nc")]
    pub compat: Option<crate::nc::Compat>,
}

impl App {
    pub fn build(cfg: Config, key: &MasterKeyResult) -> anyhow::Result<Self> {
        let data_dir = cfg.data_dir.clone();
        std::fs::create_dir_all(&data_dir)?;

        // ---- storage layer ----
        let meta = Arc::new(sc_meta::MetaStore::open(&data_dir.join("meta.db"))?);
        crate::bridge::DB_BYTES.store(
            meta.size_bytes().unwrap_or(0),
            std::sync::atomic::Ordering::Relaxed,
        );

        let auth = Arc::new(sc_auth::AuthService::new(
            &data_dir.join("auth.db"),
            sc_auth::AuthConfig {
                // The one `[oidc]` key `sc-auth` acts on by itself: whether a
                // linked account may still open the web UI with its local
                // password (§4.3.5). Read from `config.toml` only -- see
                // `OidcOverride`'s doc comment for why the settings screen
                // must never be able to set it.
                oidc_local_password_login: cfg.oidc.local_password_login.into(),
                ..sc_auth::AuthConfig::default()
            },
            key.key,
        )?);

        // ---- domain layer ----
        let acl = Arc::new(sc_acl::AclEngine::new());
        let core = Arc::new(sc_core::Core::new(meta.clone(), acl.clone()));
        // Share links live in their own database, not in `meta.db`: that one
        // is documented as a disposable cache and
        // links are not reconstructible from the filesystem. Same Argon2
        // parameters as account passwords — `sc-auth` owns those numbers.
        core.attach_links(sc_core::LinkStore::open_with_config(
            &data_dir.join("links.db"),
            sc_auth::AuthConfig::default(),
        )?)?;
        // Grants live in their own database too, for exactly the reason
        // links do (`sc_core::acl_store`'s module doc): `meta.db` is a
        // disposable cache, and a grant — "user 7
        // may read `/vacation` but not `/vacation/private`" — cannot be
        // reconstructed from anything on the filesystem.
        core.attach_acl_store(sc_core::AclStore::open(&data_dir.join("acl.db"))?)?;
        // Admin-created shares live in their own
        // database, same reasoning as grants/links above — see
        // `sc_core::share`'s module doc for the id-range split from
        // `config.toml`-derived shares. Attached before `register_shares`
        // (not after, as before) so a config-file share's persisted trash
        // override (`share_trash_override`, keyed by its real `ShareId`) is
        // already readable when that share is first registered below.
        core.attach_share_store(sc_core::ShareStore::open(&data_dir.join("shares.db"))?)?;
        // Quota enforcement — backed by `sc-auth`'s
        // ledger, not a new store of its own; see `bridge::AuthQuotaSink`.
        core.attach_quota_sink(Arc::new(crate::bridge::AuthQuotaSink {
            auth: auth.clone(),
        }))?;
        register_shares(&core, &cfg)?;
        core.load_persisted_shares()?;
        // Per-user homes -- off by default. Not fatal on
        // failure, same tolerance `register_shares` already has for a single
        // rejected config-file share: a misconfigured `homes.root` should not
        // take down a server that otherwise has nothing to do with homes.
        if cfg.homes.enabled {
            if let Err(e) = core.attach_homes(&cfg.homes_root()) {
                tracing::error!(error = %e, "homes enabled but failed to start; continuing without per-user homes");
            }
        }
        project_grants(&core, &acl, &auth)?;

        // Constructed, not armed: no token is minted, printed or written here,
        // so `gc`, `smb-sync` and the test suite can build an `App` without a
        // fresh admin-creation credential appearing on disk as a side effect.
        // `cmd_serve` arms it.
        //
        // It is built *after* the domain layer because creating the first
        // account has to re-seed grants for it. See `SetupGate::complete`
        // step (7): a brand-new deployment's bootstrap administrator gets an
        // explicit one-time full-access seed
        // (`sc_core::Core::seed_full_access`) rather than relying on
        // `project_grants` alone, which by design does *not* hand out access
        // to an account it has never seen before (`acl_store`'s module doc —
        // "no access" is the point, not a bug to work around here).
        let setup = Arc::new(crate::setup::SetupGate::new(
            auth.clone(),
            core.clone(),
            acl.clone(),
            &data_dir,
        ));

        let uploads = Arc::new(sc_upload::UploadEngine::new(
            &data_dir.join("upload.db"),
            upload_config(&cfg),
        )?);

        // ---- change detection ----
        // Built before `core_bridge` and the WS hub, not after: both need a
        // handle to the same `Watcher` (`fs_list`/`fs_stat` LRU-touch it via
        // `CoreBridge`; subscribe/unsubscribe register/release its OS-level
        // watches via `AclReadCheck`), and a watcher that fails to start is
        // not fatal — every read path re-stats before trusting anything, so
        // the only cost is that an external change is noticed on next access
        // rather than pushed.
        let (watcher, ws_hub) = start_watcher_and_ws_hub(&cfg, core.clone());

        // Admin override for `[index] name_enabled`
        // — its own tiny DB file, not a `config.toml` rewrite; see
        // `sc_search::settings` module doc for the reasoning.
        let index_settings = Arc::new(sc_search::IndexSettingsStore::open(
            &data_dir.join("index.db"),
            cfg.index.name_enabled,
        )?);

        // Server-settings admin screen's persisted overrides — already
        // folded into `cfg` once by `bootstrap()`'s
        // `apply_settings_overrides` before this function ever saw it; this
        // second open is so `SettingsBridge` can write new ones back without
        // going through `bootstrap()` again.
        let settings_store = Arc::new(crate::settings_store::SettingsStore::open(
            &data_dir.join("settings.db"),
        )?);
        let restart_signal = Arc::new(tokio::sync::Notify::new());

        let core_bridge = Arc::new(CoreBridge::new(
            core.clone(),
            cfg.smb.enabled,
            watcher.clone(),
            index_settings.clone(),
        ));
        let idle_merge = spawn_idle_merge(core_bridge.clone());

        // Decoders are the highest-risk code in the server: they are the one
        // place attacker-controlled bytes meet a parser. On Linux they therefore do not run here at all — they run in
        // forked worker processes with no filesystem (Landlock), no network,
        // no ability to spawn anything, and a twenty-two-syscall seccomp
        // allow-list, reached only through `SCM_RIGHTS` fd passing.
        //
        // Forking happens now, during startup, deliberately: the workers are
        // `fork`s without `exec`, so the earlier and quieter the process is
        // when they are created, the smaller the classic
        // allocator-lock-across-fork window (see `sc_preview::worker::jailed`).
        //
        // A failure here is fatal rather than a silent downgrade to the
        // in-process pool. Quietly running decoders unsandboxed while the
        // docs promise a jail is worse than not starting.
        let preview_pool: Arc<dyn sc_preview::WorkerPool> = preview_worker_pool()?;
        let preview = Arc::new(sc_preview::PreviewService::new(
            sc_preview::PreviewConfig::default(),
            preview_pool,
        ));
        let content_bridge: Arc<dyn sc_http::content_api::ContentApi> =
            Arc::new(crate::bridge::ContentBridge {
                core: core.clone(),
                preview,
            });
        // Shared between `SearchBridge` (picks the
        // walk deadline) and `AppState` (acquires the concurrency permit) so
        // the two can never disagree about which tier's numbers apply to a
        // given search — see `SearchBridge::limits`.
        let search_concurrency = Arc::new(sc_http::search_limits::SearchConcurrency::new(
            &(&cfg.search).into(),
        ));
        let search_bridge: Arc<dyn sc_http::search_api::SearchApi> =
            Arc::new(crate::bridge::SearchBridge {
                core: core.clone(),
                storage_cache: crate::storage_class::StorageClassCache::default(),
                limits: search_concurrency.clone(),
            });
        // Built here rather than inline in `build_http_state`, so
        // `SettingsBridge` (below) can reconfigure the very same rate
        // limiter a `[search]` patch is meant to affect, instead of each
        // holding its own disconnected copy.
        let search_rate = Arc::new(sc_http::rate_limit::KeyedTokenBucket::new(
            cfg.search.rate_per_minute,
            std::time::Duration::from_secs(60),
        ));
        // 's rate-limit shape, config-reachable via
        // `[archive]`; resizable so a live `archive.max_concurrent` change
        // from the settings screen takes effect without a restart.
        let archive_concurrency = Arc::new(sc_http::state::ResizableSemaphore::new(
            cfg.archive.max_concurrent.max(1) as usize,
        ));

        // Built as the concrete type and only then coerced: the passdb
        // publisher needs a `Weak` to *this* object, and a `Weak<dyn
        // SettingsApi>` cannot be turned back into the renderer it also is.
        let settings_bridge = Arc::new(crate::settings_bridge::SettingsBridge::new(
            settings_store,
            cfg.clone(),
            search_concurrency.clone(),
            search_rate.clone(),
            archive_concurrency.clone(),
            core.clone(),
            auth.clone(),
            restart_signal.clone(),
        ));
        let settings: Arc<dyn sc_http::settings_api::SettingsApi> = settings_bridge.clone();

        // Never fails: a `[oidc]` section this server cannot use costs single
        // sign-on and says why in the startup log, and everything else comes
        // up exactly as before (§4.3.1).
        let oidc = crate::oidc::build_oidc(&cfg);

        let http = build_http_state(
            &cfg,
            auth.clone(),
            setup.clone(),
            core_bridge.clone(),
            core.clone(),
            uploads.clone(),
            content_bridge,
            search_bridge,
            search_concurrency,
            search_rate,
            archive_concurrency,
            settings,
            restart_signal,
            ws_hub,
            oidc,
        )?;

        // The compatibility layer is assembled before the DAV service is
        // shared, because its property decoration has to be registered on
        // that service and `add_prop_source` needs `&mut`.
        //
        // `canonical_url` used to be `format!("https://{}", app_hosts.first())`
        // unconditionally — whichever host happened to be listed first, with
        // no say from the operator and no warning. That value is handed to a
        // real device's system browser (Login Flow v2's `login` URL) and the
        // device binds to it forever afterward; reordering `app_hosts` for an
        // unrelated reason (adding a bind alias, alphabetising) would silently
        // repoint every future client enrolment at a different origin. See
        // `Config::resolve_compat_canonical_url`'s doc comment for the full
        // reasoning and `diagnostics.rs` for the startup report.
        #[cfg(feature = "compat-nc")]
        let compat = match cfg.resolve_compat_canonical_url() {
            crate::config::CompatCanonicalUrl::Configured(url)
            | crate::config::CompatCanonicalUrl::Derived(url) => {
                Some(crate::nc::Compat::build(crate::nc::CompatBuildInputs {
                    data_dir: &data_dir,
                    canonical_url: url,
                    chunk_size_advisory: cfg.upload.chunk_default_bytes,
                    core: core.clone(),
                    meta: meta.clone(),
                    auth: auth.clone(),
                    uploads: uploads.clone(),
                    content_host: http.cfg.content_hosts.first().cloned().unwrap_or_default(),
                    keys: http.signed_url_keys.clone(),
                }))
            }
            crate::config::CompatCanonicalUrl::Ambiguous { app_host_count } => {
                tracing::error!(
                    app_host_count,
                    "compat layer NOT mounted: `compat_canonical_url` is unset \
                     and `app_hosts` has {app_host_count} entries, so there is no single \
                     unambiguous origin to hand a real client's system browser. Set \
                     `compat_canonical_url` explicitly in the config file to enable legacy \
                     client compatibility (native web UI, WebDAV and the plain API are \
                     unaffected)."
                );
                None
            }
            crate::config::CompatCanonicalUrl::Invalid(value) => {
                tracing::error!(
                    value = %value,
                    "compat layer NOT mounted: `compat_canonical_url` is set \
                     but is not an absolute http(s):// origin"
                );
                None
            }
        };

        // ---- protocol layer ----
        let dav = {
            let store = sc_dav::locks::SqliteLockStore::open(&data_dir.join("dav-locks.db"))?;
            #[allow(unused_mut)]
            let mut svc = sc_dav::DavService::with_lock_store(
                core_bridge.clone(),
                Arc::new(MetaBridge { meta: meta.clone() }),
                sc_dav::DavConfig::default(),
                Arc::new(store),
            );
            #[cfg(feature = "compat-nc")]
            if let Some(c) = compat.as_ref() {
                svc.add_prop_source(c.prop_source());
            }
            Arc::new(svc)
        };

        // `UploadEngine::gc` existed, was fully
        // tested, and had exactly one caller in the whole workspace:
        // `UploadBridge::drain()`, itself only ever invoked once, at clean
        // shutdown (`shutdown.rs::run_shutdown_sequence`). Nothing called it
        // while the server kept running, so an aborted session's `.scpart`
        // or a crash's orphaned part file sat on disk until an operator
        // noticed and cleaned it by hand — on the 32 GB system-SSD budget
        // this deployment is sized to, that is a
        // disk-fill bug, not just clutter. This is that missing caller.
        let upload_gc = spawn_upload_gc(uploads.clone(), core.clone());

        Ok(App {
            cfg,
            meta,
            acl,
            core,
            auth,
            setup,
            settings: settings_bridge,
            passdb: std::sync::OnceLock::new(),
            uploads,
            dav,
            http,
            watcher,
            upload_gc,
            idle_merge,
            #[cfg(feature = "compat-nc")]
            compat,
        })
    }

    /// Installs the `sc-auth` passdb sink and starts the thread behind it, so
    /// that an NT-hash change rewrites the published `smbpasswd` by itself
    /// (proposal §4.3.6 step 2).
    ///
    /// Armed by `cmd_serve` rather than by [`App::build`], for the same
    /// reason `setup` is: `gc` and `smb-sync` build the same `App`, and
    /// neither should acquire a background publisher. `smb-sync` in
    /// particular renders explicitly and would otherwise race its own
    /// publisher over the same files.
    ///
    /// **A deployment with SMB off gets no thread and no sink at all.**
    /// Turning `smb.enabled` on from the settings screen already answers
    /// `restart_required` (the flag is baked into `CoreBridge` at boot), and
    /// that restart is what arms this.
    ///
    /// Best effort by construction: a sink that fails to install costs the
    /// live republish, which is the behaviour every build before this one
    /// had, so it is a loud log line and not a startup failure.
    pub fn arm_passdb_publisher(&self) {
        if !self.cfg.smb.enabled {
            tracing::debug!(
                "smb is disabled: no passdb publisher started (enabling smb requires a restart, \
                 which is what arms it)"
            );
            return;
        }
        let publisher = crate::passdb::PassdbPublisher::start(
            Arc::downgrade(&self.settings) as std::sync::Weak<dyn crate::passdb::PassdbRender>
        );
        if !self.auth.set_passdb_sink(publisher.sink()) {
            tracing::error!(
                "a passdb sink was already installed: NT hash changes will keep being published \
                 by whichever sink got there first"
            );
            return;
        }
        let _ = self.passdb.set(publisher);
        tracing::info!(
            "passdb publisher armed: an NT hash change now rewrites smbpasswd without `smb-sync`"
        );
    }

    /// The composed router: native REST + WebSocket, WebDAV, the handful of
    /// endpoints `sc-server` itself owns, and — only when compiled with
    /// `feature = "compat-nc"` — the compatibility layer.
    pub fn router(&self) -> Router {
        // Only reassigned under `feature = "compat-nc"` below — same
        // `--no-default-features` wrinkle `dav`'s own `#[allow(unused_mut)]`
        // documents elsewhere in this file.
        #[allow(unused_mut)]
        let mut app = crate::routes::server_routes()
            .merge(sc_http::build_router(self.http.clone()))
            .merge(self.dav_router())
            // Outside the `compat-nc` gate on purpose: surviving
            // `--no-default-features` is the entire point of this mount
            // existing beside the compatibility layer's own chunked surface.
            // It needs `with_dav_auth` for the same reason the compat mount
            // does — no `DavPrincipal`, no way to scope a `{tid}` to a user.
            .merge(self.with_dav_auth(crate::dav_uploads::router(
                self.core.clone(),
                self.uploads.clone(),
                &self.dav,
            )));

        #[cfg(feature = "compat-nc")]
        {
            // The compat mount serves WebDAV too (`/remote.php/dav/...`) and
            // reaches the same `sc-dav` handlers, so it needs the same
            // authenticator. Without it no `DavPrincipal` is ever established
            // and `sc-dav` rejects every request — which fails closed, but
            // leaves the entire compat surface unusable by any real client.
            app = app.merge(self.with_dav_auth(crate::nc::router(self)));
        }

        // Who the client *is* is decided once, here, outside every mount.
        //
        // `sc_http::build_router` contains this same layer as documented step
        // 2 of its own stack, but that only covers the routes it builds — the
        // WebDAV tree and the compatibility mount are merged in beside it and
        // would otherwise never see a `ClientIp` at all, which is how they
        // ended up with a hardcoded loopback address instead. Layering it
        // once out here means there is one implementation of the
        // trusted-proxy rule (`sc_http::middleware::resolve_client_ip`) and
        // one place it is applied; a mount added below inherits it by
        // construction rather than by remembering to. The inner copy sees the
        // extension already set and steps aside — see `trusted_proxy`'s doc.
        //
        // It must be the outermost layer on this router: `RateLimit`, `Auth`,
        // `dav_authenticate` and the audit sink all read what it publishes.
        //
        // `request_trace_layer()` sits just inside it (added first, so it
        // ends up nested one level in — axum's "last `.layer()` call is
        // outermost" rule) precisely so its `make_span_with` closure runs
        // *after* `trusted_proxy` has already resolved and inserted
        // `ClientIp`: reading the raw socket peer here would log every
        // phone behind the reverse proxy as the proxy's own address.
        app.layer(request_trace_layer())
            .layer(axum::middleware::from_fn_with_state(
                self.http.clone(),
                sc_http::middleware::trusted_proxy,
            ))
    }

    /// `sc-dav` expects whatever sits in front of it to have authenticated the
    /// caller and left a `DavPrincipal` in the request extensions; it
    /// deliberately owns no credential logic of its own.
    fn dav_router(&self) -> Router {
        self.with_dav_auth(self.dav.clone().router())
    }

    /// Attach the DAV authenticator to a router. Safe to apply to routes that
    /// need no authentication: with neither an `Authorization` header nor a
    /// session cookie the middleware falls straight through, so `status.php`
    /// and the login-flow endpoints pay nothing — in particular, no Argon2.
    fn with_dav_auth(&self, router: Router) -> Router {
        let auth = self.auth.clone();
        let core = self.core.clone();
        let dav_shaped = DavShapedPaths::new(&self.dav);
        router.layer(axum::middleware::from_fn(
            move |req: axum::extract::Request, next: axum::middleware::Next| {
                let auth = auth.clone();
                let core = core.clone();
                let dav_shaped = dav_shaped.clone();
                async move { dav_authenticate(auth, core, dav_shaped, req, next).await }
            },
        ))
    }
}

/// Production request logging (`tower_http::trace::TraceLayer`).
///
/// Before this, nothing in the workspace emitted a per-request line at all —
/// `main.rs` sets up `tracing_subscriber` and that's it. `.demo/serve.log`
/// held startup diagnostics and then silence, forever. That cost real
/// debugging time on a compat mobile-app enrollment failure: with no
/// record of what the phone asked for, the only way to find the actual
/// cause (an old app version sending a request shape the server didn't
/// expect) was to open a copy of the production DB and guess. A single
/// logged `User-Agent` would have shown it in the first minute.
///
/// One line per request, `tracing::info!`, target `sc_server::access`:
/// `method`, `path` (with any credential-bearing segment redacted — see
/// `redact_token_path`), `client_ip` (the trusted-proxy-resolved address,
/// never the raw socket peer — see `router()`'s layering comment), `status`,
/// `latency_ms`, and `user_agent`. `user` is attached to the span as
/// `tracing::field::Empty` and only shows up in the rendered line when
/// something downstream calls `.record()` on it — `dav_authenticate`
/// (below) does, for every request through the native DAV mount and the
/// whole compat surface (which is what the enrollment
/// failure above actually was). The native JSON REST API's own auth
/// middleware (`sc_http::middleware::auth`) does not yet do the same, so
/// REST access lines carry no `user`.
///
/// **What is deliberately never logged, by construction**: this closure
/// only ever reads `req.method()`, `req.uri().path()` (never `.query()`),
/// the resolved `ClientIp` extension, and the `User-Agent` header. It never
/// touches `Authorization`, `Cookie`, `Sc-Csrf`, `Set-Cookie`, or the
/// request/response body — none of those are reachable from anything this
/// function does, not merely "filtered out" after the fact.
///
/// INFO, not DEBUG: the whole point is a production deployment that never
/// set `RUST_LOG` still gets this. `RUST_LOG=sc_server::access=warn` (or
/// `=off`) silences it; `=debug` doesn't add anything today since nothing
/// downstream records extra fields at that level yet.
// The four type parameters are each one specific closure/unit type inferred
// from the builder chain below; naming them is what `TraceLayer`'s own
// builder API requires (each `.on_*`/`.make_span_with` call changes the
// type), not a factoring omission — there is only one call site.
#[allow(clippy::type_complexity)]
fn request_trace_layer() -> tower_http::trace::TraceLayer<
    tower_http::classify::SharedClassifier<tower_http::classify::ServerErrorsAsFailures>,
    impl Fn(&axum::extract::Request) -> tracing::Span + Clone,
    (),
    impl Fn(&axum::response::Response, std::time::Duration, &tracing::Span) + Clone,
> {
    use tower_http::trace::TraceLayer;
    TraceLayer::new_for_http()
        .make_span_with(|req: &axum::extract::Request| {
            let client_ip = req
                .extensions()
                .get::<sc_http::state::ClientIp>()
                .map(|c| c.0.to_string())
                .unwrap_or_else(|| "-".to_string());
            let user_agent = req
                .headers()
                .get(axum::http::header::USER_AGENT)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("-")
                .to_string();
            tracing::info_span!(
                target: "sc_server::access",
                "http_request",
                method = %req.method(),
                path = %redact_token_path(req.uri().path()),
                client_ip = %client_ip,
                user_agent = %user_agent,
                user = tracing::field::Empty,
            )
        })
        // The default `on_request` logs a second "started processing"
        // line at DEBUG; one line per request is the spec here, so it's
        // silenced (`()` is a no-op `OnRequest` impl) rather than left to
        // double up under `RUST_LOG=debug`.
        .on_request(())
        .on_response(
            |resp: &axum::response::Response,
             latency: std::time::Duration,
             span: &tracing::Span| {
                tracing::info!(
                    target: "sc_server::access",
                    parent: span,
                    status = resp.status().as_u16(),
                    latency_ms = latency.as_millis() as u64,
                    "request"
                );
            },
        )
}

/// Replace a credential-bearing path segment with a short, non-reversible
/// correlation tag before it ever reaches a log line.
///
/// `/c/{token}` (signed content URL) and `/s/{token}`, `/s/{token}/auth`,
/// `/s/{token}/download`, `/s/{token}/drop` (share-link URLs,
/// `sc_http::content`/`routes.rs`) and `/index.php/login/v2/flow/{token}`
/// (Login Flow v2 flow token, `sc_compat_nc::login_flow`) each carry a
/// *working bearer credential* in the path itself — logging the path
/// verbatim would put that credential in a plaintext file
/// (`.demo/serve.log`, not access-controlled the way the session/auth DBs
/// are). `token=` in the Login Flow v2 poll request is a POST body field,
/// not a path or query parameter, so it never reaches this function at all
/// — nothing here reads a body.
///
/// The replacement is 8 hex characters of a BLAKE3 hash, not the token and
/// not reversible to it, kept only so several log lines from the same flow
/// can still be `grep`-correlated by an operator who does not have the
/// token.
fn redact_token_path(path: &str) -> String {
    fn tag(token: &str) -> String {
        format!("redacted:{}", &blake3::hash(token.as_bytes()).to_hex()[..8])
    }
    let segs: Vec<&str> = path.split('/').collect();
    if segs.len() >= 3
        && segs.first() == Some(&"")
        && matches!(segs.get(1), Some(&"c") | Some(&"s"))
    {
        if let Some(token) = segs.get(2).filter(|t| !t.is_empty()) {
            let t = tag(token);
            let mut out = segs;
            out[2] = &t;
            return out.join("/");
        }
    }
    if path.starts_with("/index.php/login/v2/flow/") {
        if let Some(token) = segs.get(5).filter(|t| !t.is_empty()) {
            let t = tag(token);
            let mut out = segs;
            out[5] = &t;
            return out.join("/");
        }
    }
    path.to_string()
}

/// Which incoming URL paths this gate treats as "a WebDAV method against a
/// file" for the purpose of mapping the HTTP method to an `sc_acl::Perms`
/// requirement (`dav_required_perms`) — as opposed to the compatibility
/// layer's other surfaces (`status.php`, OCS share management, Login Flow
/// v2), which speak ordinary `GET`/`POST`/`PUT`/`DELETE` for entirely
/// different things and have no such mapping.
///
/// Deliberately *not* threaded through `sc_dav::DavService` or
/// `sc_compat_nc`: this is a URL-space fact about how `App::router` wires
/// mounts together (`with_dav_auth` wraps both `dav_router()` and, feature-
/// gated, the whole compat router beside it), not a protocol concept either
/// of those crates should know about. The literal `remote.php`/`webdav`
/// strings live here — `sc-server` is the one crate the isolation gate does
/// not scan for that vocabulary (`build_http_state`'s
/// `reserved_path_prefixes`, same rationale, same crate).
#[derive(Clone)]
struct DavShapedPaths {
    /// The native mount's configured prefix (default `/dav`), read once at
    /// wiring time rather than re-derived per request.
    native_prefix: Arc<str>,
}

impl DavShapedPaths {
    fn new(dav: &Arc<sc_dav::DavService>) -> Self {
        Self {
            native_prefix: Arc::from(dav.config().prefix.trim_end_matches('/')),
        }
    }

    fn matches(&self, path: &str) -> bool {
        let native = self.native_prefix.as_ref();
        // An empty prefix means the native mount is configured at bare `/`
        // (`DavConfig::prefix` trimmed of its trailing slash) — every path
        // is under it, not none of them.
        if native.is_empty() || path == native || path.starts_with(&format!("{native}/")) {
            return true;
        }
        // The native session-upload surface (`dav_uploads.rs`) speaks MKCOL /
        // PUT / MOVE / DELETE / PROPFIND, so it wants the same method → perms
        // mapping every other DAV-shaped mount gets. Note what this does *not*
        // buy: `vpath_of` still answers `None` here, so a token restricted by
        // `Scope::shares` is refused on this mount outright. Resolving which
        // share a `{tid}` names would require the upload engine inside this
        // gate, which today holds only `auth` and `core`; the refusal fails
        // closed and such a token can still use whole-body `PUT` on `/dav`.
        if path == crate::dav_uploads::PREFIX
            || path.starts_with(&format!("{}/", crate::dav_uploads::PREFIX))
        {
            return true;
        }
        // Every remote.php request — file operations, chunked upload, and
        // the root/principal discovery PROPFINDs alike — is dispatched to
        // `sc-dav`'s method set by `h_remote` (`nc.rs`); OCS and Login Flow
        // v2 are mounted under different prefixes entirely and never reach
        // here (`sc_compat_nc::router`).
        #[cfg(feature = "compat-nc")]
        {
            if path.starts_with("/remote.php/") || path.starts_with("/index.php/remote.php/") {
                return true;
            }
        }
        false
    }

    /// The virtual path a DAV-shaped request names, for `sc_auth::Scope::shares`
    /// checking (`dav_authenticate`). `None` covers two different things the
    /// caller must treat identically — fail closed, never "no restriction
    /// applies":
    ///
    /// * the path is not DAV-shaped at all (`matches` already says so, and
    ///   the perms half already refuses a *scope-restricted* credential there
    ///   — this extends the same refusal to a token restricted only by
    ///   `shares`), or
    /// * it *is* DAV-shaped but names something other than a file under a
    ///   share (`sc_compat_nc::dav_paths::DavTarget`'s non-`Files` variants:
    ///   the chunked-upload session tree, the principal/root discovery
    ///   stubs) — this gate has no virtual path to resolve for those at all.
    ///
    /// The native mount and the compat `files`/`webdav` surfaces are the only
    /// two that ever resolve: both ultimately address a share by the same
    /// `/{label}/...` vocabulary `sc_core::Core::resolve_share` understands,
    /// once the mount's own prefix (and, for the compat surface, the
    /// per-user routing segment) is stripped.
    fn vpath_of(&self, uri_path: &str) -> Option<String> {
        let native = self.native_prefix.as_ref();
        if native.is_empty() || uri_path == native || uri_path.starts_with(&format!("{native}/")) {
            let rest = if native.is_empty() {
                uri_path
            } else {
                uri_path.strip_prefix(native).unwrap_or(uri_path)
            };
            return decode_dav_path(rest);
        }
        #[cfg(feature = "compat-nc")]
        {
            if uri_path.starts_with("/remote.php/")
                || uri_path.starts_with("/index.php/remote.php/")
            {
                return match sc_compat_nc::dav_paths::parse(uri_path) {
                    Some(sc_compat_nc::dav_paths::DavTarget::Files { path, .. }) => Some(path),
                    _ => None,
                };
            }
        }
        None
    }

    /// The virtual path a `MOVE`/`COPY`'s `Destination` header names, by the
    /// same rule as [`Self::vpath_of`] — clients may send it as a bare path
    /// or as an absolute URL (scheme + host + path); only the path component
    /// is ever meaningful here.
    fn destination_vpath(&self, headers: &http::HeaderMap) -> Option<String> {
        let raw = headers.get("destination")?.to_str().ok()?;
        let path = match raw.find("://") {
            Some(idx) => {
                let after_scheme = &raw[idx + 3..];
                match after_scheme.find('/') {
                    Some(i) => &after_scheme[i..],
                    None => "/",
                }
            }
            None => raw,
        };
        self.vpath_of(path)
    }
}

/// Strip a leading/trailing slash and percent-decode, rejecting anything
/// `sc_vfs::SafePath` would also reject (`.`/`..`, NUL) rather than let it
/// through and have `Core::resolve_share` fail on it in some less obvious
/// way. Deliberately the same shape as `sc_dav`'s own (private) `vpath_of` —
/// this crate cannot call that one directly, but the rule is public
/// (RFC 3986 path percent-decoding) and worth re-stating rather than
/// skipping just because the original is out of reach.
pub(crate) fn decode_dav_path(uri_path: &str) -> Option<String> {
    let rest = uri_path.trim_start_matches('/').trim_end_matches('/');
    let decoded = percent_encoding::percent_decode_str(rest)
        .decode_utf8()
        .ok()?;
    if decoded.contains('\0') {
        return None;
    }
    for seg in decoded.split('/') {
        if seg == "." || seg == ".." {
            return None;
        }
    }
    Some(decoded.into_owned())
}

/// The `sc_acl::Perms` bit(s) a WebDAV method needs from an app password's
/// scope, mirroring how `sc-core` itself gates each
/// operation (`ops.rs`: `move_to` checks `MOVE` on the source and `CREATE`
/// on the destination; `copy_to` checks `READ` then `CREATE`). `None` means
/// "no mapping" — the caller treats that as fail-closed for a
/// scope-restricted credential, same as `sc_http::middleware::RouteScope::Unmapped`.
fn dav_required_perms(method: &http::Method) -> Option<sc_acl::Perms> {
    use sc_acl::Perms;
    match method.as_str() {
        "GET" | "HEAD" | "PROPFIND" => Some(Perms::READ),
        "PUT" => Some(Perms::WRITE),
        "DELETE" => Some(Perms::DELETE),
        "MKCOL" => Some(Perms::CREATE),
        "PROPPATCH" | "LOCK" | "UNLOCK" => Some(Perms::WRITE),
        "COPY" => Some(Perms::READ | Perms::CREATE),
        "MOVE" => Some(Perms::MOVE | Perms::CREATE),
        _ => None,
    }
}

/// Basic auth (app password or account password, per) or a
/// session cookie. Failure is *not* an error here: the request continues
/// unauthenticated so `sc-dav` can answer `401` with its own
/// `WWW-Authenticate` realm, which is what makes Windows Explorer offer
/// credentials at all.
///
/// This is also the single choke point every WebDAV-shaped mount passes
/// through — native `/dav` and, feature-gated, the whole compatibility
/// router (`App::router` applies it to both via `with_dav_auth`) — so it is
/// where an app password's `sc_auth::Scope` gets enforced for both of them
/// at once (`sc_http::middleware::scope_gate` is this function's native-API
/// counterpart; see that function's doc comment for the shared design). A
/// cookie session or an account-password Basic login is always
/// `Scope::default()` (`perms_mask: None`) by construction in `sc-auth`, so
/// only `AuthVia::AppPassword` is ever narrowed here.
///
/// What this cannot cover: the compatibility layer's non-DAV surfaces
/// (OCS share management, Login Flow v2) have no per-method `Perms` mapping,
/// nor any virtual path to check `Scope::shares` against — see
/// `DavShapedPaths`'s doc comment — so a credential restricted by either
/// dimension is refused there outright rather than guessed at, while an
/// unrestricted one keeps working exactly as before. `Scope::shares` itself
/// is checked via `sc_core::Core::resolve_share`, which — unlike the
/// `perms_mask` half — needs no protocol-agnostic trait boundary to reach:
/// this function already holds the real `Core` (`App::with_dav_auth` passes
/// it straight through), so there is no `sc_dav`/`sc_compat_nc` involvement
/// at all in checking it, and no `Perms::READ` assumption either (see
/// `resolve_share`'s own doc comment).
async fn dav_authenticate(
    auth: Arc<sc_auth::AuthService>,
    core: Arc<sc_core::Core>,
    dav_shaped: DavShapedPaths,
    mut req: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    let headers = req.headers().clone();
    // The address the trusted-proxy layer resolved for this request
    // (`App::router` applies it in front of this mount). Never the socket
    // peer read again here, and never a constant: `verify_basic` feeds this
    // straight into `sc-auth`'s per-IP brute-force gate and its audit rows,
    // and a constant collapses every DAV client in the world into one bucket.
    let peer = req
        .extensions()
        .get::<sc_http::state::ClientIp>()
        .map(|c| c.0)
        .unwrap_or(sc_http::middleware::UNKNOWN_CLIENT);

    let mut principal: Option<sc_auth::Principal> = None;

    if let Some(v) = headers
        .get(http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Basic "))
    {
        if let Ok(raw) = data_encoding::BASE64.decode(v.trim().as_bytes()) {
            if let Ok(txt) = String::from_utf8(raw) {
                if let Some((u, pw)) = txt.split_once(':') {
                    if let sc_auth::BasicResult::Ok(p) = auth
                        .verify_basic(u, &secrecy::SecretString::from(pw.to_string()), peer)
                        .await
                    {
                        principal = Some(p);
                    }
                }
            }
        }
    }

    if principal.is_none() {
        if let Some(cookie) = headers
            .get(http::header::COOKIE)
            .and_then(|v| v.to_str().ok())
        {
            for part in cookie.split(';') {
                if let Some((k, val)) = part.trim().split_once('=') {
                    if k == "__Host-sc_sid" {
                        if let Ok(Some(p)) = auth.validate_session(val) {
                            principal = Some(p);
                        }
                    }
                }
            }
        }
    }

    let Some(principal) = principal else {
        // Unauthenticated: let it through unchanged so `sc-dav`/the compat
        // layer answer `401` themselves, exactly as before this gate existed.
        return next.run(req).await;
    };

    // Record onto the ambient request span (`request_trace_layer`, `app.rs`)
    // so the eventual per-request log line names who made the request — the
    // whole DAV mount and compat surface passes through
    // here, so this is the one place that can do it for that traffic
    // without duplicating auth resolution. Recorded even for a request the
    // scope gate below ends up denying: "who tried" is exactly what an
    // operator debugging a 403 needs.
    tracing::Span::current().record("user", principal.user.get());

    // Scope gate: only an app password ever carries a restriction
    // (`AuthVia::AppPassword`) — see this function's doc comment.
    if matches!(principal.via, sc_auth::AuthVia::AppPassword(_)) {
        let method = req.method().clone();
        if method != http::Method::OPTIONS {
            let path = req.uri().path().to_string();

            let perms_deny = match principal.scope.perms_mask {
                None => false, // unrestricted app password: unchanged behavior
                Some(mask) => {
                    let required = if dav_shaped.matches(&path) {
                        dav_required_perms(&method)
                    } else {
                        // OCS / Login Flow v2 / status.php: no mapping for a
                        // restricted scope to prove itself against.
                        None
                    };
                    // `from_bits_truncate`: an unrecognised bit narrows what
                    // the mask is understood to grant, never widens it.
                    let have = sc_acl::Perms::from_bits_truncate(mask);
                    !matches!(required, Some(need) if have.contains(need))
                }
            };

            let shares_deny = match &principal.scope.shares {
                None => false, // unrestricted by share: unchanged behavior
                Some(allowed) => {
                    if !dav_shaped.matches(&path) {
                        // Same fail-closed rule as the perms half, extended
                        // to a credential restricted *only* by share (a
                        // `perms_mask: None` token still narrowed by
                        // `shares` would otherwise sail through here).
                        true
                    } else {
                        let in_scope = |vpath: &str| matches!(core.resolve_share(principal.user, vpath), Ok(share) if allowed.contains(&share));
                        let primary_ok = match dav_shaped.vpath_of(&path) {
                            Some(vp) => in_scope(&vp),
                            // Chunked-upload session paths, the principal/root
                            // discovery stubs — no vpath to check at all.
                            None => false,
                        };
                        let dest_ok = if matches!(method.as_str(), "MOVE" | "COPY") {
                            match dav_shaped.destination_vpath(&headers) {
                                Some(vp) => in_scope(&vp),
                                None => false,
                            }
                        } else {
                            true
                        };
                        !(primary_ok && dest_ok)
                    }
                }
            };

            if perms_deny || shares_deny {
                return sc_dav::DavError::Forbidden.into_response();
            }
        }
    }

    req.extensions_mut()
        .insert(sc_dav::DavPrincipal(principal.user));
    next.run(req).await
}

/// The jailed, forked worker pool.
///
/// `JailedWorkerPool::new` forks the children and each child jails itself
/// before it will accept a single job; a child that cannot install its
/// Landlock ruleset, its rlimits, or its seccomp filter exits instead of
/// serving. So there is no code path here that produces a running-but-
/// unjailed decoder.
#[cfg(target_os = "linux")]
fn preview_worker_pool() -> anyhow::Result<Arc<dyn sc_preview::WorkerPool>> {
    let pool = sc_preview::worker::jailed::JailedWorkerPool::new(Default::default())
        .map_err(|e| anyhow::anyhow!("could not start the sandboxed preview worker pool: {e}"))?;
    tracing::info!(pids = ?pool.worker_pids(), "preview decoders run in jailed worker processes");
    Ok(Arc::new(pool))
}

/// Non-Linux fallback. Landlock and seccomp have no equivalent here, so
/// decoders share the server's address space and a memory-corruption bug in
/// one is a compromise of the whole process. Say so at startup — this is a
/// development posture, not a deployment one.
#[cfg(not(target_os = "linux"))]
fn preview_worker_pool() -> anyhow::Result<Arc<dyn sc_preview::WorkerPool>> {
    tracing::warn!(
        "preview decoders are running IN-PROCESS: this platform has no Landlock/seccomp \
         equivalent, so a decoder memory-corruption bug compromises the whole server \
. Deploy on Linux."
    );
    Ok(Arc::new(sc_preview::InProcessWorkerPool::default()))
}

/// There is deliberately no chunk-size ceiling here.
/// is explicit that the engine streams each chunk straight to disk through a
/// fixed-size reused buffer, so RSS never depends on chunk size — there is
/// no ceiling to enforce, and `sc_upload::UploadConfig` (below) has no field
/// to receive one. `Config::upload` used to carry a `chunk_max_bytes` key
/// that nothing ever read; it has been deleted from `config.rs` rather than
/// wired through, since wiring it would mean inventing an enforcement point
/// this design argues against — worse than the config key not existing at
/// all.
fn upload_config(cfg: &Config) -> sc_upload::UploadConfig {
    sc_upload::UploadConfig {
        chunk_size_min: cfg.upload.chunk_min_bytes,
        chunk_size_default: cfg.upload.chunk_default_bytes,
        chunk_size_advisory: cfg.upload.chunk_default_bytes,
        ..Default::default()
    }
}

// Every parameter here has a distinct type (no two `Arc<...>`s wrap the same
// concrete or trait type), so the compiler already rejects an argument-order
// mistake; this is also a private helper with exactly one call site
// (`App::build`). Bundling into a struct would relocate the same 8 fields
// into a literal there without reducing risk or improving readability —
// unlike `Compat::build`, which has two same-typed `String` params and does
// get a struct for exactly that reason.
#[allow(clippy::too_many_arguments)]
fn build_http_state(
    cfg: &Config,
    auth: Arc<sc_auth::AuthService>,
    setup: Arc<crate::setup::SetupGate>,
    core_bridge: Arc<CoreBridge>,
    core: Arc<sc_core::Core>,
    uploads: Arc<sc_upload::UploadEngine>,
    content: Arc<dyn sc_http::content_api::ContentApi>,
    search: Arc<dyn sc_http::search_api::SearchApi>,
    search_concurrency: Arc<sc_http::search_limits::SearchConcurrency>,
    search_rate: Arc<sc_http::rate_limit::KeyedTokenBucket>,
    archive_concurrency: Arc<sc_http::state::ResizableSemaphore>,
    settings: Arc<dyn sc_http::settings_api::SettingsApi>,
    restart_signal: Arc<tokio::sync::Notify>,
    ws_hub: Arc<sc_http::ws::WsHub>,
    oidc: Arc<dyn sc_http::oidc_api::OidcApi>,
) -> anyhow::Result<sc_http::AppState> {
    let mut http_cfg = sc_http::config::HttpConfig {
        trusted_proxy_cidrs: cfg
            .trusted_proxies
            .iter()
            .filter_map(|s| sc_http::config::Cidr::parse(s))
            .collect(),
        app_hosts: cfg.app_hosts.clone(),
        content_hosts: cfg.content_hosts.clone(),
        body_limit_bytes: 16 * 1024 * 1024,
        chunk_size_min: cfg.upload.chunk_min_bytes,
        chunk_size_default: cfg.upload.chunk_default_bytes,
        // Same resolution the compatibility layer mounts on, and for the same
        // reason: a URL handed to somebody else has to be one they can reach.
        // Ambiguous or invalid leaves this `None`, so share links keep the
        // old `https://{app_hosts[0]}` guess rather than losing the feature
        // over a config value only the compat layer previously needed.
        public_base_url: match cfg.resolve_compat_canonical_url() {
            crate::config::CompatCanonicalUrl::Configured(url)
            | crate::config::CompatCanonicalUrl::Derived(url) => Some(url),
            _ => None,
        },
        // What the CSRF check compares a private-LAN `Origin` against, so that
        // a neighbouring service on the same address but another port is not
        // mistaken for us (`sc_http::config::is_self_lan_origin`).
        https_port: Some(cfg.bind.port()),
        ..Default::default()
    };
    // The Host header carries whatever literal the client dialled, so a
    // server bound to 0.0.0.0:8080 and reached at 192.168.1.10 sees that
    // address, not a resolved name. Without this a first run rejects every
    // request from its own bind address with 421 — which is exactly what
    // happened the first time the binary was actually started.
    let bind_ip = cfg.bind.ip();
    if !bind_ip.is_unspecified() {
        let bind_host = bind_ip.to_string();
        if !http_cfg.app_hosts.iter().any(|h| h == &bind_host) {
            http_cfg.app_hosts.push(bind_host);
        }
    }

    // CSRF's `Origin` allowlist. `HttpConfig`'s
    // default is the placeholder `https://app.example.com`, and nothing
    // populated it — so every cookie-authenticated state-changing request
    // failed the origin half of the check with 403. That is the entire write
    // surface of the web UI: new folder, rename, delete, move, copy, upload,
    // share. Reads worked, which is what made it look like a permissions bug.
    //
    // Derived from `app_hosts` rather than configured separately, so the two
    // cannot disagree: an origin is exactly a scheme plus one of the hosts we
    // already agreed to answer for. `https` only, since that is the only way in
    // (`Config::bind`) and a reverse proxy in front terminates `https` too. An
    // explicit `allowed_origins` in the config still wins, which is the escape
    // hatch for a proxy that publishes some other port.
    if cfg.allowed_origins.is_empty() {
        let port = cfg.bind.port();
        let mut origins = Vec::with_capacity(http_cfg.app_hosts.len() * 2);
        for h in &http_cfg.app_hosts {
            origins.push(format!("https://{h}"));
            origins.push(format!("https://{h}:{port}"));
        }
        http_cfg.allowed_origins = origins;
    } else {
        http_cfg.allowed_origins = cfg.allowed_origins.clone();
    }

    // Each compatibility layer contributes its own name; the core API never
    // learns what any of them mean.
    #[cfg(feature = "compat-nc")]
    http_cfg
        .extensions
        .push(crate::nc::EXTENSION_NAME.to_string());

    // `App::router` (below) merges this state's own `/api/**` router with the
    // WebDAV tree and, feature-gated, the compatibility layer — beside it,
    // not inside it, and neither of those mounts registers a fallback of its
    // own. That makes `sc_http::routes::admin_catch_all` (this crate's single
    // router-wide fallback) responsible for keeping an unmatched request
    // under one of *their* prefixes from turning into the embedded SPA's
    // `index.html` on a miss — see `HttpConfig::reserved_path_prefixes`'s doc
    // comment for why `sc-http` cannot own this list itself.
    //
    // The literal strings live here, in the one crate the isolation gate
    // does not scan, on purpose: `sc-http` — and every other core crate — is
    // grepped for compat protocol vocabulary (`remote.php`, `ocs`) as
    // *code*, and this is exactly that, as runtime configuration data rather
    // than a hardcoded exception carved into the gate.
    http_cfg.reserved_path_prefixes = vec!["/dav/".to_string()];
    #[cfg(feature = "compat-nc")]
    http_cfg.reserved_path_prefixes.extend([
        "/remote.php/".to_string(),
        "/index.php/".to_string(),
        "/ocs/".to_string(),
        "/status.php".to_string(),
    ]);

    let mut csrf_key = [0u8; 32];
    getrandom::getrandom(&mut csrf_key).expect("OS entropy unavailable");

    Ok(sc_http::AppState {
        cfg: Arc::new(http_cfg),
        auth,
        core: core_bridge.clone(),
        uploads: Arc::new(UploadBridge {
            engine: uploads,
            core: core.clone(),
        }),
        content,
        search,
        setup,
        oidc,
        // 20 attempts, then one every 3 seconds per IP. A whole office behind
        // one NAT never notices; a script pointed at `/start` cannot make this
        // server open outbound connections to the IdP at will, which is the
        // resource on the other side of these two routes that is not ours.
        oidc_rate: Arc::new(sc_http::rate_limit::IpTokenBucket::new(
            20,
            std::time::Duration::from_secs(3),
        )),
        signed_url_keys: Arc::new(parking_lot::Mutex::new(
            sc_http::content::SignedUrlKeys::generate(),
        )),
        listings: Arc::new(sc_http::listing::ListingCache::new()),
        ws: ws_hub,
        // Persisted alongside every other per-feature store this function's
        // caller (`App::build`) already opens under `data_dir` (`shares.db`,
        // `upload.db`, `index.db`) — a job, and the per-item record of what
        // it has actually done so far, must survive a restart just like
        // those do.
        jobs: Arc::new(sc_http::state::JobStore::open(&cfg.data_dir.join("jobs.db"))?),
        rate_limiter: Arc::new(sc_http::rate_limit::IpTokenBucket::new(
            60,
            std::time::Duration::from_secs(1),
        )),
        // 10 attempts/hour per token.
        link_rate: Arc::new(sc_http::rate_limit::KeyedTokenBucket::new(
            10,
            std::time::Duration::from_secs(3600),
        )),
        // Per-user search rate limit, config-reachable via `[search]` — shared
        // with `SettingsBridge` (`App::build`) so a live rate-limit change
        // reconfigures the same bucket this state serves requests through.
        search_rate,
        // `POST /api/setup`: burst 10, then one attempt per 30 s per IP.
        // Generous enough that an operator who fat-fingers the token a few
        // times is never locked out, and small enough that guessing a 256-bit
        // token is not a strategy — 2/min against 2^256 is not a number worth
        // writing down. The general `rate_limiter` (60/s) sits in front of
        // this and is nowhere near tight enough on its own.
        setup_rate: Arc::new(sc_http::rate_limit::IpTokenBucket::new(
            10,
            std::time::Duration::from_secs(30),
        )),
        // Storage-class-aware per `[search]` — see `App::build`'s
        // `search_concurrency` construction, shared with `SearchBridge` so
        // the walk deadline and this cap always agree on the tier in play.
        search_concurrency,
        // 's rate-limit shape, config-reachable via
        // `[archive]` — see `crate::routes` / `sc_http::routes::fs_archive`.
        // Shared with `SettingsBridge` (`App::build`), same reasoning as
        // `search_rate` above.
        archive_concurrency,
        csrf_key,
        boot_time: std::time::Instant::now(),
        settings,
        restart_signal,
    })
}

/// READ is re-checked at *event delivery* time, not only
/// at subscription time — otherwise a user whose grant was revoked keeps
/// receiving change notifications, which leaks the existence of paths they
/// can no longer see.
struct AclReadCheck {
    core: Arc<sc_core::Core>,
    /// `None` when the watcher failed to start — `watch_subscribe`/
    /// `watch_unsubscribe` are then no-ops, same as everywhere else this
    /// crate treats a dead watcher as "no live push", never an error.
    watcher: Option<Arc<sc_watch::Watcher>>,
}

impl sc_http::ws::ReadPermCheck for AclReadCheck {
    fn can_read(&self, user: UserId, vpath: &str) -> bool {
        self.core.resolve(user, vpath).is_ok()
    }

    // `WsHub` calls these exactly once per (connection, vpath) 0<->1
    // transition (`ws.rs::handle_client_msg`/`disconnect`), so `sc_watch`'s
    // own per-key sticky refcount (`HotSet::add_sticky`/`remove_sticky`) is
    // the single source of truth for "how many subscribers does this
    // directory have" across every connection — no separate refcount is
    // kept here.
    //
    // Re-resolving `vpath` at unsubscribe time (rather than caching the
    // `(ShareId, SafePath)` from subscribe time) has one known gap: if the
    // user's grant on `vpath` is deleted entirely while still subscribed,
    // `resolve` fails closed and the matching `unsubscribe` never fires,
    // leaving that directory's sticky watch registered until the process
    // restarts. Accepted rather than built around: it needs an admin to
    // revoke a grant out from under a live subscription (not a client
    // action, and not triggered by ordinary use), and the leaked watch is
    // one entry for one real directory, not unbounded growth — `sc-watch`'s
    // hot-set cap ( / `WatchConfig::hot_set_max`) is what
    // guards against that.
    fn watch_subscribe(&self, user: UserId, vpath: &str) {
        let Some(w) = &self.watcher else { return };
        if let Ok(r) = self.core.resolve(user, vpath) {
            if let Err(e) = w.subscribe(r.share, &r.path) {
                tracing::warn!(vpath, error = %e, "watch subscribe failed; falling back to lazy revalidation for this path");
            }
        }
    }

    fn watch_unsubscribe(&self, user: UserId, vpath: &str) {
        let Some(w) = &self.watcher else { return };
        if let Ok(r) = self.core.resolve(user, vpath) {
            w.unsubscribe(r.share, &r.path);
        }
    }
}

/// Open every configured share. A share that will not open (missing path,
/// rejected filesystem) is logged and
/// skipped, never silently substituted: startup diagnostics already reported
/// it, and a half-open share is worse than an absent one.
pub(crate) fn register_shares(core: &sc_core::Core, cfg: &Config) -> anyhow::Result<()> {
    let policy = sc_vfs::SharePolicy {
        symlink: cfg.symlink_policy.into(),
        ..Default::default()
    };
    for (i, s) in cfg.shares.iter().enumerate() {
        let id = ShareId::new(i as u32 + 1);
        // Both apply an admin-UI edit persisted in `shares.db` rather than
        // `config.toml` (`Core::update_share`), and both are a no-op when
        // none was ever made. Without them the config file would win back on
        // every restart and the edit would look discarded.
        let (name, host_path) = core.apply_identity_override(id, s.name.clone(), s.host_path.clone());
        let def = sc_core::ShareDef {
            id,
            name: name.clone(),
            host_path,
            policy: core.apply_trash_override(id, policy.clone()),
            shared_externally: s.shared_externally,
        };
        if let Err(e) = core.register_share(def) {
            tracing::error!(share = %name, error = %e, "share rejected at startup; not registered");
        }
    }
    Ok(())
}

/// Load the live `AclEngine` from durable grants, running the one-time
/// legacy-projection migration first if this data directory has never
/// persisted a grant before.
///
/// This used to *be* the interim projection: "every enabled account gets a
/// full grant on every configured share", recomputed from nothing on every
/// call. It no longer computes anything — `sc_core::Core::migrate_legacy_grants`
/// and `Core::reload_acl` (both in `acl_store.rs`, which owns the actual
/// database) do the work now — but the call sites this function has (three
/// production paths: `App::build`, `SetupGate::complete`, `smb-sync`'s CLI
/// entry point; several integration tests besides) are left untouched by
/// keeping the same name and signature rather than rippling a rename through
/// all of them for what is now an internal detail.
///
/// `acl` is consequently unused: `core.reload_acl()` reaches the very same
/// `AclEngine` internally (`App::build` constructs `Core::new(meta, acl.clone())`
/// with this exact instance), so calling it a second way here would be
/// redundant, not additive.
pub fn project_grants(
    core: &sc_core::Core,
    _acl: &sc_acl::AclEngine,
    auth: &sc_auth::AuthService,
) -> anyhow::Result<()> {
    if core.acl_store_enabled() {
        let users = auth.list_users().unwrap_or_default();
        // Matches the old projection's own filter exactly: a disabled
        // account never got a grant from it either, so the migration must
        // not hand one to a disabled account that happens to still be on
        // record.
        let existing: Vec<sc_vfs::UserId> =
            users.iter().filter(|u| !u.disabled).map(|u| u.id).collect();
        core.migrate_legacy_grants(&existing)?;
    }
    core.reload_acl()?;

    // Group membership: `sc_auth::AuthService::list_memberships_all` reads
    // every `(user, group_)` row back in one shot — the accessor this used
    // to wait on before added group CRUD.
    core.set_group_memberships(auth.list_memberships_all().unwrap_or_default());

    tracing::info!(
        grants = core
            .list_grants(&sc_core::GrantFilter::default())
            .map(|v| v.len())
            .unwrap_or(0),
        "grants loaded from the durable store"
    );
    Ok(())
}

/// Builds the change watcher and the WebSocket hub together, because they
/// now share an object graph in both directions: the hub's `AclReadCheck`
/// needs the watcher (to register/release OS-level watches as clients
/// subscribe/unsubscribe), and the watcher's own `InvalEvent` sink needs the
/// hub to publish into. Building either one first and wiring the other in
/// after construction would need an `Option`/`OnceLock` seam for no benefit
/// — there is exactly one call site, in `App::build`.
fn start_watcher_and_ws_hub(
    cfg: &Config,
    core: Arc<sc_core::Core>,
) -> (Option<Arc<sc_watch::Watcher>>, Arc<sc_http::ws::WsHub>) {
    let backend = match cfg.watch.backend {
        crate::config::WatchBackend::Auto => sc_watch::WatchBackend::HotSet,
        crate::config::WatchBackend::Hotset => sc_watch::WatchBackend::HotSet,
        crate::config::WatchBackend::InotifyFull => sc_watch::WatchBackend::InotifyFull,
        crate::config::WatchBackend::Fanotify => sc_watch::WatchBackend::Fanotify,
    };
    let wcfg = sc_watch::WatchConfig {
        backend,
        hot_set_max: cfg.watch.hot_set_max as usize,
        full_threshold: cfg.watch.full_threshold as u64,
    };

    let (tx, rx) = crossbeam_channel::unbounded::<sc_watch::InvalEvent>();
    // A watcher that fails to start is not fatal: every read path re-stats
    // before trusting anything, so the only cost is that a change made
    // outside this process is noticed on next access rather than pushed.
    // `tx` is consumed either way — dropped inside
    // `Watcher::start` on failure, which ends the forwarder thread's `recv`
    // loop below cleanly instead of leaving it blocked forever.
    let watcher = match sc_watch::Watcher::start(wcfg, core.clone(), tx) {
        Ok(w) => Some(Arc::new(w)),
        Err(e) => {
            tracing::warn!(error = %e, "change watcher failed to start; falling back to lazy revalidation");
            None
        }
    };

    let ws_hub = sc_http::ws::WsHub::new(Arc::new(AclReadCheck {
        core: core.clone(),
        watcher: watcher.clone(),
    }));

    // The watcher's sink is a plain channel; forwarding it to the WebSocket
    // hub and the search index's reconciler is this process's job, and the
    // hub re-checks READ per subscriber.
    let watcher_core = core;
    // Weak, not a clone: the hub owns `AclReadCheck`, which owns the
    // `Arc<Watcher>`, which owns the `tx` half of `rx` below. A strong handle
    // here closed that ring — the thread kept the hub alive, the hub kept the
    // sender alive, and `rx.recv()` therefore never returned, so dropping the
    // `App` freed nothing: watcher inotify fd, `Arc<Core>` and its nine
    // `meta.db` descriptors, and the forked preview workers all stayed. One
    // `App` per process hid it; the `wiring` tests build seventeen and ran the
    // process out of descriptors.
    let ws_for_forward = Arc::downgrade(&ws_hub);
    // `publish_inval` calls bare `tokio::spawn` for its debounce timer, but
    // this is a plain OS thread with no ambient Tokio context — confirmed
    // live in the dev VM: the moment a client had a subscription open, an
    // external write to the watched share panicked the whole process
    // ("there is no reactor running, must be called from the context of a
    // Tokio 1.x runtime"), which systemd then silently restarted, hiding the
    // crash. `try_current()` is `None` only for `gc`/`smb-sync`, which build
    // an `App` with no HTTP server and so never have a subscriber for
    // `publish_inval` to spawn against.
    let rt_handle = tokio::runtime::Handle::try_current().ok();
    std::thread::spawn(move || {
        let _rt_guard = rt_handle.as_ref().map(|h| h.enter());
        while let Ok(ev) = rx.recv() {
            // The hub outlives every event in a live server; once it doesn't,
            // there is nobody left to publish to and nothing left to keep this
            // thread for.
            let Some(ws_for_forward) = ws_for_forward.upgrade() else {
                return;
            };
            // `sc_watch::InvalEvent::path` is share-relative
            // (`SafePath::to_display_string()`: `""` at the share root, else
            // no leading slash) — sc-watch, like sc-vfs beneath it, never
            // learns the HTTP layer's `/{label}/...` vpath vocabulary
            // ('s isolation rule). `WsHub::publish_inval`
            // and the `AclReadCheck::can_read` recheck above both key on
            // that full vpath (`Core::resolve` treats the first segment as a
            // share *label*), so publishing the bare share-relative path
            // here used to mean every watcher-driven `inval` matched zero
            // subscriptions — confirmed live: `ClientMsg::Sub{paths:
            // ["/Documents"]}` never once received an event for a change
            // made by another process, no matter how long it waited.
            //
            // The share -> label mapping is in general per-*grant*
            // (`sc_acl::AclEngine::roots`: a grant's own `label`, which can
            // rename or subpath-scope a root per user) and this event has no
            // `UserId` to resolve grants for — a filesystem change is
            // nobody's request. Falling back to the share's own registered
            // name covers the deployment shape this product actually
            // targets ('s one-grant-per-share default, where
            // label == share name, true of every share in `.dev/sc.toml`).
            // The one case this can't help — a grant that renames or
            // subpath-scopes a root — degrades to the existing lazy
            // revalidation (`dir_etag` mismatch on the next explicit list),
            // the same fallback `try_register`'s own degraded-subtree path
            // already relies on elsewhere in this pipeline; it never risks a
            // wrong or leaked notification, since `can_read` still gates
            // delivery per connection.
            let label = watcher_core
                .share_defs()
                .into_iter()
                .find(|d| d.id == ev.share)
                .map(|d| d.name);
            if let Some(label) = label {
                let vpath = if ev.path.is_empty() {
                    format!("/{label}")
                } else {
                    format!("/{label}/{}", ev.path)
                };
                ws_for_forward.publish_inval(&vpath, ev.etag.as_deref().unwrap_or(""));
            }

            // Search's T3 name index (a separate subsystem from the push
            // above): keep it in sync with a directory this process didn't
            // itself write to (`bridge::note_index_change` already covers
            // our own writes) — a no-op wherever the share has no index.
            if let Ok(dir) = sc_vfs::SafePath::parse(&ev.path, u16::MAX) {
                crate::bridge::reconcile_watch_event(&watcher_core, ev.share, &dir);
            }
        }
    });

    (watcher, ws_hub)
}

/// Interval between background upload-GC sweeps.
///
/// 15 minutes: the tighter of the doc's two documented targets (expired
/// sessions 15 min, orphaned part files 6h). One `UploadEngine::gc()` call
/// already does both passes together, and the orphan half has its own age
/// gate against `session_ttl` (`sweep_orphans` skips anything younger than
/// the TTL) — so running the combined pass on the tighter cadence costs one
/// extra bounded directory listing per previously-touched directory between
/// real expirations, not a share walk. Not config-exposed: there is no
/// legitimate reason to tune it away from the documented number — shorter
/// wastes a listing, longer widens the exact disk-fill window
/// (32 GB system-SSD budget) this loop exists to
/// close.
const UPLOAD_GC_INTERVAL: std::time::Duration = std::time::Duration::from_secs(15 * 60);

/// Handle to the periodic upload-GC sweep thread (`spawn_upload_gc`).
///
/// Without this, the thread was unstoppable: `App::build` discarded the
/// `JoinHandle` outright, so the loop just ran until the process was torn
/// down out from under it — abandoned mid-sweep on a bad day, and racing
/// `UploadApi::drain()`'s own `UploadEngine::gc()` call at shutdown on every
/// day ('s "must stop cleanly ... must not double-run
/// against the existing shutdown drain"). `stop_tx` doubles as the sleep
/// timer's wakeup: the loop blocks on `recv_timeout(interval)` rather than
/// `thread::sleep`, so a stop request interrupts a sleep immediately instead
/// of waiting out the rest of the interval.
pub struct UploadGcHandle {
    stop_tx: std::sync::mpsc::Sender<()>,
    join: std::sync::Mutex<Option<std::thread::JoinHandle<()>>>,
}

impl UploadGcHandle {
    /// Signal the loop to exit and wait for it to actually do so. Runs to
    /// completion of whatever it's mid-doing (an in-progress sweep, never
    /// longer) rather than aborting it — `run_upload_gc_pass` is a bounded,
    /// best-effort pass, so this is a bounded wait, not an indefinite one.
    /// Idempotent: a second call finds `join` already taken and is a no-op,
    /// so `shutdown.rs` calling this once and a test calling it again (e.g.
    /// via `Drop`, if ever added) can't double-join and panic.
    pub fn stop(&self) {
        let _ = self.stop_tx.send(());
        if let Some(handle) = self.join.lock().unwrap().take() {
            let _ = handle.join();
        }
    }
}

/// Start the periodic upload-session/part-file GC sweep for the life of the
/// process. Runs on a plain OS thread, not a Tokio
/// task: `App::build` is also reached from `sc-server gc` and `smb-sync`
/// (`lib.rs::cmd_gc`/`cmd_smb_sync`), neither of which runs inside a Tokio
/// runtime — `tokio::spawn` outside one panics ("there is no reactor
/// running"). A `std::thread` needs no runtime and costs one idle thread for
/// the process's lifetime, the same trade `start_watcher`'s forwarder thread
/// just above already makes. Those one-shot CLI commands never call
/// `UploadGcHandle::stop`, but dropping the handle drops `stop_tx` too, which
/// unblocks the thread's `recv_timeout` with `Disconnected` on its own — so
/// the thread still exits promptly once `App` does, rather than only via
/// process teardown.
fn spawn_upload_gc(
    uploads: Arc<sc_upload::UploadEngine>,
    core: Arc<sc_core::Core>,
) -> UploadGcHandle {
    spawn_upload_gc_with_interval(uploads, core, UPLOAD_GC_INTERVAL)
}

/// `spawn_upload_gc`, parameterized on the interval so a test can exercise
/// the sleep/sweep loop itself without waiting out a real 15 minutes.
fn spawn_upload_gc_with_interval(
    uploads: Arc<sc_upload::UploadEngine>,
    core: Arc<sc_core::Core>,
    interval: std::time::Duration,
) -> UploadGcHandle {
    let (stop_tx, stop_rx) = std::sync::mpsc::channel::<()>();
    let join = std::thread::spawn(move || loop {
        match stop_rx.recv_timeout(interval) {
            // Explicit stop, or the sender was dropped (handle discarded) --
            // either way, stop looping rather than running forever.
            Ok(()) | Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => run_upload_gc_pass(&uploads, &core),
        }
    });
    UploadGcHandle {
        stop_tx,
        join: std::sync::Mutex::new(Some(join)),
    }
}

/// One GC sweep: resolve each candidate's share and delegate to
/// `UploadEngine::gc`, surviving whatever that call does.
///
/// Wrapped in `catch_unwind` because `UploadEngine::gc` already turns a
/// per-row I/O/DB failure into a log line and keeps going (see its own
/// loop), but a panic unwinds straight past that handling. Without this
/// backstop, one panicking pass would unwind out of the spawned thread's
/// closure and end the thread — silently cancelling every future sweep for
/// the rest of the process's life, which is the original "nothing calls
/// gc()" bug again, just delayed until the first bad row.
fn run_upload_gc_pass(uploads: &Arc<sc_upload::UploadEngine>, core: &Arc<sc_core::Core>) {
    let core = core.clone();
    let resolver = move |share: ShareId| core.share(share);
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| uploads.gc(&resolver)));
    match result {
        Ok(Ok(n)) if n > 0 => tracing::info!(
            reclaimed = n,
            "upload gc: sessions and/or part files reclaimed"
        ),
        Ok(Ok(_)) => {}
        Ok(Err(e)) => {
            tracing::warn!(error = %e, "upload gc sweep failed; will retry next interval")
        }
        Err(_) => tracing::error!("upload gc sweep panicked; will retry next interval"),
    }
}

/// How often to check every share's name index for `needs_merge()`
/// ("merges on idle").
///
/// "Idle" here is deliberately narrow: it means "no admin-triggered
/// `/api/admin/index/build` job is running right now" (`CoreBridge::
/// run_idle_merge_pass` checks `index_build_running`), not CPU/disk load —
/// this deployment has no load signal to sense in the first place
/// (`CrawlThrottle`'s doc: the VM's virtio block devices have no
/// priority-aware I/O scheduler either). Ten minutes is frequent enough that
/// a share under steady write traffic doesn't accumulate an unbounded delta
/// backlog between checks, and infrequent enough that an idle deployment
/// spends effectively no time on it.
const IDLE_MERGE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(10 * 60);

/// Handle to the periodic idle-merge thread (`spawn_idle_merge`). Same shape
/// and stop/join contract as `UploadGcHandle` — see its doc for why this is a
/// `std::thread`, not a Tokio task (`App::build` runs outside a Tokio runtime
/// for `sc-server gc`/`smb-sync`).
pub struct IndexMergeHandle {
    stop_tx: std::sync::mpsc::Sender<()>,
    join: std::sync::Mutex<Option<std::thread::JoinHandle<()>>>,
}

impl IndexMergeHandle {
    pub fn stop(&self) {
        let _ = self.stop_tx.send(());
        if let Some(handle) = self.join.lock().unwrap().take() {
            let _ = handle.join();
        }
    }
}

fn spawn_idle_merge(core_bridge: Arc<CoreBridge>) -> IndexMergeHandle {
    spawn_idle_merge_with_interval(core_bridge, IDLE_MERGE_INTERVAL)
}

/// `spawn_idle_merge`, parameterized on the interval so a test can exercise
/// the loop without waiting out ten real minutes.
fn spawn_idle_merge_with_interval(
    core_bridge: Arc<CoreBridge>,
    interval: std::time::Duration,
) -> IndexMergeHandle {
    let (stop_tx, stop_rx) = std::sync::mpsc::channel::<()>();
    let join = std::thread::spawn(move || loop {
        match stop_rx.recv_timeout(interval) {
            Ok(()) | Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                // Same panic backstop as `run_upload_gc_pass`: one bad share
                // must not end the loop for every share after it.
                let cb = core_bridge.clone();
                if std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| cb.run_idle_merge_pass())).is_err() {
                    tracing::error!("idle name index merge pass panicked; will retry next interval");
                }
            }
        }
    });
    IndexMergeHandle {
        stop_tx,
        join: std::sync::Mutex::new(Some(join)),
    }
}

#[cfg(test)]
mod upload_gc_tests {
    //! `UploadEngine::gc` (`sc-upload`) was fully implemented and tested in
    //! isolation, but nothing in `sc-server` ever called it outside of a
    //! clean shutdown — these tests are about the *caller* added in this
    //! file, not the engine logic itself (covered separately in
    //! `sc_upload::engine::tests`).
    use super::*;
    use sc_vfs::UserId;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn engine_and_core(
        dir: &std::path::Path,
    ) -> (
        Arc<sc_upload::UploadEngine>,
        Arc<sc_core::Core>,
        Arc<sc_vfs::ShareRoot>,
    ) {
        let data_dir = dir.join("data");
        std::fs::create_dir_all(&data_dir).unwrap();
        let share =
            sc_vfs::ShareRoot::open(ShareId::new(1), &data_dir, sc_vfs::SharePolicy::default())
                .unwrap();
        let meta = Arc::new(sc_meta::MetaStore::open(&dir.join("meta.db")).unwrap());
        let acl = Arc::new(sc_acl::AclEngine::new());
        let core = Arc::new(sc_core::Core::new(meta, acl));
        core.register_share(sc_core::ShareDef {
            id: ShareId::new(1),
            name: "s".into(),
            host_path: data_dir.clone(),
            policy: sc_vfs::SharePolicy::default(),
            shared_externally: false,
        })
        .unwrap();
        let uploads = Arc::new(
            sc_upload::UploadEngine::new(
                &dir.join("upload.db"),
                sc_upload::UploadConfig {
                    session_ttl: std::time::Duration::from_millis(1),
                    ..Default::default()
                },
            )
            .unwrap(),
        );
        (uploads, core, Arc::new(share))
    }

    /// Create a session, let it become GC-eligible (`session_ttl` set to ~0
    /// above), and confirm a direct sweep reclaims it — proving
    /// `run_upload_gc_pass`'s wiring (resolver, error handling) is correct
    /// before trusting the sleep loop around it. The end-to-end version
    /// (an abandoned `.scpart` on disk, gone after a real periodic sweep)
    /// needs a live server and an actual wait, so it is not a unit test.
    #[test]
    fn a_gc_eligible_session_is_reclaimed_by_one_sweep() {
        let dir = tempfile::tempdir().unwrap();
        let (uploads, core, share) = engine_and_core(dir.path());

        let spec = sc_upload::SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: sc_vfs::SafePath::parse("abandoned.bin", 64).unwrap(),
            total_len: Some(10),
            random_access: false,
            if_match: None,
            mode: sc_upload::SpoolMode::OffsetAddressed,
            meta: sc_upload::UploadMeta {
                filename: "abandoned.bin".into(),
                ..Default::default()
            },
        };
        uploads.create(&share, spec).unwrap();
        std::thread::sleep(std::time::Duration::from_millis(20)); // past the 1ms TTL above

        let part_file_before = std::fs::read_dir(dir.path().join("data")).unwrap().count();
        assert_eq!(
            part_file_before, 1,
            "the sparse part file must exist before the sweep"
        );

        run_upload_gc_pass(&uploads, &core);

        let part_file_after = std::fs::read_dir(dir.path().join("data")).unwrap().count();
        assert_eq!(
            part_file_after, 0,
            "the abandoned session's part file must be gone after one sweep"
        );
    }

    /// "It must survive a `drain()` that returns an error or panics — one
    /// bad share must not stop the sweep for every other share, and it must
    /// not take the server down": this proves the `catch_unwind` backstop
    /// directly, driving `run_upload_gc_pass` with a resolver that panics.
    #[test]
    fn a_panicking_share_resolver_does_not_poison_future_sweeps() {
        let dir = tempfile::tempdir().unwrap();
        let (uploads, core, share) = engine_and_core(dir.path());

        let spec = sc_upload::SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: sc_vfs::SafePath::parse("abandoned2.bin", 64).unwrap(),
            total_len: Some(10),
            random_access: false,
            if_match: None,
            mode: sc_upload::SpoolMode::OffsetAddressed,
            meta: sc_upload::UploadMeta {
                filename: "abandoned2.bin".into(),
                ..Default::default()
            },
        };
        uploads.create(&share, spec).unwrap();
        std::thread::sleep(std::time::Duration::from_millis(20));

        // A resolver standing in for "resolving this share panicked" (e.g. a
        // poisoned lock somewhere downstream) rather than the ordinary,
        // already-handled "share unregistered" (`None`) case.
        let calls = Arc::new(AtomicUsize::new(0));
        {
            let calls = calls.clone();
            let panicking = move |_: ShareId| -> Option<Arc<sc_vfs::ShareRoot>> {
                calls.fetch_add(1, Ordering::SeqCst);
                panic!("simulated share-resolution panic");
            };
            let result =
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| uploads.gc(&panicking)));
            assert!(
                result.is_err(),
                "the panic must propagate out of this one gc() call"
            );
        }
        assert_eq!(calls.load(Ordering::SeqCst), 1);

        // The next sweep, with a working resolver, must still succeed --
        // the earlier panic must not have poisoned the engine (e.g. via a
        // `parking_lot` mutex left locked) or otherwise wedged future runs.
        run_upload_gc_pass(&uploads, &core);
        let remaining = std::fs::read_dir(dir.path().join("data")).unwrap().count();
        assert_eq!(
            remaining, 0,
            "a later, non-panicking sweep must still reclaim the session"
        );
    }

    /// The sleep/spawn plumbing itself, on a short interval so the test
    /// doesn't wait out the real 15-minute production constant.
    #[test]
    fn the_periodic_loop_actually_sweeps_on_its_own() {
        let dir = tempfile::tempdir().unwrap();
        let (uploads, core, share) = engine_and_core(dir.path());

        let spec = sc_upload::SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: sc_vfs::SafePath::parse("periodic.bin", 64).unwrap(),
            total_len: Some(10),
            random_access: false,
            if_match: None,
            mode: sc_upload::SpoolMode::OffsetAddressed,
            meta: sc_upload::UploadMeta {
                filename: "periodic.bin".into(),
                ..Default::default()
            },
        };
        uploads.create(&share, spec).unwrap();
        std::thread::sleep(std::time::Duration::from_millis(20));

        let handle =
            spawn_upload_gc_with_interval(uploads, core, std::time::Duration::from_millis(50));
        // Generous relative to the 50ms interval, tight relative to a human
        // waiting on a test suite.
        std::thread::sleep(std::time::Duration::from_millis(500));

        let remaining = std::fs::read_dir(dir.path().join("data")).unwrap().count();
        assert_eq!(
            remaining, 0,
            "the background loop must have swept on its own within a few intervals"
        );
        handle.stop();
    }

    /// "Must stop cleanly on shutdown rather than being abandoned mid-pass":
    /// `stop()` must actually return (i.e. the spawned thread's `join`
    /// completes) within a bounded time, not hang forever waiting on a sleep
    /// that never gets interrupted. Uses a long interval specifically so a
    /// naive `thread::sleep`-based loop (the shape this replaced) would make
    /// this test time out.
    #[test]
    fn stop_interrupts_the_sleep_instead_of_waiting_out_the_interval() {
        let dir = tempfile::tempdir().unwrap();
        let (uploads, core, _share) = engine_and_core(dir.path());

        let handle =
            spawn_upload_gc_with_interval(uploads, core, std::time::Duration::from_secs(3600));
        let start = std::time::Instant::now();
        handle.stop();
        assert!(
            start.elapsed() < std::time::Duration::from_secs(5),
            "stop() must interrupt the sleep, not wait out a 1-hour interval"
        );
    }
}

#[cfg(test)]
mod passdb_arming_tests {
    //! Which deployments get a passdb publisher, and which deliberately get
    //! nothing at all. The publisher's own
    //! behaviour is covered in `passdb.rs`; this is about the wiring.
    use super::*;

    struct NoopSink;
    impl sc_auth::PassdbSink for NoopSink {
        fn republish(&self) {}
    }

    fn app_with_smb(enabled: bool) -> (App, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join("share")).unwrap();
        let cfg = crate::config::Config {
            data_dir: dir.path().join("data"),
            shares: vec![crate::config::ShareBootstrap {
                name: "root".into(),
                host_path: dir.path().join("share"),
                shared_externally: false,
            }],
            smb: sc_smb::SmbConfig {
                enabled,
                // Never written by these tests: arming starts a thread and
                // nothing marks the passdb dirty, so no render happens.
                config_dir: dir.path().join("smb"),
                ..sc_smb::SmbConfig::default()
            },
            ..crate::config::Config::default()
        };
        let key = crate::masterkey::MasterKeyResult {
            key: [4u8; 32],
            inside_data_dir: false,
            generated: true,
        };
        (App::build(cfg, &key).expect("app builds"), dir)
    }

    /// "Do not spin up work for a deployment that never enabled SMB": no
    /// thread, and no sink either, so `sc-auth` keeps logging that an NT hash
    /// changed with nowhere to publish it, which for this deployment is the
    /// truth.
    #[test]
    fn smb_off_gets_no_publisher_and_no_sink() {
        let (app, _dir) = app_with_smb(false);
        app.arm_passdb_publisher();

        assert!(app.passdb.get().is_none(), "no SMB, no publisher thread");
        assert!(
            app.auth.set_passdb_sink(Arc::new(NoopSink)),
            "the sink slot has to still be free, or something was installed for a feature that is off"
        );
    }

    /// And the case the whole module exists for.
    #[test]
    fn smb_on_installs_the_sink_and_keeps_the_publisher() {
        let (app, _dir) = app_with_smb(true);
        app.arm_passdb_publisher();

        assert!(app.passdb.get().is_some());
        assert!(
            !app.auth.set_passdb_sink(Arc::new(NoopSink)),
            "the slot must already hold the publisher: `set_passdb_sink` keeps the first sink, so \
             a free slot here means NT hash changes are going nowhere"
        );
    }

    /// Building an `App` is what `gc` and `smb-sync` do, and neither may end
    /// up with a background thread rewriting the files they render by hand.
    #[test]
    fn building_an_app_arms_nothing_by_itself() {
        let (app, _dir) = app_with_smb(true);
        assert!(app.passdb.get().is_none());
        assert!(app.auth.set_passdb_sink(Arc::new(NoopSink)));
    }
}
