//! `sc-auth` — protocol-agnostic authentication/authorization core.
//! See `docs/DESIGN-AUTH.md` for the full design this implements.

mod app_password;
mod argon_gate;
pub mod audit;
mod basic;
pub mod config;
mod cred_cache;
mod crockford;
mod db;
mod groups;
mod login;
mod nt_hash;
mod nt_ops;
mod oidc;
pub mod password;
mod rate_limit;
mod rotate;
mod session;
mod totp;
mod users;

pub use audit::{AuditFilter, AuditRow};
pub use config::{AuthConfig, DavAccountPassword, OidcLocalPasswordLogin, SmbTotpPolicy};
pub use nt_ops::PassdbSink;
pub use oidc::{
    NewOidcFlow, OidcFlow, OidcFlowMode, OidcIdentity, OidcLinkError, OidcUnlink, OidcUnlinkError,
    OIDC_FLOW_TTL,
};
pub use rotate::RotationReport;
pub use session::{token_hash_hex, AMR_OIDC, AMR_PASSWORD, AMR_RECOVERY, AMR_TOTP};

use anyhow::Context;
use argon_gate::ArgonGate;
use cred_cache::{ConnMemo, CredCache, TokenCache};
use sc_vfs::{ShareId, UserId};
use rate_limit::{AccountGate, IpGate};
use std::collections::HashMap;
use std::path::Path;
use std::sync::atomic::AtomicU64;
use std::sync::Arc;
use std::time::Instant;

/// Auth database + all in-memory state (caches, rate gates, the Argon2
/// concurrency semaphore). Cheap to clone behind an `Arc` in the HTTP layer
/// — construct exactly one per process.
pub struct AuthService {
    pool: db::DbPool,
    cfg: AuthConfig,
    master_key: [u8; 32],

    /// Gates *every* Argon2 invocation in the crate — hashing and
    /// verifying, sync and async callers alike — to the configured
    /// concurrency (DESIGN-AUTH §2.2). See `argon_gate` module docs for why
    /// this can't just be a `tokio::sync::Semaphore`.
    argon2_gate: Arc<ArgonGate>,
    /// Count of Argon2 invocations. Not part of the documented contract;
    /// exposed via `argon2_calls()` purely so tests can assert the cred
    /// cache is actually skipping Argon2 on a hit.
    argon2_calls: AtomicU64,
    dummy_hash: String,

    cred_cache: CredCache,
    conn_memo: ConnMemo,
    token_cache: TokenCache,
    app_pw_last_write: parking_lot::Mutex<HashMap<[u8; 32], Instant>>,

    ip_gate: IpGate,
    account_gate: AccountGate,

    generation: AtomicU64,

    /// Installed once by the process that can actually write Samba's files.
    /// See [`nt_ops::PassdbSink`] for why deleting an NT hash is only half
    /// the job.
    passdb_sink: std::sync::OnceLock<Arc<dyn nt_ops::PassdbSink>>,
}

