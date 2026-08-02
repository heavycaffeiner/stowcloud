//! Files Sharing OCS API.
//!
//! Reference:
//! - `apps/files_sharing/lib/Controller/ShareAPIController.php::formatShare`
//! - `apps/files_sharing/lib/Controller/ShareesAPIController.php::search`
//! - `lib/public/Share/IShare.php` (`TYPE_*`), `lib/public/Constants.php`
//!   (`PERMISSION_*`)
//!
//! ```text
//! GET    /ocs/v2.php/apps/files_sharing/api/v1/shares
//! POST   /ocs/v2.php/apps/files_sharing/api/v1/shares
//! PUT    /ocs/v2.php/apps/files_sharing/api/v1/shares/{id}
//! DELETE /ocs/v2.php/apps/files_sharing/api/v1/shares/{id}
//! GET    /ocs/v2.php/apps/files_sharing/api/v1/sharees
//! ```

use std::collections::HashMap;
use std::sync::Arc;

use parking_lot::Mutex;

use crate::config::{NcConfig, ShareeLookup};
use crate::ocs::{OcsError, Val};
use crate::props::{grantee_kind_to_share_type, nc_bits_to_perms, perms_to_nc_bits, share_type};
use crate::ports::{
    CoreShare, GranteeCandidate, GranteeKind, GranteeScope, PortError, SharePort, ShareSpec,
    UserId,
};

fn port_err(e: PortError) -> OcsError {
    match e {
        PortError::NotFound => OcsError::not_found("Wrong share ID, share does not exist"),
        PortError::Forbidden => OcsError::forbidden("Not allowed"),
        PortError::Invalid(m) => OcsError::bad_request(m),
        PortError::Conflict(m) => OcsError::new(409, m),
        PortError::Backend(m) => OcsError::server_error(m),
    }
}

/// Map an incoming `shareType` integer onto a grantee kind.
///
/// `DESIGN-COMPAT.md` §10: unsupported types get a **400**, never a silent
/// drop. Silently ignoring `shareType=4` would report success for an email
/// share that was never created — the user believes they shared a file and
/// nobody receives it.
pub fn share_type_to_kind(t: i64) -> Result<GranteeKind, OcsError> {
    match t {
        share_type::USER => Ok(GranteeKind::User),
        share_type::GROUP => Ok(GranteeKind::Group),
        share_type::PUBLIC_LINK => Ok(GranteeKind::Link),
        // Named so the error tells the admin what was attempted. 2 usergroup,
        // 4 email, 6 remote, 7 circle/team, 8 guest, 9 remote group, 10 room,
        // 12 deck, 15 sciencemesh — all documented non-goals.
        2 | 4 | 6..=10 | 12 | 13 | 15 => Err(OcsError::bad_request(format!(
            "Share type {t} is not supported by this server"
        ))),
        _ => Err(OcsError::bad_request("Unknown share type")),
    }
}

