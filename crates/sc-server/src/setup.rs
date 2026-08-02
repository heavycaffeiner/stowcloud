//! First-run bootstrap (`DESIGN-AUTH.md` §8): a one-time setup token, printed
//! to stdout and written to `<data>/setup-token` (mode `0600`, best-effort
//! outside Unix), expiring after 15 minutes — and [`SetupGate`], the thing
//! that actually spends it and creates the administrator account.
//!
//! ## What "permanently disabled" means here
//!
//! The design says the route and the token are *permanently* disabled once the
//! admin account is created, and that a restart re-issues the token only while
//! there is still no admin. Those two sentences together rule out a
//! process-local `bool` as the source of truth: a flag set in memory is gone
//! after a restart, and the next boot would happily mint a fresh token for a
//! server that already has an administrator — an unauthenticated
//! admin-creation endpoint reopening itself on every restart.
//!
//! So the authoritative answer to "is setup closed?" is **"does an account
//! exist?"**, read from the auth database on every call. It is durable by
//! construction, it cannot drift from the thing it is describing (an account
//! either exists or it does not), and it needs no new column, no marker file
//! and no migration. The alternatives were considered and rejected:
//!
//! * a `setup_completed` row/marker file — a second copy of a fact the `user`
//!   table already states, which can be deleted or restored out of step with
//!   the accounts it claims to describe. A restored-from-backup data dir with
//!   the marker missing would reopen admin creation on a live system.
//! * an in-memory flag alone — does not survive the restart the design
//!   explicitly discusses.
//!
//! The in-memory token slot still exists, but only as the *narrower* of two
//! gates: it makes the token single-use within a process even under concurrent
//! requests, and it is what expiry is checked against. The account check is
//! what makes the closure permanent.
//!
//! Because the answer is derived rather than stored, "there is no admin
//! account" is also literally "there are no accounts" — this deployment has no
//! role column, so the first account *is* the administrator (`app.rs`'s grant
//! projection gives every enabled account a full grant on every share). If a
//! role model lands later, this predicate is the one place that changes.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use sc_http::setup_api::{SetupApi, SetupError, SetupOutcome};
use parking_lot::Mutex;
use secrecy::{ExposeSecret, SecretString};
use subtle::ConstantTimeEq;

/// 32 bytes — the 256 bits `DESIGN-AUTH.md` §8 asks for.
const TOKEN_BYTES: usize = 32;
const EXPIRY_SECS: i64 = 15 * 60;

/// Login names are the identity every other subsystem keys off, so the
/// characters that would break one of them are refused rather than escaped.
/// `:` in particular is the field separator in both HTTP Basic credentials
/// and the `smbpasswd(5)` line `sc-auth` exports.
const USERNAME_EXTRA: &str = ".-_@+";
const USERNAME_MAX_CHARS: usize = 64;

#[derive(Debug, Clone)]
pub struct SetupToken {
    pub token: String,
    pub expires_at_unix: i64,
}

impl SetupToken {
    pub fn is_expired_at(&self, now_unix: i64) -> bool {
        now_unix >= self.expires_at_unix
    }
}

fn now_unix() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

fn token_path(data_dir: &Path) -> PathBuf {
    data_dir.join("setup-token")
}

/// Generate a fresh setup token, print it to stdout, and persist it.
pub fn generate(data_dir: &Path) -> anyhow::Result<SetupToken> {
    let mut raw = [0u8; TOKEN_BYTES];
    getrandom::getrandom(&mut raw).map_err(|e| anyhow::anyhow!("getrandom failed: {e}"))?;
    let token = data_encoding::BASE32_NOPAD.encode(&raw).to_lowercase();
    let expires_at_unix = now_unix() + EXPIRY_SECS;
    let t = SetupToken {
        token,
        expires_at_unix,
    };

    std::fs::create_dir_all(data_dir)?;
    let path = token_path(data_dir);
    let content = format!("{}\n{}\n", t.token, t.expires_at_unix);
    std::fs::write(&path, &content)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600));
    }

    println!("=== sc-server first-run setup ===");
    println!("Setup token (expires in 15 minutes): {}", t.token);
    println!("Also written to: {}", path.display());
    println!("Use it at the web UI's first-run screen to create the admin account.");
    println!("No account is created from an environment variable: an initial password");
    println!("passed that way is readable in `docker inspect` and the process list.");

    Ok(t)
}