impl AuthService {
    pub fn new(db_path: &Path, mut cfg: AuthConfig, master_key: [u8; 32]) -> anyhow::Result<Self> {
        let pool = db::open_pool(db_path)?;

        // Refuse to start on a key that cannot decrypt what is already on
        // disk (`FEATURES.md` #156) — before anything else touches
        // `master_key`, so a bad key never gets the chance to look like a
        // working one until some later, unrelated request fails.
        rotate::verify_master_key(&pool, &master_key)?;

        // The persisted counter, not `AuthConfig::default`'s hardcoded `1` —
        // `rotate_master_key` bumps this in the database; every fresh
        // `AuthService::new` must pick that up, or a rotated deployment
        // would keep sealing new secrets under the version number it just
        // rotated away from.
        cfg.key_ver = {
            let conn = pool.get().context("getting a connection to read key_ver")?;
            db::current_key_version(&conn)?
        };

        let argon2_gate = Arc::new(ArgonGate::new(cfg.argon2_parallelism));
        // Startup only, single-threaded — but routed through the gate too
        // so *every* Argon2 call in the crate has exactly one path, no
        // exceptions to remember.
        let dummy_hash = {
            let _permit = argon2_gate.acquire();
            password::make_dummy_hash(&cfg)?
        };
        let cred_cache = CredCache::new(
            cfg.cred_cache_cap,
            cfg.cred_cache_pos_ttl,
            cfg.cred_cache_pos_idle,
            cfg.cred_cache_neg_ttl,
        );
        let conn_memo = ConnMemo::new(cfg.conn_memo_cap);
        let token_cache = TokenCache::new(cfg.token_cache_cap, cfg.token_cache_ttl);
        let ip_gate = IpGate::new(cfg.rate_ip_capacity, cfg.rate_ip_refill);
        let account_gate = AccountGate::new(cfg.rate_account_refill);

        Ok(Self {
            pool,
            cfg,
            master_key,
            argon2_gate,
            argon2_calls: AtomicU64::new(0),
            dummy_hash,
            cred_cache,
            conn_memo,
            token_cache,
            app_pw_last_write: parking_lot::Mutex::new(HashMap::new()),
            ip_gate,
            account_gate,
            generation: AtomicU64::new(0),
            passdb_sink: std::sync::OnceLock::new(),
        })
    }

    pub fn generation(&self) -> u64 {
        self.generation.load(std::sync::atomic::Ordering::SeqCst)
    }

    /// Number of Argon2 invocations since construction. Not part of the
    /// public contract in `docs/DESIGN-AUTH.md`; used by tests (and
    /// available for metrics) to prove the DAV credential cache is actually
    /// skipping Argon2 on repeat requests.
    pub fn argon2_calls(&self) -> u64 {
        self.argon2_calls.load(std::sync::atomic::Ordering::SeqCst)
    }

    /// The configured minimum account-password length (`DESIGN-AUTH.md`
    /// §2.3: 10). Exposed so callers that validate a password *before*
    /// handing it to `create_user`/`set_password` — the first-run bootstrap
    /// does, so it can answer `422` with the requirement rather than a bare
    /// failure — quote this number instead of hardcoding their own.
    pub fn min_password_len(&self) -> usize {
        self.cfg.min_password_len
    }

    /// Re-encrypts every SMB NT hash and TOTP secret under `new_key` and
    /// bumps the persisted `key_ver`, all inside one SQLite transaction —
    /// see `rotate` module doc for the interrupt-safety argument. `self`'s
    /// own `master_key`/`cfg.key_ver` are intentionally left untouched: the
    /// caller (the `sc-server` CLI) constructs a fresh `AuthService` against
    /// the swapped key file for anything after this returns, rather than
    /// mutating a live instance mid-process.
    pub fn rotate_master_key(&self, new_key: &[u8; 32]) -> anyhow::Result<RotationReport> {
        rotate::rotate(&self.pool, &self.master_key, new_key)
    }
}

// ---------------------------------------------------------------- Types --

#[derive(Clone, Debug)]
pub enum LoginResult {
    Ok(UserId),
    TotpRequired { challenge: String },
    Invalid,
    RateLimited { retry_after_s: u32 },
}