/// One share, in OCS shape.
///
/// Field types are not uniform in the reference and clients depend on the
/// inconsistency:
/// * `id` is a **string** (`IShare::getId(): string`).
/// * `mail_send` and `hide_download` are **ints** `0`/`1`.
/// * `can_edit`, `can_delete`, `has_preview` are real **booleans**.
/// * `password` is never the real password — it is `null` or the literal
///   `"redacted"` (`formatPasswordField`).
/// * `parent` is always `null`.
/// * `expiration` is `"Y-m-d H:i:s"` in the user's timezone, not RFC 3339.
pub fn format_share(s: &CoreShare, shares: &dyn SharePort) -> Val {
    let st = grantee_kind_to_share_type(s.kind);
    let perms = perms_to_nc_bits(s.perms) as i64;

    let mut v: Vec<(String, Val)> = vec![
        ("id".into(), Val::str(s.id.to_string())),
        ("share_type".into(), Val::Int(st)),
        ("uid_owner".into(), Val::str(s.owner.clone())),
        ("displayname_owner".into(), Val::str(s.owner_display.clone())),
        ("permissions".into(), Val::Int(perms)),
        ("can_edit".into(), Val::Bool(true)),
        ("can_delete".into(), Val::Bool(true)),
        ("stime".into(), Val::Int(s.created_s)),
        // Hardcoded null in the reference and never overwritten.
        ("parent".into(), Val::Null),
        (
            "expiration".into(),
            match s.expires_s {
                Some(t) => Val::str(format_expiration(t)),
                None => Val::Null,
            },
        ),
        (
            "token".into(),
            match &s.token {
                Some(t) => Val::str(t.clone()),
                None => Val::Null,
            },
        ),
        ("uid_file_owner".into(), Val::str(s.owner.clone())),
        ("note".into(), Val::str(s.note.clone())),
        ("label".into(), Val::str(s.label.clone())),
        (
            "displayname_file_owner".into(),
            Val::str(s.owner_display.clone()),
        ),
        ("path".into(), Val::str(s.path.clone())),
        (
            "item_type".into(),
            Val::str(if s.kind_is_dir { "folder" } else { "file" }),
        ),
        ("item_permissions".into(), Val::Int(perms)),
        ("is-mount-root".into(), Val::Bool(false)),
        ("mount-type".into(), Val::str("")),
        (
            "mimetype".into(),
            Val::str(if s.kind_is_dir {
                "httpd/unix-directory"
            } else {
                // We deliberately do not sniff or guess a MIME type
                // (DESIGN-API.md §4.3: the server saying a MIME type risks it
                // being trusted for a serving decision). Clients fall back to
                // extension-based detection.
                "application/octet-stream"
            }),
        ),
        ("has_preview".into(), Val::Bool(false)),
        ("storage_id".into(), Val::str("home")),
        ("storage".into(), Val::Int(1)),
        ("item_source".into(), Val::Int(s.file_id.0)),
        ("file_source".into(), Val::Int(s.file_id.0)),
        (
            "file_parent".into(),
            match s.parent_file_id {
                Some(p) => Val::Int(p.0),
                None => Val::Int(0),
            },
        ),
        ("file_target".into(), Val::str(s.path.clone())),
    ];

    match s.kind {
        GranteeKind::User => {
            let who = s.grantee.clone().unwrap_or_default();
            let disp = s.grantee_display.clone().unwrap_or_else(|| who.clone());
            v.push(("share_with".into(), Val::str(who.clone())));
            v.push(("share_with_displayname".into(), Val::str(disp)));
            v.push(("share_with_displayname_unique".into(), Val::str(who)));
        }
        GranteeKind::Group => {
            let who = s.grantee.clone().unwrap_or_default();
            let disp = s.grantee_display.clone().unwrap_or_else(|| who.clone());
            v.push(("share_with".into(), Val::str(who)));
            v.push(("share_with_displayname".into(), Val::str(disp)));
        }
        GranteeKind::Link => {
            let pw = if s.has_password {
                // NEVER the real password.
                Val::str("redacted")
            } else {
                Val::Null
            };
            v.push(("share_with".into(), pw.clone()));
            v.push((
                "share_with_displayname".into(),
                Val::str("(Shared link)"),
            ));
            v.push(("password".into(), pw));
            v.push(("send_password_by_talk".into(), Val::Bool(false)));
            v.push((
                "url".into(),
                match &s.token {
                    Some(t) => Val::str(shares.link_url(t)),
                    None => Val::Null,
                },
            ));
        }
    }

    // Always last, in this order, and both are ints not bools.
    v.push(("mail_send".into(), Val::Int(0)));
    v.push(("hide_download".into(), Val::Int(0)));
    v.push(("attributes".into(), Val::Null));

    Val::Map(v)
}

