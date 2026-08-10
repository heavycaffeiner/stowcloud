//! `GET /ocs/{v1,v2}.php/cloud/user`
//!
//! Reference: `apps/provisioning_api/lib/Controller/UsersController::getCurrentUser`,
//! plus `OCP\Files\FileInfo::SPACE_*` for the quota sentinels.

use crate::ocs::Val;
use crate::ports::{Quota, UserInfo};

/// `OCP\Files\FileInfo::SPACE_UNLIMITED`.
///
/// A quota of `-3` means "no limit". This is not a general-purpose sentinel —
/// `-2` is SPACE_UNKNOWN and `-1` is SPACE_NOT_COMPUTED, and clients render all
/// three differently. Reporting `0` for unlimited would make every client
/// display a full disk and refuse to upload.
pub const SPACE_UNLIMITED: i64 = -3;
/// `SPACE_UNKNOWN` — we cannot determine the size (e.g. statvfs failed).
pub const SPACE_UNKNOWN: i64 = -2;

/// Build the `quota` sub-object.
///
/// Key order is `free, used, total, relative, quota`, taken from
/// `AUserDataOCSController::fillStorageInfo`.
///
/// The unlimited branch is easy to get subtly wrong. The reference does **not**
/// report physical free space when there is no quota — it sets `free`, `total`
/// *and* `quota` all to `-3` and `relative` to `0`:
///
/// ```php
/// } else {                      // $quota <= 0
///     $relative = 0;
///     $free  = FileInfo::SPACE_UNLIMITED;
///     $total = FileInfo::SPACE_UNLIMITED;
/// }
/// ```
///
/// Reporting a real byte count in `total` while `quota` is `-3` makes some
/// clients compute a bogus usage bar; reporting `0` makes them refuse uploads.
pub fn quota_val(q: &Quota) -> Val {
    match q.total {
        Some(total) if total > 0 => {
            // PHP: round(($used / $quota) * 10000) / 100 — percent, 2 decimals.
            let relative = ((q.used as f64 / total as f64) * 10000.0).round() / 100.0;
            Val::map([
                ("free", Val::Int(total.saturating_sub(q.used) as i64)),
                ("used", Val::from(q.used)),
                ("total", Val::from(total)),
                ("relative", Val::Float(relative)),
                ("quota", Val::from(total)),
            ])
        }
        // No per-user cap. Only `quota` carries the sentinel: upstream
        // (`OC_Helper::getStorageInfo`) puts the storage's real free space in
        // `free` and derives `total = free + used`. We were sending `-3` for
        // all three, and the Android client compares the file size against
        // `free` before it will start an upload — a negative free space is
        // smaller than any file, so a large upload sat at "Pending operation"
        // forever without ever issuing a request.
        _ => {
            let total = q.free.saturating_add(q.used);
            let relative = if total > 0 {
                ((q.used as f64 / total as f64) * 10000.0).round() / 100.0
            } else {
                0.0
            };
            Val::map([
                ("free", Val::from(q.free)),
                ("used", Val::from(q.used)),
                ("total", Val::from(total)),
                ("relative", Val::Float(relative)),
                ("quota", Val::Int(SPACE_UNLIMITED)),
            ])
        }
    }
}

pub fn current_user(u: &UserInfo, q: &Quota) -> Val {
    Val::map([
        ("id", Val::str(u.login_name.clone())),
        ("enabled", Val::Bool(u.enabled)),
        ("quota", quota_val(q)),
        (
            "email",
            match &u.email {
                Some(e) => Val::str(e.clone()),
                None => Val::Null,
            },
        ),
        // Both spellings. Clients are split: older ones read `display-name`,
        // newer ones `displayname`. Emitting one only is a silently blank
        // account name in half the client population.
        ("displayname", Val::str(u.display_name.clone())),
        ("display-name", Val::str(u.display_name.clone())),
        ("phone", Val::str("")),
        ("address", Val::str("")),
        ("website", Val::str("")),
        ("twitter", Val::str("")),
        ("fediverse", Val::str("")),
        ("organisation", Val::str("")),
        ("role", Val::str("")),
        ("headline", Val::str("")),
        ("biography", Val::str("")),
        ("profile_enabled", Val::Bool(false)),
        (
            "groups",
            Val::list(u.groups.iter().map(|g| Val::str(g.clone())).collect::<Vec<_>>()),
        ),
        ("language", Val::str(u.language.clone())),
        ("locale", Val::str(u.locale.clone())),
        ("notify_email", Val::Null),
        (
            "backendCapabilities",
            Val::map([
                // We never let a client change a password or display name
                // through the provisioning API; saying so up front stops the
                // client rendering edit affordances that would 405.
                ("setDisplayName", Val::Bool(false)),
                ("setPassword", Val::Bool(false)),
            ]),
        ),
    ])
}