/// Read back a previously-generated token, if the file exists and parses.
/// Does not itself check expiry — callers decide what to do with a stale one.
pub fn read_existing(data_dir: &Path) -> Option<SetupToken> {
    let content = std::fs::read_to_string(token_path(data_dir)).ok()?;
    let mut lines = content.lines();
    let token = lines.next()?.to_string();
    let expires_at_unix: i64 = lines.next()?.parse().ok()?;
    Some(SetupToken {
        token,
        expires_at_unix,
    })
}

/// Best-effort removal of `<data>/setup-token`. A token that has been spent —
/// or that belongs to a deployment which already has an account — has no
/// business staying readable on disk.
pub fn remove_token_file(data_dir: &Path) {
    let path = token_path(data_dir);
    match std::fs::remove_file(&path) {
        Ok(()) => tracing::info!(path = %path.display(), "setup token file removed"),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => {
            tracing::warn!(path = %path.display(), error = %e, "could not remove setup token file")
        }
    }
}

/// The `/api/setup` gate. See the module docs for how "permanently disabled"
/// is represented.
pub struct SetupGate {
    auth: Arc<sc_auth::AuthService>,
    /// Needed only to re-run the grant projection after the first account is
    /// created — see [`SetupGate::complete`] step (7).
    core: Arc<sc_core::Core>,
    acl: Arc<sc_acl::AclEngine>,
    data_dir: PathBuf,
    /// The token this process issued, or `None` if none was issued or it has
    /// been spent. The lock is held across the entire check-create sequence,
    /// which is what makes "works exactly once" true under concurrency.
    slot: Mutex<Option<SetupToken>>,
}

impl SetupGate {
    /// A closed gate: no token issued. Constructing one has no side effects —
    /// no file is written and nothing is printed — so `App::build` can be
    /// called by `gc`, `smb-sync` and the test suite without minting
    /// credentials as a side effect of assembling the object graph.
    pub fn new(
        auth: Arc<sc_auth::AuthService>,
        core: Arc<sc_core::Core>,
        acl: Arc<sc_acl::AclEngine>,
        data_dir: &Path,
    ) -> Self {
        Self {
            auth,
            core,
            acl,
            data_dir: data_dir.to_path_buf(),
            slot: Mutex::new(None),
        }
    }

    /// Arm the gate for a serving process. Issues (and prints, and persists) a
    /// fresh token iff no account exists yet; otherwise clears any stale token
    /// file left over from an earlier boot. Returns whether a token was
    /// issued.
    ///
    /// This is the "reissued by restarting" half of §8, and the reason it is a
    /// separate call rather than part of construction: re-issuing is a
    /// property of *starting to serve*, not of building an `App`.
    pub fn arm_for_first_run(&self) -> anyhow::Result<bool> {
        if !self.no_account_exists() {
            remove_token_file(&self.data_dir);
            return Ok(false);
        }
        let token = generate(&self.data_dir)?;
        *self.slot.lock() = Some(token);
        Ok(true)
    }

    /// Arm with a caller-supplied token instead of a generated one. Used by
    /// tests to exercise the expiry branch without waiting fifteen minutes.
    pub fn arm_with(&self, token: SetupToken) {
        *self.slot.lock() = Some(token);
    }

    /// The authoritative, restart-surviving gate. Errs on the side of
    /// *closed*: if the account table cannot be read we would rather show a
    /// login screen for a server nobody can log into than expose admin
    /// creation on a database we could not inspect.
    fn no_account_exists(&self) -> bool {
        match self.auth.list_users() {
            Ok(users) => users.is_empty(),
            Err(e) => {
                tracing::error!(error = %e, "could not read the account table; treating setup as closed");
                false
            }
        }
    }
}

fn validate_username(name: &str) -> Result<(), SetupError> {
    if name.is_empty() {
        return Err(SetupError::InvalidUsername("must not be empty"));
    }
    if name.chars().count() > USERNAME_MAX_CHARS {
        return Err(SetupError::InvalidUsername("must be at most 64 characters"));
    }
    if !name
        .chars()
        .all(|c| c.is_alphanumeric() || USERNAME_EXTRA.contains(c))
    {
        return Err(SetupError::InvalidUsername(
            "may contain only letters, digits and . - _ @ +",
        ));
    }
    Ok(())
}

impl SetupApi for SetupGate {
    fn is_required(&self) -> bool {
        self.no_account_exists()
    }