/// `Y-m-d H:i:s`, the format the reference uses for `expiration`.
///
/// Deliberately *not* RFC 3339: clients parse this with a fixed format string
/// and a `T` separator or a timezone suffix breaks them.
fn format_expiration(unix_s: i64) -> String {
    let (y, mo, d, h, mi, s) = civil_from_unix(unix_s);
    format!("{y:04}-{mo:02}-{d:02} {h:02}:{mi:02}:{s:02}")
}

/// Days-from-civil, inverted. Howard Hinnant's algorithm; no chrono dependency
/// for one format string.
fn civil_from_unix(t: i64) -> (i64, u32, u32, u32, u32, u32) {
    let days = t.div_euclid(86_400);
    let secs = t.rem_euclid(86_400);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    let y = if m <= 2 { y + 1 } else { y };
    (
        y,
        m,
        d,
        (secs / 3600) as u32,
        ((secs % 3600) / 60) as u32,
        (secs % 60) as u32,
    )
}

/// Parsed `POST`/`PUT` body for a share.
#[derive(Clone, Debug, Default)]
pub struct ShareRequest {
    pub path: Option<String>,
    pub share_type: Option<i64>,
    pub share_with: Option<String>,
    pub permissions: Option<u32>,
    pub password: Option<String>,
    pub expire_date: Option<String>,
    pub note: Option<String>,
    pub label: Option<String>,
}

impl ShareRequest {
    /// Accepts both `application/x-www-form-urlencoded` bodies and query
    /// strings; the OCS share API is used with both in the wild.
    pub fn from_form(pairs: &[(String, String)]) -> Self {
        let mut r = Self::default();
        for (k, v) in pairs {
            match k.as_str() {
                "path" => r.path = Some(v.clone()),
                "shareType" => r.share_type = v.parse().ok(),
                "shareWith" => r.share_with = Some(v.clone()),
                "permissions" => r.permissions = v.parse().ok(),
                "password" => r.password = Some(v.clone()),
                "expireDate" => r.expire_date = Some(v.clone()),
                "note" => r.note = Some(v.clone()),
                "label" => r.label = Some(v.clone()),
                _ => {}
            }
        }
        r
    }

    pub fn to_spec(&self) -> Result<ShareSpec, OcsError> {
        let path = self
            .path
            .clone()
            .filter(|p| !p.is_empty())
            // The reference checks `path` before `shareType`, and answers 404
            // here, not 400.
            .ok_or_else(|| OcsError::not_found("Please specify a file or folder path"))?;

        // Default -1 in the reference, which falls through to "Unknown share
        // type" -> 400. Same outcome here.
        let kind = share_type_to_kind(self.share_type.unwrap_or(-1))?;

        // PERMISSION_ALL when unspecified, matching
        // `shareapi_default_permissions`.
        let bits = self.permissions.unwrap_or(31);
        let perms = nc_bits_to_perms(bits).map_err(|unknown| {
            OcsError::bad_request(format!(
                "Unsupported permission bits 0x{unknown:x} in `permissions`"
            ))
        })?;

        if matches!(kind, GranteeKind::User | GranteeKind::Group)
            && self.share_with.as_deref().unwrap_or("").is_empty()
        {
            return Err(OcsError::bad_request(
                "`shareWith` is required for user and group shares",
            ));
        }

        if let Some(l) = &self.label {
            if l.len() > 255 {
                return Err(OcsError::bad_request("Maximum label length is 255"));
            }
        }

        Ok(ShareSpec {
            path,
            kind,
            grantee: self.share_with.clone(),
            perms,
            password: self.password.clone().filter(|p| !p.is_empty()),
            expires_s: expire_date_to_unix(self.expire_date.as_deref())?,
            label: self.label.clone(),
            note: self.note.clone(),
        })
    }
}

