//! The route table.
//!
//! Two things live here, and only these two:
//!
//! * [`route_table`] — the declarative list of paths this binary serves,
//!   split along the `feature = "compat-nc"` boundary (`ARCHITECTURE.md`
//!   §1/§10.1). It is what `sc-server routes --json` dumps and what the
//!   isolation CI gate greps ("the `--no-default-features` build passes and
//!   that binary has zero NC routes").
//! * [`server_routes`] — the handful of endpoints `sc-server` itself owns,
//!   because they answer questions about the *process* rather than about
//!   storage, and so belong to no library crate.
//!
//! Every other path in the table is registered by the crate that implements
//! it: `sc_http::build_router`, `sc_dav::DavService::router`, and (feature
//! gated) `crate::nc::router`. Assembly happens in [`crate::app::App::router`].

use axum::routing::get;
use axum::Json;
use axum::Router;
use serde::Serialize;

#[derive(Clone, Debug, Serialize)]
pub struct RouteInfo {
    pub method: &'static str,
    pub path: &'static str,
    /// `"native"` or `"compat-nc"` — what the isolation-gate grep keys off.
    pub group: &'static str,
    /// Which crate registers the handler. Documentation, and a standing
    /// answer to "is this route real or a placeholder?".
    pub owner: &'static str,
}

/// `GET /api/health` — liveness only. It deliberately reports nothing about
/// configuration, versions, share count or user count: it is reachable
/// unauthenticated, and anything richer is a fingerprinting surface
/// (`DESIGN-API.md` §8's "leaks nothing beyond the fact the server exists" applies here
/// even more strictly than to `capabilities`).
async fn health() -> Json<serde_json::Value> {
    // Two states, and only two. `"ok"` was hardcoded, so this said the server
    // was fine while a share had been rejected at startup, the SMB bind had
    // failed, or the DB size guard had tripped — an endpoint that cannot
    // report ill health is decoration, and an uptime monitor watching it
    // learns nothing.
    //
    // *Which* of those is wrong stays out of the payload deliberately: this is
    // unauthenticated, and the reasons are a fingerprinting surface (see this
    // module's header and `Diagnostics::degraded_reasons`, which exists for
    // logs and an authenticated view). "Something is wrong, go look" is the
    // most an anonymous caller gets.
    let status = if crate::diagnostics::is_degraded_now() {
        "degraded"
    } else {
        "ok"
    };
    Json(serde_json::json!({ "status": status }))
}

pub fn server_routes() -> Router {
    Router::new().route("/api/health", get(health))
}

