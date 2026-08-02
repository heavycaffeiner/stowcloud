//! `GET /status.php`
//!
//! Reference: `status.php` at the reference repo root. This is the very first
//! request every client makes, before authentication, and the response shape
//! is parsed strictly.
//!
//! What the desktop client actually requires (`CheckServerJob::finished` in
//! `src/libsync/networkjobs.cpp`):
//!
//! * HTTP **200** — anything else is `instanceNotFound`.
//! * A non-empty body that parses as JSON **within the first 4 KiB**
//!   (`reply()->peek(4 * 1024)`), so this response must stay small.
//! * An `installed` key must be *present*.
//! * `maintenance` must be falsy or the client parks in maintenance mode.
//! * `Content-Type: application/json` and `Access-Control-Allow-Origin: *`.
//!
//! Per `DESIGN-COMPAT.md` §3 this endpoint carries **no information about
//! our actual implementation**. It is unauthenticated, so it is a
//! reconnaissance surface, and the client parser expects exactly these keys.
//! Our real identity is published through `/api/capabilities` and the `Server:`
//! header.

use axum::body::Body;
use axum::http::{header, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};

use crate::config::NcConfig;

pub fn status_json(cfg: &NcConfig) -> serde_json::Value {
    let c = &cfg.matrix.claim;
    // Key order mirrors the reference `$values` array. PHP's json_encode
    // preserves insertion order and serde_json's `json!` macro with a
    // non-preserve-order build sorts keys; clients do not care about order
    // here (they index by key), but keeping the same set matters.
    serde_json::json!({
        "installed": true,
        "maintenance": false,
        "needsDbUpgrade": false,
        "version": c.version,
        "versionstring": c.versionstring,
        "edition": c.edition,
        "productname": c.productname,
        "extendedSupport": c.extended_support,
    })
}

pub fn status_response(cfg: &NcConfig) -> Response {
    let body = serde_json::to_string(&status_json(cfg)).unwrap_or_else(|_| "{}".into());
    Response::builder()
        .status(StatusCode::OK)
        .header(
            header::CONTENT_TYPE,
            HeaderValue::from_static("application/json"),
        )
        // The reference sets this unconditionally; some clients probe
        // status.php from a browser context.
        .header("access-control-allow-origin", HeaderValue::from_static("*"))
        .body(Body::from(body))
        .expect("static header values are valid")
        .into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_has_exactly_the_reference_keys() {
        let j = status_json(&NcConfig::default());
        let obj = j.as_object().unwrap();
        let mut keys: Vec<&str> = obj.keys().map(|s| s.as_str()).collect();
        keys.sort_unstable();
        assert_eq!(
            keys,
            [
                "edition",
                "extendedSupport",
                "installed",
                "maintenance",
                "needsDbUpgrade",
                "productname",
                "version",
                "versionstring",
            ]
        );
        assert_eq!(j["installed"], true);
        assert_eq!(j["maintenance"], false);
        assert_eq!(j["productname"], "Nextcloud");
        assert_eq!(j["version"], "31.0.4.1");
        assert_eq!(j["versionstring"], "31.0.4");
        assert_eq!(j["edition"], "");
    }

    #[test]
    fn status_leaks_nothing_about_us() {
        let s = serde_json::to_string(&status_json(&NcConfig::default())).unwrap();
        let lower = s.to_ascii_lowercase();
        for leak in ["stowcloud", "sc-", "rust", "axum"] {
            assert!(
                !lower.contains(leak),
                "status.php must not disclose implementation details: found {leak}"
            );
        }
    }

    #[test]
    fn status_body_fits_in_the_clients_4kib_peek_window() {
        let s = serde_json::to_string(&status_json(&NcConfig::default())).unwrap();
        assert!(s.len() < 4096);
    }
}