/// `expireDate` arrives as `YYYY-MM-DD` at 00:00 in the user's timezone.
fn expire_date_to_unix(s: Option<&str>) -> Result<Option<i64>, OcsError> {
    let Some(s) = s.map(str::trim).filter(|s| !s.is_empty()) else {
        return Ok(None);
    };
    let mut it = s.split('-');
    let (y, m, d) = match (it.next(), it.next(), it.next(), it.next()) {
        (Some(y), Some(m), Some(d), None) => (
            y.parse::<i64>().ok(),
            m.parse::<u32>().ok(),
            d.parse::<u32>().ok(),
        ),
        _ => (None, None, None),
    };
    let (Some(y), Some(m), Some(d)) = (y, m, d) else {
        return Err(OcsError::bad_request("Invalid date, date format must be YYYY-MM-DD"));
    };
    if !(1..=12).contains(&m) || !(1..=31).contains(&d) {
        return Err(OcsError::bad_request("Invalid date, date format must be YYYY-MM-DD"));
    }
    Ok(Some(unix_from_civil(y, m, d)))
}

fn unix_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = if m > 2 { m - 3 } else { m + 9 } as i64;
    let doy = (153 * mp + 2) / 5 + d as i64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    (era * 146_097 + doe - 719_468) * 86_400
}

// ---------------------------------------------------------------------------
// sharees
// ---------------------------------------------------------------------------

/// Fixed-window rate limiter for the sharee autocomplete.
///
/// Autocomplete over account names is an **enumeration oracle**: type `a`, get
/// every account starting with `a`. The reference's default minimum search
/// length is 0, i.e. wide open. Three defences here, all required together:
/// a minimum query length, this limiter, and `GranteeScope::SameGroup`.
pub struct ShareeLimiter {
    per_min: u32,
    hits: Mutex<HashMap<u32, (i64, u32)>>,
}

impl ShareeLimiter {
    pub fn new(per_min: u32) -> Self {
        Self { per_min, hits: Mutex::new(HashMap::new()) }
    }

    /// `now_s` is seconds; returns false when the caller is over budget.
    pub fn allow(&self, user: UserId, now_s: i64) -> bool {
        let window = now_s / 60;
        let mut g = self.hits.lock();
        let e = g.entry(user.0).or_insert((window, 0));
        if e.0 != window {
            *e = (window, 0);
        }
        if e.1 >= self.per_min {
            return false;
        }
        e.1 += 1;
        true
    }
}

pub struct ShareesApi {
    shares: Arc<dyn SharePort>,
    cfg: Arc<NcConfig>,
    limiter: ShareeLimiter,
}

impl ShareesApi {
    pub fn new(shares: Arc<dyn SharePort>, cfg: Arc<NcConfig>) -> Self {
        let per_min = cfg.sharee_rate_per_min;
        Self { shares, cfg, limiter: ShareeLimiter::new(per_min) }
    }

    /// `GET .../sharees?search=&itemType=&perPage=&page=`
    pub fn search(
        &self,
        user: UserId,
        query: &str,
        item_type: Option<&str>,
        page: i64,
        per_page: i64,
        now_s: i64,
    ) -> Result<Val, OcsError> {
        // The reference throws 400 'Missing itemType'.
        if item_type.is_none() {
            return Err(OcsError::bad_request("Missing itemType"));
        }
        if per_page <= 0 {
            return Err(OcsError::bad_request("Invalid perPage argument"));
        }
        if page <= 0 {
            return Err(OcsError::bad_request("Invalid page"));
        }

        // Below the floor: return the empty skeleton with HTTP 200, exactly as
        // the reference does. An error here would let a caller distinguish
        // "too short" from "no matches", which is itself a small signal.
        if query.chars().count() < self.cfg.sharee_min_search {
            return Ok(empty_sharees());
        }
        if self.cfg.sharee_lookup == ShareeLookup::Off {
            return Ok(empty_sharees());
        }
        if !self.limiter.allow(user, now_s) {
            return Err(OcsError::new(429, "Too many autocomplete requests"));
        }

        let scope = match self.cfg.sharee_lookup {
            ShareeLookup::SameGroup => GranteeScope::SameGroup,
            ShareeLookup::All => GranteeScope::All,
            ShareeLookup::Off => GranteeScope::Off,
        };

        let found = self
            .shares
            .find_grantees(user, query, scope)
            .map_err(port_err)?;

        let limit = per_page as usize;
        let offset = ((page - 1) * per_page) as usize;

        let mut exact_users = Vec::new();
        let mut exact_groups = Vec::new();
        let mut wide_users = Vec::new();
        let mut wide_groups = Vec::new();

        for c in found.into_iter().skip(offset).take(limit) {
            let entry = sharee_entry(&c);
            match (c.kind, c.exact) {
                (GranteeKind::User, true) => exact_users.push(entry),
                (GranteeKind::User, false) => wide_users.push(entry),
                (GranteeKind::Group, true) => exact_groups.push(entry),
                (GranteeKind::Group, false) => wide_groups.push(entry),
                // A public link is not a searchable principal.
                (GranteeKind::Link, _) => {}
            }
        }

        Ok(sharees_result(
            exact_users,
            exact_groups,
            wide_users,
            wide_groups,
        ))
    }
}