#[derive(Clone, Debug)]
pub enum BasicResult {
    Ok(Principal),
    Invalid,
    AppPasswordRequired,
    RateLimited { retry_after_s: u32 },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AuthVia {
    Session,
    AppPassword(u32),
    AccountPassword,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Scope {
    pub perms_mask: Option<u16>,
    pub shares: Option<Vec<ShareId>>,
}

#[derive(Clone, Debug)]
pub struct Principal {
    pub user: UserId,
    pub scope: Scope,
    pub via: AuthVia,
}

#[derive(Clone, Debug)]
pub struct UserRow {
    pub id: UserId,
    pub name: String,
    pub display: Option<String>,
    pub disabled: bool,
    pub totp_enabled: bool,
    pub smb_opt_out: bool,
    pub smb_enabled: bool,
    pub created_ns: i64,
    /// Backed by the real `user.role` column (`db.rs`'s `migrate_user_role`
    /// brings it to every pre-existing database too) — no longer the
    /// `id.get() == 1` stand-in this field used to be computed as before a
    /// role model existed. `AuthService::create_user` always creates a plain
    /// user; only `AuthService::set_admin` (called explicitly by the
    /// first-run bootstrap in `sc-server::setup` for account #1, and by the
    /// admin user-management API for any account after that) flips it.
    pub is_admin: bool,
    /// `user.quota_bytes` — `None` (column `NULL`) means unlimited.
    /// this is a reporting gate on top of the
    /// existing physical-quota machinery, not a usage-tracking cap; nothing
    /// in this crate enforces it against writes.
    pub quota_bytes: Option<u64>,
    /// `user.usage_bytes` — the running ledger `sc_core::QuotaSink` charges
    /// against (`FEATURES.md` #49). Not a live filesystem recomputation; see
    /// `sc_core::quota`'s module doc for what can make it drift.
    pub usage_bytes: u64,
}

/// What `AuthService::quota_status` reports for one account: the ledger
/// alongside the cap it's checked against, so a caller never has to
/// cross-reference two separate calls to answer "how full is this user".
#[derive(Clone, Copy, Debug)]
pub struct QuotaStatus {
    pub used: u64,
    /// `None` = unlimited (`user.quota_bytes IS NULL`).
    pub limit: Option<u64>,
}

/// One `group_` row (`FEATURES.md` #48). Membership itself is a separate
/// `(user, group_)` table — see `AuthService::list_group_members`/
/// `list_memberships_all` — not a field here, the same reason `UserRow`
/// doesn't carry a user's grants inline.
#[derive(Clone, Debug)]
pub struct GroupRow {
    pub id: sc_vfs::GroupId,
    pub name: String,
}

/// [`AuthService::create_group`]/`rename_group` outcomes — `group_.name` is
/// `UNIQUE`, same shape as [`CreateUserError`] for the identical reason.
/// `NotFound` only ever comes back from `rename_group` (there is no id to
/// miss when creating one); kept on this one enum rather than a fourth error
/// type, same precedent as [`AdminGuardError::LastAdmin`] not applying to
/// every method that returns it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GroupNameError {
    DuplicateName,
    NotFound,
    Internal(String),
}

impl std::fmt::Display for GroupNameError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GroupNameError::DuplicateName => write!(f, "a group with that name already exists"),
            GroupNameError::NotFound => write!(f, "no such group"),
            GroupNameError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for GroupNameError {}

/// [`AuthService::delete_group`]/`add_membership`/`remove_membership`
/// outcomes — no last-admin-style guard applies to a group, only "does the
/// row exist".
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GroupOpError {
    NotFound,
    Internal(String),
}

impl std::fmt::Display for GroupOpError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GroupOpError::NotFound => write!(f, "no such group"),
            GroupOpError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for GroupOpError {}

/// Guarded outcomes shared by every admin user-management operation that can
/// either target a nonexistent account or strip the deployment of its last
/// administrator (`set_admin(_, false)`, `disable_user(_, true)`,
/// `delete_user`). Kept distinct from a bare `anyhow::Error` for the same
/// reason [`ChangePasswordError`] is: the HTTP layer needs to tell these
/// apart (`404` vs `409` vs `500`) without string-matching a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AdminGuardError {
    /// No account has that id.
    NoSuchUser,
    /// This account is the deployment's last *active* administrator
    /// (`role = admin`, `disabled = 0`) — refused unconditionally. Locking
    /// every administrator out is strictly worse than the "no lockout"
    /// principle `DESIGN-AUTH.md` §7.1 already applies to failed logins: a
    /// mistaken click here would leave nobody able to administer the
    /// deployment at all, and there is no self-service recovery path.
    LastAdmin,
    /// A storage-layer failure unrelated to either guard above.
    Internal(String),
}