    fn complete(
        &self,
        token: &str,
        username: &str,
        password: &SecretString,
        ip: std::net::IpAddr,
    ) -> Result<SetupOutcome, SetupError> {
        // One lock for the whole sequence. Two requests arriving with the
        // same valid token cannot both pass the `slot.take()` below, so the
        // token works exactly once even if the second request is already in
        // flight when the first commits.
        let mut slot = self.slot.lock();

        // (1) The durable gate first: an account exists, so setup is over,
        //     no matter what this process is holding in memory.
        if !self.no_account_exists() {
            *slot = None;
            remove_token_file(&self.data_dir);
            self.audit_failure(ip, "already_completed");
            return Err(SetupError::Completed);
        }

        // (2) Expiry is checked *before* the token is compared, so a caller
        //     arriving after the window learns only that the window closed —
        //     never whether the value they presented was the right one. An
        //     absent slot lands here too: no token is in circulation and the
        //     operator's remedy is the same, restart to issue a new one.
        let Some(issued) = slot.as_ref() else {
            return Err(SetupError::Expired);
        };
        if issued.is_expired_at(now_unix()) {
            self.audit_failure(ip, "token_expired");
            return Err(SetupError::Expired);
        }

        // (3) Timing-safe comparison, using the same `subtle::ConstantTimeEq`
        //     the share-link password check and the signed-URL signature
        //     check already use. `==` on `String` short-circuits at the first
        //     differing byte and would leak a matching prefix one request at
        //     a time.
        if issued.token.as_bytes().ct_eq(token.as_bytes()).unwrap_u8() != 1 {
            self.audit_failure(ip, "invalid_token");
            return Err(SetupError::InvalidToken);
        }

        // (4) Validate the *inputs* only after the token is known good, and
        //     before the token is spent. A rejected password must not burn
        //     the operator's one chance — they get the 422 and retry with the
        //     same token.
        validate_username(username)?;
        let min_len = self.auth.min_password_len();
        if password.expose_secret().chars().count() < min_len {
            self.audit_failure(ip, "weak_password");
            return Err(SetupError::WeakPassword { min_len });
        }

        // (5) Create. `sc_auth::create_user` re-checks the length, writes the
        //     Argon2 hash, and derives the NT hash unconditionally
        //     (`DESIGN-AUTH.md` §2.4) so SMB can be switched on later without
        //     a password reset.
        let user_id = match self.auth.create_user(username, password) {
            Ok(id) => id,
            Err(e) => {
                tracing::error!(error = %e, "first-run account creation failed");
                self.audit_failure(ip, "create_failed");
                // The token is deliberately *not* spent: nothing was created,
                // so the operator can fix the input and retry.
                return Err(SetupError::Internal);
            }
        };
        // `create_user` never grants the role itself (an admin-created
        // second account must not silently become an administrator too) —
        // account #1 needs it explicitly, which is exactly what "the first
        // account is the administrator" means now that a real role column
        // exists. The guard on `set_admin(_, false)` cannot fire for `true`,
        // so the only realistic failure here is the same storage problem
        // `create_user` above already guards against.
        if let Err(e) = self.auth.set_admin(user_id, true) {
            tracing::error!(error = %e, user = user_id.get(), "granting the admin role to the first account failed");
            self.audit_failure(ip, "create_failed");
            return Err(SetupError::Internal);
        }

        // (6) Spend. The in-memory token goes first, then the file. From here
        //     on step (1) answers `Completed` on its own, which is what makes
        //     this survive a restart.
        *slot = None;
        remove_token_file(&self.data_dir);

        // (7) Give the new account its grants. This is the *bootstrap*
        //     administrator of a brand-new deployment — by definition, no
        //     account existed before it, so `sc_core::Core::migrate_legacy_grants`
        //     (the one-time legacy-projection seed `project_grants` below
        //     also runs) has nothing to preserve and, correctly, grants it
        //     nothing: `acl_store.rs`'s module doc explains why "no access"
        //     is the deliberate default for every account this deployment
        //     has never seen before, this one included.
        //
        //     Without an explicit seed here the administrator we just
        //     created logs in successfully and sees an empty file list with
        //     every configured share invisible, which reads as "the server
        //     is broken" rather than "you have no permissions" — found by
        //     doing exactly that. `seed_full_access` is the same one-time,
        //     idempotent, explicitly-persisted grant the legacy migration
        //     gives an *existing* deployment's accounts; this is its
        //     brand-new-deployment counterpart: the built-in-administrator
        //     convenience an operator expects on first login, without
        //     reintroducing "everyone gets everything" for accounts created
        //     afterward.
        //
        //     Both failures are logged, not returned: the account exists and
        //     the token is spent, so failing the request would tell the
        //     operator to retry something that can no longer succeed.
        if let Err(e) = self.core.seed_full_access(user_id) {
            tracing::error!(error = %e, "first-run account created but its access seed failed");
        }
        if let Err(e) = crate::app::project_grants(&self.core, &self.acl, &self.auth) {
            tracing::error!(
                error = %e,
                "first-run account created but grant projection failed; restart the server to project them"
            );
        }

        self.auth.audit(
            Some(user_id),
            "admin.setup_completed",
            Some(username),
            Some(ip),
            true,
            None,
        );
        tracing::info!(
            user = user_id.get(),
            "first-run setup completed; /api/setup is now permanently closed"
        );

        Ok(SetupOutcome {
            user_id: user_id.get(),
            username: username.to_string(),
        })
    }
}