fn sharee_entry(c: &GranteeCandidate) -> Val {
    let st = grantee_kind_to_share_type(c.kind);
    Val::map([
        ("label", Val::str(c.display.clone())),
        (
            "subline",
            Val::str(c.subline.clone().unwrap_or_default()),
        ),
        (
            "icon",
            Val::str(match c.kind {
                GranteeKind::User => "icon-user",
                GranteeKind::Group => "icon-group",
                GranteeKind::Link => "icon-public",
            }),
        ),
        (
            "value",
            Val::map([
                ("shareType", Val::Int(st)),
                ("shareWith", Val::str(c.id.clone())),
            ]),
        ),
        ("shareWithDisplayNameUnique", Val::str(c.id.clone())),
        // The reference emits `[]` (an empty PHP array) when the user has no
        // status, which serialises as a JSON array, not an object. Reproduce
        // it: a typed client deserialiser that expects an object here breaks.
        ("status", Val::empty_list()),
    ])
}

fn empty_sharees() -> Val {
    sharees_result(Vec::new(), Vec::new(), Vec::new(), Vec::new())
}

/// The full skeleton. Every bucket must be present even when empty — the
/// reference initialises all of them up front and clients index into them
/// unconditionally.
fn sharees_result(
    exact_users: Vec<Val>,
    exact_groups: Vec<Val>,
    users: Vec<Val>,
    groups: Vec<Val>,
) -> Val {
    Val::map([
        (
            "exact",
            Val::map([
                ("users", Val::List(exact_users)),
                ("groups", Val::List(exact_groups)),
                ("remotes", Val::empty_list()),
                ("remote_groups", Val::empty_list()),
                ("emails", Val::empty_list()),
                ("circles", Val::empty_list()),
                ("rooms", Val::empty_list()),
            ]),
        ),
        ("users", Val::List(users)),
        ("groups", Val::List(groups)),
        ("remotes", Val::empty_list()),
        ("remote_groups", Val::empty_list()),
        ("emails", Val::empty_list()),
        ("lookup", Val::empty_list()),
        ("circles", Val::empty_list()),
        ("rooms", Val::empty_list()),
        // Never true: consulting a global lookup server would publish our
        // account names off-box.
        ("lookupEnabled", Val::Bool(false)),
    ])
}

// ---------------------------------------------------------------------------
// the API surface
// ---------------------------------------------------------------------------

pub struct SharesApi {
    shares: Arc<dyn SharePort>,
}

impl SharesApi {
    pub fn new(shares: Arc<dyn SharePort>) -> Self {
        Self { shares }
    }

    pub fn index(
        &self,
        user: UserId,
        filter: &crate::ports::ShareFilter,
    ) -> Result<Val, OcsError> {
        let list = self.shares.list(user, filter).map_err(port_err)?;
        Ok(Val::List(
            list.iter()
                .map(|s| format_share(s, self.shares.as_ref()))
                .collect(),
        ))
    }