/// Routes that exist regardless of compile-time features.
fn native_routes() -> Vec<RouteInfo> {
    vec![
        RouteInfo {
            method: "GET",
            path: "/api/health",
            group: "native",
            owner: "sc-server",
        },
        RouteInfo {
            method: "GET",
            path: "/api/capabilities",
            group: "native",
            owner: "sc-http",
        },
        // First-run administrator bootstrap (`DESIGN-AUTH.md` §8).
        // Unauthenticated by necessity — there is no account to authenticate
        // as yet — and permanently closed the moment one exists. `sc-http`
        // owns the route and the wire shapes; `sc-server` owns the gate,
        // because only it knows about the one-time token.
        RouteInfo {
            method: "GET",
            path: "/api/setup",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "POST",
            path: "/api/setup",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/login",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/login/totp",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/logout",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/auth/session",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/auth/app-passwords",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/app-passwords",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/auth/app-passwords/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/password",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/totp/setup",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/totp/enroll",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/totp/disable",
            group: "native",
            owner: "sc-http",
        },
        // Recovery-code self-service (`DESIGN-AUTH.md` §6.2: how many unused
        // codes remain, and reissuing) was listed here as `GET`/`POST
        // /api/auth/totp/recovery-codes` for a route that was never actually
        // registered anywhere in `sc_http::routes::protected_routes` — the
        // only place recovery codes reach the wire today is inline in
        // `auth_totp_enroll`'s response, at first enrollment. A router-drift
        // test (`tests/route_drift.rs::every_table_path_is_a_real_route`)
        // caught the table claiming a path the assembled router 404s on.
        // Removed rather than left in as aspirational documentation: this
        // table is `sc-server routes --json`'s and `diagnostics`'s source of
        // truth for what is actually wired, and a phantom entry here is the
        // exact "advertised but not real" failure this file exists to avoid.
        // Implementing the endpoint is real, undone work for whoever owns
        // `sc_http::routes` — not restored here.
        RouteInfo {
            method: "GET",
            path: "/api/auth/sessions",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/auth/sessions/{id_hash}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/auth/smb",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/fs/list",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/fs/stat",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/mkdir",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/rename",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/move",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/copy",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/delete",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/fs/read",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PUT",
            path: "/api/fs/write",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/link",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/fs/archive",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/trash",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/trash/restore",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/trash/purge",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/shares",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/shares",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/shares/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/shares/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/shares/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/search",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/search/stream",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/jobs",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/jobs/{id}",
            group: "native",
            owner: "sc-http",
        },
        // `sc_http::routes::protected_routes` mounts `.delete(job_cancel)` on
        // the same path — missing here until a router-drift test
        // (`tests/route_drift.rs`) caught it; see that file for why axum
        // gives no route-table introspection to check this any other way.
        RouteInfo {
            method: "DELETE",
            path: "/api/jobs/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/events",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/admin/storage",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/admin/index/estimate",
            group: "native",
            owner: "sc-http",
        },
        // Same handler answers both methods (`admin_index_estimate` reads no
        // body either way) — `sc_http::routes::protected_routes` mounts
        // `.post(admin_index_estimate)` alongside the `.get()` on this path;
        // this entry was missing for the same reason the `DELETE` above was.
        RouteInfo {
            method: "POST",
            path: "/api/admin/index/estimate",
            group: "native",
            owner: "sc-http",
        },
        // Admin-settable, server-global chunk floor/default (`DESIGN-UPLOAD.md`
        // §1.3) — persisted in `upload.db`, read live by `capabilities`/
        // `GET /api/auth/session`.
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/upload-settings",
            group: "native",
            owner: "sc-http",
        },
        // Server-settings admin screen: every operator-settable `config.toml`
        // field, live-apply where possible, restart-required where not
        // (`crate::settings_bridge::SettingsBridge`). Persisted in
        // `settings.db`, mirroring the upload/index override pairs above.
        RouteInfo {
            method: "GET",
            path: "/api/admin/server-settings",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/smb",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/search",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/archive",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/network",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/db",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/symlink-policy",
            group: "native",
            owner: "sc-http+sc-server",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/server-settings/homes",
            group: "native",
            owner: "sc-http+sc-server",
        },
        // Notifies `cmd_serve`'s restart branch — see `lib.rs`'s
        // `restart_signal`/exit-code-75 handling.
        RouteInfo {
            method: "POST",
            path: "/api/admin/server-settings/restart",
            group: "native",
            owner: "sc-http+sc-server",
        },
        // Whether the T3 name index is on, and the runtime toggle for it
        // (`FEATURES.md` #116) — persisted in `index.db`, mirroring the
        // upload-settings pair above.
        RouteInfo {
            method: "GET",
            path: "/api/admin/index/settings",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/index/settings",
            group: "native",
            owner: "sc-http",
        },
        // Starts a full crawl-and-rebuild of every share's name index
        // through the same `/api/jobs/{id}` queue as `fs.copy`/`fs.archive`.
        RouteInfo {
            method: "POST",
            path: "/api/admin/index/build",
            group: "native",
            owner: "sc-http",
        },
        // User management (`FEATURES.md` #157). Admin-only — `sc-http`'s
        // `require_admin` re-checks the account fresh on every one of these,
        // same as the two routes above.
        RouteInfo {
            method: "GET",
            path: "/api/admin/users",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/admin/users",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/users/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/admin/users/{id}",
            group: "native",
            owner: "sc-http",
        },
        // Grant management (deny by default: no access until explicitly
        // granted —). `sc_core::acl_store`
        // owns persistence; `sc-http` owns the HTTP skin, admin-gated the
        // same way the routes above are.
        // Folder shares (`FEATURES.md` #40/#157: "there is no setting to add
        // folders"). `sc_core::Core::create_share`/`update_share`/
        // `delete_share` own persistence and validation; config-file shares
        // (below `sc_core::DYNAMIC_SHARE_ID_BASE`) refuse the latter two.
        RouteInfo {
            method: "GET",
            path: "/api/admin/shares",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/admin/shares",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/shares/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/admin/shares/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/api/admin/grants",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/admin/grants",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/grants/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/admin/grants/{id}",
            group: "native",
            owner: "sc-http",
        },
        // Group CRUD + membership (`FEATURES.md` #48).
        RouteInfo {
            method: "GET",
            path: "/api/admin/groups",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/admin/groups",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/admin/groups/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/admin/groups/{id}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/api/admin/groups/{id}/members",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/admin/groups/{id}/members/{user}",
            group: "native",
            owner: "sc-http",
        },
        // Audit log browsing (`FEATURES.md` #158).
        RouteInfo {
            method: "GET",
            path: "/api/admin/audit",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "GET",
            path: "/c/{token}",
            group: "native",
            owner: "sc-http",
        },
        // Public share links. Unauthenticated: the
        // token in the path, plus the link password when one is set, is the
        // whole authorization story.
        RouteInfo {
            method: "GET",
            path: "/s/{token}",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/s/{token}/auth",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/s/{token}/download",
            group: "native",
            owner: "sc-http",
        },
        RouteInfo {
            method: "POST",
            path: "/s/{token}/drop",
            group: "native",
            owner: "sc-http",
        },
        // TUS 1.0.0 core + creation + termination.
        RouteInfo {
            method: "OPTIONS",
            path: "/api/uploads",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        RouteInfo {
            method: "POST",
            path: "/api/uploads",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        RouteInfo {
            method: "OPTIONS",
            path: "/api/uploads/{id}",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        RouteInfo {
            method: "HEAD",
            path: "/api/uploads/{id}",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        RouteInfo {
            method: "PATCH",
            path: "/api/uploads/{id}",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        RouteInfo {
            method: "DELETE",
            path: "/api/uploads/{id}",
            group: "native",
            owner: "sc-http+sc-upload",
        },
        // RFC 4918 Class 2 + RFC 4331.
        RouteInfo {
            method: "OPTIONS",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "PROPFIND",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "PROPPATCH",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "GET",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "PUT",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "DELETE",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "MKCOL",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "COPY",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "MOVE",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "LOCK",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        RouteInfo {
            method: "UNLOCK",
            path: "/dav/{*path}",
            group: "native",
            owner: "sc-dav",
        },
        // Session-folder chunked upload (`dav_uploads.rs`). A top-level prefix
        // rather than `/dav/uploads/...`: axum matches literal segments before
        // a wildcard, so the latter would permanently shadow a share named
        // `uploads`.
        RouteInfo {
            method: "OPTIONS",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
        RouteInfo {
            method: "MKCOL",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
        RouteInfo {
            method: "PUT",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
        RouteInfo {
            method: "MOVE",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
        RouteInfo {
            method: "DELETE",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
        RouteInfo {
            method: "PROPFIND",
            path: "/dav-uploads/{*rest}",
            group: "native",
            owner: "sc-server+sc-upload",
        },
    ]
}

/// Compat-layer routes (`DESIGN-COMPAT.md`,
/// `ARCHITECTURE.md` §10.2). Entirely absent — not merely unregistered, the
/// *code that would produce this list* doesn't exist in the binary — when
/// built with `--no-default-features`.
#[cfg(feature = "compat-nc")]
fn compat_nc_routes() -> Vec<RouteInfo> {
    vec![
        RouteInfo {
            method: "GET",
            path: "/status.php",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/index.php/204",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "POST",
            path: "/index.php/login/v2",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "POST",
            path: "/index.php/login/v2/poll",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/index.php/login/v2/flow/{token}",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "POST",
            path: "/index.php/login/v2/grant",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/ocs/v1.php/{*rest}",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/ocs/v2.php/{*rest}",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/index.php/core/preview",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        RouteInfo {
            method: "GET",
            path: "/index.php/apps/files",
            group: "compat-nc",
            owner: "sc-compat-nc",
        },
        // The DAV tree, re-mounted under the layout NC clients expect.
        // `sc-compat-nc` maps the URL; `sc-dav` answers it.
        RouteInfo {
            method: "PROPFIND",
            path: "/remote.php/dav/files/{user}/{*path}",
            group: "compat-nc",
            owner: "sc-compat-nc+sc-dav",
        },
        RouteInfo {
            method: "GET",
            path: "/remote.php/webdav/{*path}",
            group: "compat-nc",
            owner: "sc-compat-nc+sc-dav",
        },
        RouteInfo {
            method: "PUT",
            path: "/remote.php/dav/uploads/{user}/{tid}/{*path}",
            group: "compat-nc",
            owner: "sc-compat-nc+sc-upload",
        },
    ]
}

/// The full route table: native routes, plus (feature-gated) compat
/// compatibility routes.
pub fn route_table() -> Vec<RouteInfo> {
    #[allow(unused_mut)]
    let mut routes = native_routes();
    #[cfg(feature = "compat-nc")]
    routes.extend(compat_nc_routes());
    routes
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn native_routes_present() {
        let routes = route_table();
        assert!(routes.iter().any(|r| r.path == "/api/health"));
        assert!(routes.iter().any(|r| r.path == "/api/capabilities"));
        assert!(routes.iter().any(|r| r.path.starts_with("/dav/")));
        assert!(routes.iter().any(|r| r.path == "/api/uploads"));
    }

    /// Recovery-code self-service (`DESIGN-AUTH.md` §6.2) has no route yet —
    /// see the removal note above `/api/auth/sessions` in `native_routes`.
    /// This is the negative of the old `recovery_code_routes_present`: it
    /// exists so a future re-add of the table entries without the matching
    /// `sc_http::routes::protected_routes` registration fails here first,
    /// rather than silently reintroducing the phantom entry
    /// `tests/route_drift.rs` caught.
    #[test]
    fn recovery_code_routes_are_not_claimed_until_they_are_real() {
        let routes = route_table();
        assert!(!routes
            .iter()
            .any(|r| r.path == "/api/auth/totp/recovery-codes"));
    }

    /// Nothing in the table may be listed without an owning crate — that
    /// field is how "wired" is distinguished from "declared".
    #[test]
    fn every_route_names_an_owner() {
        for r in route_table() {
            assert!(!r.owner.is_empty(), "{} has no owner", r.path);
        }
    }

    #[cfg(feature = "compat-nc")]
    #[test]
    fn compat_nc_feature_on_includes_nc_routes() {
        let routes = route_table();
        assert!(routes.iter().any(|r| r.path.contains("remote.php")));
        assert!(routes.iter().any(|r| r.path.contains("/ocs/")));
        assert!(routes.iter().any(|r| r.path.contains("status.php")));
    }

    #[cfg(not(feature = "compat-nc"))]
    #[test]
    fn compat_nc_feature_off_has_zero_nc_routes() {
        let routes = route_table();
        assert!(!routes.iter().any(|r| r.path.contains("remote.php")));
        assert!(!routes.iter().any(|r| r.path.contains("/ocs/")));
        assert!(!routes.iter().any(|r| r.path.contains("status.php")));
        assert!(routes.iter().all(|r| r.group == "native"));
    }

    #[test]
    fn server_routes_builds() {
        let _ = server_routes();
    }
}
