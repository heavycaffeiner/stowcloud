//! Endpoints that exist only so clients stop asking.
//!
//! Every one of these belongs to an app we have
//! declared a non-goal. Without them the client either retries in a loop or
//! surfaces an error banner to the user — a 404 on `notifications` in
//! particular makes the desktop client log an error every poll interval.
//!
//! The rule is: answer with an **empty success**, and make sure the matching
//! capability says the feature is off, so the client has no reason to look at
//! the payload.

use crate::ocs::Val;

/// `/ocs/v2.php/apps/notifications/api/v2/notifications` -> `data: []`
pub fn notifications() -> Val {
    Val::empty_list()
}

/// `/ocs/v2.php/apps/user_status/api/v1/statuses` -> `data: []`
pub fn user_statuses() -> Val {
    Val::empty_list()
}

/// `/ocs/v2.php/core/navigation/apps` -> `data: []`
///
/// An empty navigation list is what a server with no apps looks like, which is
/// exactly what we are.
pub fn navigation_apps() -> Val {
    Val::empty_list()
}

/// `/ocs/v2.php/core/autocomplete/get` -> `data: []`
///
/// Deliberately always empty rather than wired to `find_grantees`: this
/// endpoint has none of the sharee rate limiting or minimum-length checks, so
/// pointing it at the principal directory would reopen the account enumeration
/// hole that `ShareesApi` closes.
pub fn autocomplete() -> Val {
    Val::empty_list()
}

/// `/ocs/v2.php/apps/provisioning_api/api/v1/config/...` -> `data: {}`
pub fn empty_object() -> Val {
    Val::empty_map()
}

/// Paths that must answer **404**, not an empty success.
///
/// Returning `200 []` for these is worse than 404: the client takes it as
/// "supported but empty" and keeps polling. A 404 is a definitive answer, and
/// for `activity` it is the *documented* correct outcome because capabilities
/// advertise `activity.apiv2 = []`.
///
/// `user_status` singular used to answer `200 {}` instead — matching
/// `user_status.enabled = false` felt right, but the empty object was worse
/// than 404: the Android client's `GetStatusRemoteOperation` only special-cases
/// a 404 response (building a safe synthetic offline `Status` itself);
/// anything else, `{}` included, it hands straight to Gson. `Status.status`
/// is a non-nullable Kotlin field with no accessible no-arg constructor, so
/// Gson fills the empty object via `Unsafe.allocateInstance()` and leaves
/// `status` Java-null — a landmine the type system promised couldn't exist,
/// waiting for the first `when (status.status)` to NPE. 404 exercises the
/// client's own guarded path instead.
pub const NOT_FOUND_PATHS: &[&str] = &[
    "/ocs/v2.php/apps/activity/api/v2/activity",
    "/ocs/v1.php/apps/activity/api/v2/activity",
    "/ocs/v2.php/apps/user_status/api/v1/user_status",
    "/ocs/v1.php/apps/user_status/api/v1/user_status",
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stub_payload_shapes() {
        // Arrays vs objects are not interchangeable to a typed client.
        assert!(notifications().to_json().is_array());
        assert!(navigation_apps().to_json().is_array());
        assert!(autocomplete().to_json().is_array());
        assert!(empty_object().to_json().is_object());
        assert!(notifications().to_json().as_array().unwrap().is_empty());
    }
}