    pub fn create(&self, user: UserId, req: &ShareRequest) -> Result<Val, OcsError> {
        let spec = req.to_spec()?;
        let s = self.shares.create(user, &spec).map_err(port_err)?;
        Ok(format_share(&s, self.shares.as_ref()))
    }

    pub fn update(&self, user: UserId, id: u64, req: &ShareRequest) -> Result<Val, OcsError> {
        // An update with nothing to change is a 400 in the reference; mirror
        // that rather than performing a no-op write.
        if req.permissions.is_none()
            && req.password.is_none()
            && req.expire_date.is_none()
            && req.note.is_none()
            && req.label.is_none()
        {
            return Err(OcsError::bad_request("Wrong or no update parameter given"));
        }
        let existing = self.shares.get(user, id).map_err(port_err)?;
        let perms = match req.permissions {
            Some(bits) => nc_bits_to_perms(bits).map_err(|u| {
                OcsError::bad_request(format!("Unsupported permission bits 0x{u:x}"))
            })?,
            None => existing.perms,
        };
        let spec = ShareSpec {
            path: existing.path.clone(),
            kind: existing.kind,
            grantee: existing.grantee.clone(),
            perms,
            password: req.password.clone().filter(|p| !p.is_empty()),
            expires_s: match &req.expire_date {
                Some(_) => expire_date_to_unix(req.expire_date.as_deref())?,
                None => existing.expires_s,
            },
            label: req.label.clone().or(Some(existing.label.clone())),
            note: req.note.clone().or(Some(existing.note.clone())),
        };
        let s = self.shares.update(user, id, &spec).map_err(port_err)?;
        Ok(format_share(&s, self.shares.as_ref()))
    }