impl SetupGate {
    /// One audit row per refused attempt. The presented token and the
    /// requested username are **not** recorded: a rejected attempt's inputs
    /// are attacker-controlled and, in the token's case, a live secret —
    /// `DESIGN-AUTH.md` §9's log is readable by administrators and is assumed
    /// to leak.
    fn audit_failure(&self, ip: std::net::IpAddr, reason: &str) {
        self.auth.audit(
            None,
            "admin.setup_failed",
            None,
            Some(ip),
            false,
            Some(reason),
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use sc_auth::{AuthConfig, AuthService};

    #[test]
    fn generate_writes_readable_token_with_future_expiry() {
        let dir = tempfile::tempdir().unwrap();
        let t = generate(dir.path()).unwrap();
        assert!(!t.token.is_empty());
        assert!(t.expires_at_unix > now_unix());
        assert!(t.expires_at_unix <= now_unix() + EXPIRY_SECS);

        let read_back = read_existing(dir.path()).unwrap();
        assert_eq!(read_back.token, t.token);
        assert_eq!(read_back.expires_at_unix, t.expires_at_unix);
    }

    /// `DESIGN-AUTH.md` §8 specifies 256 bits. Base32 of 32 bytes is 52
    /// characters with no padding.
    #[test]
    fn token_carries_256_bits_of_entropy() {
        let dir = tempfile::tempdir().unwrap();
        let t = generate(dir.path()).unwrap();
        assert_eq!(TOKEN_BYTES * 8, 256);
        assert_eq!(
            t.token.len(),
            52,
            "base32(32 bytes) is 52 chars: {}",
            t.token
        );
        assert!(t
            .token
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit()));
    }

    #[test]
    fn expiry_check() {
        let t = SetupToken {
            token: "x".into(),
            expires_at_unix: 1000,
        };
        assert!(!t.is_expired_at(999));
        assert!(t.is_expired_at(1000));
        assert!(t.is_expired_at(1001));
    }

    #[test]
    fn remove_token_file_is_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        generate(dir.path()).unwrap();
        assert!(read_existing(dir.path()).is_some());
        remove_token_file(dir.path());
        assert!(read_existing(dir.path()).is_none());
        remove_token_file(dir.path()); // no panic, no error
    }

    #[test]
    fn username_rules() {
        assert!(validate_username("admin").is_ok());
        assert!(validate_username("a.b-c_d@e+f").is_ok());
        assert!(validate_username("").is_err());
        assert!(validate_username(" admin").is_err());
        assert!(validate_username("admin ").is_err());
        assert!(validate_username("ad min").is_err());
        // `:` separates the fields of HTTP Basic credentials and of an
        // smbpasswd(5) line.
        assert!(validate_username("ad:min").is_err());
        assert!(validate_username("ad\nmin").is_err());
        assert!(validate_username(&"a".repeat(65)).is_err());
        assert!(validate_username(&"a".repeat(64)).is_ok());
    }

    // ---- the gate itself ----

    struct Fixture {
        gate: SetupGate,
        auth: Arc<AuthService>,
        dir: tempfile::TempDir,
    }