/// Another account, for the sharee-name lookup both apps do when they need to
/// turn a login into a display name.
///
/// The same shape as [`current_user`] minus `quota`, which is nobody else's
/// business, and minus the backend capabilities, which describe what *you* may
/// change about your own account.
pub fn other_user(u: &UserInfo) -> Val {
    Val::map([
        ("id", Val::str(u.login_name.clone())),
        ("enabled", Val::Bool(u.enabled)),
        (
            "email",
            match &u.email {
                Some(e) => Val::str(e.clone()),
                None => Val::Null,
            },
        ),
        // Both spellings, for the same split client population `current_user`
        // documents.
        ("displayname", Val::str(u.display_name.clone())),
        ("display-name", Val::str(u.display_name.clone())),
        ("phone", Val::str("")),
        ("address", Val::str("")),
        ("website", Val::str("")),
        ("twitter", Val::str("")),
        ("fediverse", Val::str("")),
        ("organisation", Val::str("")),
        ("role", Val::str("")),
        ("headline", Val::str("")),
        ("biography", Val::str("")),
        ("profile_enabled", Val::Bool(false)),
        (
            "groups",
            Val::list(u.groups.iter().map(|g| Val::str(g.clone())).collect::<Vec<_>>()),
        ),
        ("language", Val::str(u.language.clone())),
        ("locale", Val::str(u.locale.clone())),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::UserId;

    fn user() -> UserInfo {
        UserInfo {
            id: UserId(1),
            login_name: "alice".into(),
            display_name: "Alice".into(),
            email: None,
            enabled: true,
            groups: vec!["staff".into()],
            language: "ko".into(),
            locale: "ko_KR".into(),
        }
    }

    #[test]
    fn unlimited_quota_puts_the_sentinel_only_in_quota() {
        // `OC_Helper::getStorageInfo`: `free` is the storage's real free
        // space and `total = free + used`; only `quota` carries -3. Sending
        // -3 for `free` too stalled Android uploads at "Pending operation" —
        // it compares the file size against `free` before starting, and a
        // negative free space is smaller than any file.
        let q = Quota { used: 10, free: 90, total: None };
        let v = quota_val(&q).to_json();
        assert_eq!(v["quota"], -3);
        assert_eq!(v["free"], 90);
        assert_eq!(v["total"], 100);
        assert_eq!(v["relative"], 10.0);
        assert_eq!(v["used"], 10);
    }

    #[test]
    fn limited_quota_reports_the_cap() {
        let q = Quota { used: 25, free: 75, total: Some(100) };
        let v = quota_val(&q).to_json();
        assert_eq!(v["quota"], 100);
        assert_eq!(v["total"], 100);
        assert_eq!(v["free"], 75);
        assert_eq!(v["relative"], 25.0);
    }

    #[test]
    fn a_zero_quota_is_treated_as_unlimited_not_as_a_full_disk() {
        // Guards the `$quota > 0` branch condition: a 0 cap must not divide by
        // zero, and must not be reported as "100% used".
        let v = quota_val(&Quota { used: 5, free: 0, total: Some(0) }).to_json();
        assert_eq!(v["quota"], -3);
    }

    #[test]
    fn current_user_shape() {
        let j = current_user(&user(), &Quota { used: 0, free: 1, total: None }).to_json();
        assert_eq!(j["id"], "alice");
        assert_eq!(j["displayname"], "Alice");
        assert_eq!(j["display-name"], "Alice");
        assert_eq!(j["enabled"], true);
        assert!(j["email"].is_null());
        assert_eq!(j["groups"][0], "staff");
        assert_eq!(j["language"], "ko");
        assert_eq!(j["quota"]["quota"], -3);
    }
}