    /// The reference returns `DataResponse()` with no argument, which
    /// serialises as an empty **array**.
    pub fn delete(&self, user: UserId, id: u64) -> Result<Val, OcsError> {
        self.shares.delete(user, id).map_err(port_err)?;
        Ok(Val::empty_list())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::{FileId, Perms, PortResult, ShareFilter, ShareId};

    struct FakeShares;
    impl SharePort for FakeShares {
        fn list(&self, _u: UserId, _f: &ShareFilter) -> PortResult<Vec<CoreShare>> {
            Ok(vec![link_share()])
        }
        fn get(&self, _u: UserId, _id: u64) -> PortResult<CoreShare> {
            Ok(link_share())
        }
        fn create(&self, _u: UserId, _s: &ShareSpec) -> PortResult<CoreShare> {
            Ok(link_share())
        }
        fn update(&self, _u: UserId, _id: u64, _s: &ShareSpec) -> PortResult<CoreShare> {
            Ok(link_share())
        }
        fn delete(&self, _u: UserId, _id: u64) -> PortResult<()> {
            Ok(())
        }
        fn kinds_for(&self, _s: ShareId, _id: FileId) -> PortResult<Vec<GranteeKind>> {
            Ok(vec![])
        }
        fn find_grantees(
            &self,
            _u: UserId,
            q: &str,
            _s: GranteeScope,
        ) -> PortResult<Vec<GranteeCandidate>> {
            Ok(vec![GranteeCandidate {
                kind: GranteeKind::User,
                id: format!("{q}bot"),
                display: format!("{q} Bot"),
                subline: None,
                exact: false,
            }])
        }
        fn link_url(&self, token: &str) -> String {
            format!("https://cloud.example.com/s/{token}")
        }
    }

    fn link_share() -> CoreShare {
        CoreShare {
            id: 12,
            kind: GranteeKind::Link,
            grantee: None,
            grantee_display: None,
            owner: "alice".into(),
            owner_display: "Alice".into(),
            perms: Perms::READ | Perms::DOWNLOAD,
            created_s: 1_753_600_000,
            expires_s: Some(1_787_788_800), // 2026-08-27 00:00:00 UTC
            token: Some("aB3xQ".into()),
            has_password: true,
            label: String::new(),
            note: String::new(),
            path: "/photos/summer".into(),
            kind_is_dir: true,
            file_id: FileId(123),
            parent_file_id: Some(FileId(1)),
        }
    }

    #[test]
    fn unsupported_share_types_are_rejected_with_400_not_dropped() {
        for t in [2i64, 4, 6, 7, 8, 9, 10, 12, 15] {
            let e = share_type_to_kind(t).unwrap_err();
            assert_eq!(e.code, 400, "share type {t} must be a 400");
        }
        assert!(share_type_to_kind(0).is_ok());
        assert!(share_type_to_kind(1).is_ok());
        assert!(share_type_to_kind(3).is_ok());
        assert_eq!(share_type_to_kind(-1).unwrap_err().code, 400);
        assert_eq!(share_type_to_kind(99).unwrap_err().code, 400);
    }

    #[test]
    fn unknown_permission_bits_are_rejected() {
        let r = ShareRequest {
            path: Some("/x".into()),
            share_type: Some(3),
            permissions: Some(31 | 32),
            ..ShareRequest::default()
        };
        let e = r.to_spec().unwrap_err();
        assert_eq!(e.code, 400);
        assert!(e.message.contains("permission"));
    }

    #[test]
    fn link_share_serialises_with_the_reference_types() {
        let j = format_share(&link_share(), &FakeShares).to_json();
        // id is a STRING.
        assert_eq!(j["id"], "12");
        assert_eq!(j["share_type"], 3);
        assert_eq!(j["permissions"], 1);
        assert_eq!(j["item_type"], "folder");
        assert_eq!(j["mimetype"], "httpd/unix-directory");
        assert_eq!(j["url"], "https://cloud.example.com/s/aB3xQ");
        // Password is redacted, never echoed.
        assert_eq!(j["password"], "redacted");
        assert_eq!(j["share_with"], "redacted");
        // mail_send / hide_download are ints, not bools.
        assert_eq!(j["mail_send"], 0);
        assert_eq!(j["hide_download"], 0);
        assert!(j["parent"].is_null());
        assert_eq!(j["expiration"], "2026-08-27 00:00:00");
        // can_edit/can_delete/has_preview are real bools.
        assert!(j["can_edit"].is_boolean());
        assert!(j["has_preview"].is_boolean());
    }

    #[test]
    fn a_link_without_a_password_reports_null_not_redacted() {
        let mut s = link_share();
        s.has_password = false;
        let j = format_share(&s, &FakeShares).to_json();
        assert!(j["password"].is_null());
    }

    #[test]
    fn expiration_format_is_space_separated_not_rfc3339() {
        assert_eq!(format_expiration(0), "1970-01-01 00:00:00");
        assert_eq!(format_expiration(1_753_600_000), "2025-07-27 07:06:40");
        assert!(!format_expiration(1_753_600_000).contains('T'));
    }

    #[test]
    fn expire_date_roundtrip() {
        // Round-trip against the formatter, so the two civil-date conversions
        // are pinned to each other rather than to a hand-computed constant.
        for d in ["1970-01-01", "2000-02-29", "2026-08-27", "2038-01-19"] {
            let t = expire_date_to_unix(Some(d)).unwrap().unwrap();
            assert_eq!(format_expiration(t), format!("{d} 00:00:00"));
        }
        assert_eq!(expire_date_to_unix(Some("2026-08-27")).unwrap(), Some(1_787_788_800));
        assert_eq!(expire_date_to_unix(None).unwrap(), None);
        assert_eq!(expire_date_to_unix(Some("")).unwrap(), None);
        assert!(expire_date_to_unix(Some("27/08/2026")).is_err());
        assert!(expire_date_to_unix(Some("2026-13-01")).is_err());
    }

    #[test]
    fn sharees_requires_three_characters() {
        let cfg = Arc::new(NcConfig::default());
        let api = ShareesApi::new(Arc::new(FakeShares), cfg);
        // Below the floor: 200 with an empty skeleton, not an error and not
        // results.
        let j = api.search(UserId(1), "ab", Some("file"), 1, 20, 0).unwrap().to_json();
        assert_eq!(j["users"].as_array().unwrap().len(), 0);
        assert_eq!(j["lookupEnabled"], false);

        let j = api.search(UserId(1), "abc", Some("file"), 1, 20, 0).unwrap().to_json();
        assert_eq!(j["users"].as_array().unwrap().len(), 1);
        assert_eq!(j["users"][0]["value"]["shareWith"], "abcbot");
        assert_eq!(j["users"][0]["value"]["shareType"], 0);
        // status is an empty ARRAY when absent, matching the reference.
        assert!(j["users"][0]["status"].is_array());
    }

    #[test]
    fn sharees_skeleton_has_every_bucket() {
        let j = empty_sharees().to_json();
        for k in [
            "users",
            "groups",
            "remotes",
            "remote_groups",
            "emails",
            "lookup",
            "circles",
            "rooms",
        ] {
            assert!(j[k].is_array(), "top-level bucket {k} missing");
        }
        for k in [
            "users",
            "groups",
            "remotes",
            "remote_groups",
            "emails",
            "circles",
            "rooms",
        ] {
            assert!(j["exact"][k].is_array(), "exact bucket {k} missing");
        }
    }

    #[test]
    fn sharees_is_rate_limited() {
        let cfg = NcConfig {
            sharee_rate_per_min: 3,
            ..NcConfig::default()
        };
        let api = ShareesApi::new(Arc::new(FakeShares), Arc::new(cfg));
        for _ in 0..3 {
            assert!(api.search(UserId(1), "abc", Some("file"), 1, 20, 0).is_ok());
        }
        let e = api.search(UserId(1), "abc", Some("file"), 1, 20, 0).unwrap_err();
        assert_eq!(e.code, 429);
        // A different user has their own budget.
        assert!(api.search(UserId(2), "abc", Some("file"), 1, 20, 0).is_ok());
        // ...and the window rolls over.
        assert!(api.search(UserId(1), "abc", Some("file"), 1, 20, 60).is_ok());
    }

    #[test]
    fn sharees_argument_validation() {
        let api = ShareesApi::new(Arc::new(FakeShares), Arc::new(NcConfig::default()));
        assert_eq!(
            api.search(UserId(1), "abc", None, 1, 20, 0).unwrap_err().code,
            400
        );
        assert_eq!(
            api.search(UserId(1), "abc", Some("file"), 0, 20, 0).unwrap_err().code,
            400
        );
        assert_eq!(
            api.search(UserId(1), "abc", Some("file"), 1, 0, 0).unwrap_err().code,
            400
        );
    }

    #[test]
    fn sharee_lookup_off_returns_nothing() {
        let cfg = NcConfig {
            sharee_lookup: ShareeLookup::Off,
            ..NcConfig::default()
        };
        let api = ShareesApi::new(Arc::new(FakeShares), Arc::new(cfg));
        let j = api.search(UserId(1), "abcdef", Some("file"), 1, 20, 0).unwrap().to_json();
        assert_eq!(j["users"].as_array().unwrap().len(), 0);
    }

    #[test]
    fn delete_returns_an_empty_array() {
        let api = SharesApi::new(Arc::new(FakeShares));
        assert!(api.delete(UserId(1), 12).unwrap().to_json().as_array().unwrap().is_empty());
    }

    #[test]
    fn create_without_a_path_is_404_before_share_type_is_examined() {
        let api = SharesApi::new(Arc::new(FakeShares));
        let r = ShareRequest::default();
        assert_eq!(api.create(UserId(1), &r).unwrap_err().code, 404);
    }
}