    /// A *fast* Argon2: the gate's tests care about the token state machine,
    /// not about the KDF's cost parameters, and the real ones make each
    /// `create_user` take ~80 ms.
    fn fixture() -> Fixture {
        let dir = tempfile::tempdir().unwrap();
        let cfg = AuthConfig {
            argon2_m_cost_kib: 8,
            argon2_t_cost: 1,
            ..AuthConfig::default()
        };
        let auth = Arc::new(AuthService::new(&dir.path().join("auth.db"), cfg, [3u8; 32]).unwrap());
        // A real (share-less) domain layer: `complete` re-projects grants, and
        // these tests should exercise that call rather than route around it.
        // With no shares registered the projection is a no-op, which is all
        // this fixture needs — `tests/first_run.rs` covers the case where the
        // new administrator actually gains roots.
        let meta = Arc::new(sc_meta::MetaStore::open_in_memory().unwrap());
        let acl = Arc::new(sc_acl::AclEngine::new());
        let core = Arc::new(sc_core::Core::new(meta, acl.clone()));
        let gate = SetupGate::new(auth.clone(), core, acl, dir.path());
        Fixture { gate, auth, dir }
    }

    fn ip() -> std::net::IpAddr {
        "203.0.113.7".parse().unwrap()
    }

    fn pw(s: &str) -> SecretString {
        SecretString::from(s.to_string())
    }

    fn armed() -> (Fixture, String) {
        let f = fixture();
        assert!(
            f.gate.arm_for_first_run().unwrap(),
            "a fresh data dir has no account"
        );
        let token = f.gate.slot.lock().as_ref().unwrap().token.clone();
        (f, token)
    }

    #[test]
    fn a_fresh_deployment_requires_setup() {
        let f = fixture();
        assert!(f.gate.is_required());
    }

    #[test]
    fn the_token_works_exactly_once() {
        let (f, token) = armed();
        let out = f
            .gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .unwrap();
        assert_eq!(out.username, "admin");
        assert_eq!(f.auth.list_users().unwrap().len(), 1);

        // Same token, again: the account now exists, so the durable gate
        // refuses before anything else is even looked at.
        let again = f
            .gate
            .complete(&token, "admin2", &pw("correct horse battery"), ip());
        assert_eq!(again, Err(SetupError::Completed));
        assert_eq!(
            f.auth.list_users().unwrap().len(),
            1,
            "no second account was created"
        );
    }

    #[test]
    fn a_wrong_token_is_refused_and_does_not_spend_the_real_one() {
        let (f, token) = armed();
        assert_eq!(
            f.gate
                .complete("not-the-token", "admin", &pw("correct horse battery"), ip()),
            Err(SetupError::InvalidToken)
        );
        // A near miss — the right length, one byte off — is refused too.
        let mut near = token.clone();
        near.pop();
        near.push(if token.ends_with('a') { 'b' } else { 'a' });
        assert_eq!(
            f.gate
                .complete(&near, "admin", &pw("correct horse battery"), ip()),
            Err(SetupError::InvalidToken)
        );
        assert!(f.auth.list_users().unwrap().is_empty());

        // And the real token still works: a failed attempt must not burn it.
        assert!(f
            .gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .is_ok());
    }

    #[test]
    fn an_expired_token_is_refused() {
        let f = fixture();
        f.gate.arm_with(SetupToken {
            token: "stale-token".into(),
            expires_at_unix: now_unix() - 1,
        });
        assert_eq!(
            f.gate
                .complete("stale-token", "admin", &pw("correct horse battery"), ip()),
            Err(SetupError::Expired)
        );
        assert!(f.auth.list_users().unwrap().is_empty());
        // Setup is still *required* — there is no admin. What the operator
        // needs is a restart, which is what the error says.
        assert!(f.gate.is_required());
    }

    /// Expiry is reported without ever consulting the token, so a late caller
    /// cannot use the endpoint as a token oracle.
    #[test]
    fn an_expired_window_reports_expiry_even_for_a_wrong_token() {
        let f = fixture();
        f.gate.arm_with(SetupToken {
            token: "stale".into(),
            expires_at_unix: now_unix() - 1,
        });
        assert_eq!(
            f.gate.complete(
                "something-else",
                "admin",
                &pw("correct horse battery"),
                ip()
            ),
            Err(SetupError::Expired)
        );
    }

    #[test]
    fn an_unarmed_gate_creates_nobody() {
        let f = fixture();
        assert_eq!(
            f.gate
                .complete("anything", "admin", &pw("correct horse battery"), ip()),
            Err(SetupError::Expired)
        );
        assert!(f.auth.list_users().unwrap().is_empty());
    }