impl std::fmt::Display for AdminGuardError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AdminGuardError::NoSuchUser => write!(f, "no such user"),
            AdminGuardError::LastAdmin => write!(f, "refusing to remove the last administrator"),
            AdminGuardError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for AdminGuardError {}

/// Error surface for [`AuthService::create_user`] — distinct from a bare
/// `anyhow::Error` for the same reason [`ChangePasswordError`] is: the admin
/// user-creation HTTP handler needs to tell "too short" (`422`) apart from
/// "that name is taken" (`409`) apart from everything else (`500`) without
/// string-matching a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CreateUserError {
    TooShort { min: usize },
    /// `user.name` is `UNIQUE ... COLLATE NOCASE` — this is a SQLite
    /// constraint violation on that column, not a pre-check race (`db.rs`).
    DuplicateName,
    Internal(String),
}

impl std::fmt::Display for CreateUserError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CreateUserError::TooShort { min } => write!(f, "password too short: minimum {min} characters"),
            CreateUserError::DuplicateName => write!(f, "a user with that name already exists"),
            CreateUserError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for CreateUserError {}

/// Error surface for [`AuthService::change_password`] — distinct from a bare
/// `anyhow::Error` so the HTTP layer can tell "wrong current password" (a
/// `401`, same family as a failed login) apart from "new password too short"
/// (a `422` with the minimum length in `detail`, same shape as
/// `setup.weak_password`) without string-matching an error message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ChangePasswordError {
    BadCurrentPassword,
    TooShort { min: usize },
}

/// Error surface for [`AuthService::reissue_recovery_codes`] — distinct from
/// a bare `anyhow::Error` so the HTTP layer can tell "wrong password" (a
/// `401`, same family as a failed login) apart from "TOTP isn't enabled on
/// this account" (reissuing recovery codes makes no sense without it — a
/// `409`) apart from an unrelated storage failure (`500`), without
/// string-matching an error message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RecoveryReissueError {
    BadPassword,
    TotpNotEnabled,
    Internal(String),
}

impl std::fmt::Display for RecoveryReissueError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RecoveryReissueError::BadPassword => write!(f, "bad password"),
            RecoveryReissueError::TotpNotEnabled => write!(f, "totp is not enabled"),
            RecoveryReissueError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for RecoveryReissueError {}

/// A freshly generated, **not yet persisted** TOTP secret plus the
/// `otpauth://` URL a client renders as a QR code. Nothing is written to the
/// database until the caller proves possession via
/// [`AuthService::totp_enroll`] (secret + current code + password
/// reconfirmation) — this step alone cannot enable 2FA on an account.
#[derive(Clone, Debug)]
pub struct TotpSetup {
    /// Base32, for manual entry when a QR scan isn't convenient.
    pub secret: String,
    pub otpauth_url: String,
}

/// Plaintext session token — shown to the caller exactly once, at creation.
/// Only `sha256(token)` is ever persisted.
#[derive(Clone, Debug)]
pub struct SessionToken(pub String);

impl SessionToken {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl std::fmt::Display for SessionToken {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}

#[derive(Clone, Debug)]
pub struct SessionInfo {
    pub id_hash_hex: String,
    pub created_ns: i64,
    pub last_seen_ns: i64,
    pub absolute_expiry_ns: i64,
    pub ip_first: Option<String>,
    pub ua_first: Option<String>,
    pub amr: u32,
}

#[derive(Clone, Debug)]
pub struct AppPwInfo {
    pub id: u32,
    pub name: String,
    pub scope_perms: Option<u16>,
    pub created_ns: i64,
    pub last_used_ns: Option<i64>,
    pub expires_ns: Option<i64>,
}

#[cfg(test)]
mod tests;