    #[test]
    fn a_short_password_is_refused_and_the_token_survives() {
        let (f, token) = armed();
        // Nine characters; `DESIGN-AUTH.md` §2.3 demands ten.
        assert_eq!(
            f.gate.complete(&token, "admin", &pw("123456789"), ip()),
            Err(SetupError::WeakPassword { min_len: 10 })
        );
        assert!(f.auth.list_users().unwrap().is_empty());
        // Exactly ten is fine, and the token was not spent by the refusal.
        assert!(f
            .gate
            .complete(&token, "admin", &pw("1234567890"), ip())
            .is_ok());
    }

    #[test]
    fn a_bad_username_is_refused_and_the_token_survives() {
        let (f, token) = armed();
        assert!(matches!(
            f.gate
                .complete(&token, "ad:min", &pw("correct horse battery"), ip()),
            Err(SetupError::InvalidUsername(_))
        ));
        assert!(f.auth.list_users().unwrap().is_empty());
        assert!(f
            .gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .is_ok());
    }

    /// The heart of "permanently disabled": a brand-new process over the same
    /// data directory must not reopen admin creation.
    #[test]
    fn a_restart_does_not_reissue_once_an_account_exists() {
        let (f, token) = armed();
        f.gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .unwrap();
        assert!(
            read_existing(f.dir.path()).is_none(),
            "the token file is removed when spent"
        );

        // A second gate over the same auth service and data dir — this is
        // what the next boot builds.
        let reboot = SetupGate::new(
            f.auth.clone(),
            Arc::new(sc_core::Core::new(
                Arc::new(sc_meta::MetaStore::open_in_memory().unwrap()),
                Arc::new(sc_acl::AclEngine::new()),
            )),
            Arc::new(sc_acl::AclEngine::new()),
            f.dir.path(),
        );
        assert!(!reboot.is_required());
        assert!(!reboot.arm_for_first_run().unwrap(), "no token is issued");
        assert!(read_existing(f.dir.path()).is_none());
        assert_eq!(
            reboot.complete(&token, "admin2", &pw("correct horse battery"), ip()),
            Err(SetupError::Completed)
        );
        assert_eq!(f.auth.list_users().unwrap().len(), 1);
    }

    /// A token file left behind by a `sc-server setup` run against a
    /// deployment that has since gained an account is cleared at boot rather
    /// than left lying around.
    #[test]
    fn arming_clears_a_stale_token_file_when_accounts_exist() {
        let f = fixture();
        generate(f.dir.path()).unwrap();
        f.auth
            .create_user("someone", &pw("correct horse battery"))
            .unwrap();
        assert!(!f.gate.arm_for_first_run().unwrap());
        assert!(read_existing(f.dir.path()).is_none());
    }

    /// `DESIGN-AUTH.md` §2.4: derivation happens at account creation,
    /// unconditionally, so SMB can be switched on later without a password
    /// reset. Asserted here because the first-run path is the one that
    /// creates the administrator, and this is exactly the property nobody
    /// would notice was missing until the day SMB was enabled.
    #[test]
    fn the_bootstrapped_admin_is_smb_ready() {
        let (f, token) = armed();
        let out = f
            .gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .unwrap();
        assert!(f
            .auth
            .nt_hash_present(sc_vfs::UserId::new(out.user_id))
            .unwrap());
    }

    /// The whole point of this change: the first account created through
    /// `/api/setup` must hold the real `role` column's admin bit, not rely on
    /// "id happens to be 1".
    #[test]
    fn the_bootstrapped_account_holds_the_admin_role() {
        let (f, token) = armed();
        let out = f
            .gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .unwrap();
        let row = f
            .auth
            .find_user_by_id(sc_vfs::UserId::new(out.user_id))
            .unwrap()
            .unwrap();
        assert!(
            row.is_admin,
            "the first account created by /api/setup must be an administrator"
        );
    }

    #[test]
    fn success_and_failure_both_leave_an_audit_row() {
        let (f, token) = armed();
        let _ = f
            .gate
            .complete("wrong", "admin", &pw("correct horse battery"), ip());
        f.gate
            .complete(&token, "admin", &pw("correct horse battery"), ip())
            .unwrap();

        assert_eq!(
            f.auth
                .audit_count("admin.setup_failed", Some(false))
                .unwrap(),
            1
        );
        assert_eq!(
            f.auth
                .audit_count("admin.setup_completed", Some(true))
                .unwrap(),
            1
        );
        // `create_user`'s own row is still there — one says an account
        // appeared, the other says the bootstrap route was consumed.
        assert_eq!(f.auth.audit_count("auth.user_created", None).unwrap(), 1);
    }
}
