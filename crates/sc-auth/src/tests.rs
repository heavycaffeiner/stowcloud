//! White-box tests for the 10 non-negotiable behaviours in
//! `docs/DESIGN-AUTH.md`. Argon2 cost is overridden to m=8MiB,t=1 so the
//! suite runs fast; the *default* in `AuthConfig::default()` (asserted by
//! `default_argon2_params_match_spec`) stays at the spec's 48MiB/t=3/p=1.

use super::*;
use secrecy::{ExposeSecret, SecretString};
use std::net::{IpAddr, Ipv4Addr};
use std::time::Duration;

fn local_ip() -> IpAddr {
    IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1))
}

fn test_cfg() -> AuthConfig {
    // Keep the suite fast: low Argon2 cost only in tests.
    AuthConfig {
        argon2_m_cost_kib: 8 * 1024,
        argon2_t_cost: 1,
        argon2_p_cost: 1,
        ..AuthConfig::default()
    }
}

fn new_service(cfg: AuthConfig) -> (AuthService, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let db_path = dir.path().join("auth.db");
    let svc = AuthService::new(&db_path, cfg, [7u8; 32]).unwrap();
    (svc, dir)
}

fn pw(s: &str) -> SecretString {
    SecretString::from(s.to_string())
}

fn random_totp_secret() -> String {
    let mut raw = [0u8; 20];
    getrandom::getrandom(&mut raw).unwrap();
    totp_rs::Secret::Raw(raw.to_vec()).to_encoded().to_string()
}

// ---- #1 Argon2 params, rehash-on-login, concurrency semaphore ----------

#[test]
fn default_argon2_params_match_spec() {
    let cfg = AuthConfig::default();
    assert_eq!(cfg.argon2_m_cost_kib, 48 * 1024);
    assert_eq!(cfg.argon2_t_cost, 3);
    assert_eq!(cfg.argon2_p_cost, 1);
    assert_eq!(cfg.argon2_parallelism, 4);
}

/// `create_user` distinguishes a taken name from every other failure — the
/// admin user-creation HTTP handler needs this to answer `409`, not a bare
/// `500`, when an operator retypes an existing username.
#[test]
fn create_user_reports_duplicate_name_distinctly() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_user("taken", &pw("correct horse battery")).unwrap();
    assert_eq!(
        svc.create_user("taken", &pw("another password")),
        Err(CreateUserError::DuplicateName)
    );
    // Case-insensitive too — `user.name` is `COLLATE NOCASE` (`db.rs`).
    assert_eq!(
        svc.create_user("TAKEN", &pw("another password")),
        Err(CreateUserError::DuplicateName)
    );
}

#[test]
fn create_user_reports_short_password_distinctly() {
    let (svc, _dir) = new_service(test_cfg());
    assert_eq!(
        svc.create_user("shorty", &pw("short1")),
        Err(CreateUserError::TooShort { min: 10 })
    );
}

#[tokio::test]
async fn rehash_on_login_when_params_change() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("alice", &pw("correct horse battery")).unwrap();

    // Simulate a stale hash created under weaker params.
    let mut old_cfg = test_cfg();
    old_cfg.argon2_m_cost_kib = 8; // deliberately weak
    let old_hash = password::hash_password(&old_cfg, &pw("correct horse battery")).unwrap();
    {
        let conn = svc.pool.get().unwrap();
        conn.execute(
            "UPDATE user SET pw_hash = ?1 WHERE id = ?2",
            rusqlite::params![old_hash, uid.get()],
        )
        .unwrap();
    }
    assert!(password::needs_rehash(&old_hash, &svc.cfg));

    let result = svc.login("alice", &pw("correct horse battery"), local_ip()).await;
    assert!(matches!(result, LoginResult::Ok(_)));

    let new_hash = svc.pw_hash_of(uid).unwrap();
    assert_ne!(new_hash, old_hash);
    assert!(!password::needs_rehash(&new_hash, &svc.cfg));
}

#[test]
fn argon2_gate_bounds_concurrency() {
    let mut cfg = test_cfg();
    cfg.argon2_parallelism = 2;
    let (svc, _dir) = new_service(cfg);
    assert_eq!(svc.argon2_gate.available_permits(), 2);

    let p1 = svc.argon2_gate.acquire();
    let p2 = svc.argon2_gate.acquire();
    assert_eq!(svc.argon2_gate.available_permits(), 0);
    // A 3rd acquire must not succeed instantly.
    assert!(svc.argon2_gate.try_acquire().is_none());
    drop(p1);
    assert_eq!(svc.argon2_gate.available_permits(), 1);
    drop(p2);
    assert_eq!(svc.argon2_gate.available_permits(), 2);
}

/// The bug this refactor fixes: `create_user`/`set_password`/`totp_enroll`
/// are synchronous and previously hashed/verified straight through Argon2,
/// bypassing the concurrency gate entirely — N concurrent password changes
/// cost N × m_cost instead of being capped at `argon2_parallelism × m_cost`
/// (DESIGN-AUTH §2.2). This drives well over `argon2_parallelism` concurrent
/// `set_password` calls from real OS threads and asserts the gate's
/// concurrent-invocation high-water mark never exceeds the configured limit.
#[test]
fn set_password_high_water_mark_never_exceeds_parallelism() {
    let mut cfg = test_cfg();
    cfg.argon2_parallelism = 2;
    let (svc, _dir) = new_service(cfg);
    let svc = std::sync::Arc::new(svc);

    let n = 8;
    let uids: Vec<_> = (0..n)
        .map(|i| svc.create_user(&format!("hwuser{i}"), &pw("initialpassword1")).unwrap())
        .collect();
    // create_user's own hashing already exercised the gate; the mark it
    // left behind isn't what we're asserting on below.
    assert!(svc.argon2_gate.high_water() <= 2);

    let barrier = std::sync::Arc::new(std::sync::Barrier::new(n));
    let handles: Vec<_> = uids
        .into_iter()
        .map(|uid| {
            let svc = std::sync::Arc::clone(&svc);
            let barrier = std::sync::Arc::clone(&barrier);
            std::thread::spawn(move || {
                barrier.wait(); // line every thread up so they contend together
                svc.set_password(uid, &pw("newpassword1234")).unwrap();
            })
        })
        .collect();
    for h in handles {
        h.join().unwrap();
    }

    // Concurrency must have fully drained back to zero once every thread
    // has joined — proves the gate isn't leaking permits.
    assert_eq!(svc.argon2_gate.concurrent(), 0);

    let high_water = svc.argon2_gate.high_water();
    assert!(
        (1..=2).contains(&high_water),
        "Argon2 concurrency high-water mark was {high_water}, must be in 1..=2 \
         (configured argon2_parallelism=2)"
    );
}

// ---- #2 NT hash: derived at creation/password change, encrypted --------

#[test]
fn nt_hash_derived_at_creation_and_encrypted_separately() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("bob", &pw("hunter2hunter2")).unwrap();
    assert!(svc.nt_hash_present(uid).unwrap());

    let conn = svc.pool.get().unwrap();
    let ct: Vec<u8> = conn
        .query_row(
            "SELECT nt_hash_ct FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![uid.get()],
            |r| r.get(0),
        )
        .unwrap();
    // Ciphertext must not equal the raw NT hash (i.e. it's actually sealed).
    let raw_nt = nt_hash::nt_hash("hunter2hunter2");
    assert_ne!(ct[24..], raw_nt[..]);
    let opened = nt_hash::open_nt(&svc.master_key, &ct, uid, svc.cfg.key_ver).unwrap();
    assert_eq!(opened, raw_nt);
}

#[test]
fn set_password_rederives_nt_hash() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("carol", &pw("firstpassword1")).unwrap();
    let conn = svc.pool.get().unwrap();
    let ct1: Vec<u8> = conn
        .query_row(
            "SELECT nt_hash_ct FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![uid.get()],
            |r| r.get(0),
        )
        .unwrap();
    drop(conn);

    svc.set_password(uid, &pw("secondpassword2")).unwrap();
    let conn = svc.pool.get().unwrap();
    let ct2: Vec<u8> = conn
        .query_row(
            "SELECT nt_hash_ct FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![uid.get()],
            |r| r.get(0),
        )
        .unwrap();
    let opened = nt_hash::open_nt(&svc.master_key, &ct2, uid, svc.cfg.key_ver).unwrap();
    assert_eq!(opened, nt_hash::nt_hash("secondpassword2"));
    assert_ne!(ct1, ct2);
}

// ---- #3 TOTP users can't use account password over Basic ----------------

#[tokio::test]
async fn totp_user_rejected_on_basic_account_password() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("dora", &pw("dorapassword1")).unwrap();

    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    let code = totp.generate_current().unwrap();

    svc.totp_enroll(uid, &pw("dorapassword1"), &secret, &code).unwrap();

    let result = svc.verify_basic("dora", &pw("dorapassword1"), local_ip()).await;
    assert!(matches!(result, BasicResult::AppPasswordRequired));
}

// ---- #4 Enabling TOTP deletes NT hash; disabling re-derives it ---------

#[test]
fn totp_enable_deletes_nt_disable_rederives() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("erin", &pw("erinpassword1")).unwrap();
    assert!(svc.nt_hash_present(uid).unwrap());

    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    let code = totp.generate_current().unwrap();
    svc.totp_enroll(uid, &pw("erinpassword1"), &secret, &code).unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap(), "NT hash must be deleted once TOTP is on");

    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(svc.totp_disable(uid, &pw("erinpassword1"))).unwrap();
    assert!(svc.nt_hash_present(uid).unwrap(), "NT hash must be re-derived immediately on TOTP disable");
    let conn = svc.pool.get().unwrap();
    let ct: Vec<u8> = conn
        .query_row(
            "SELECT nt_hash_ct FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![uid.get()],
            |r| r.get(0),
        )
        .unwrap();
    let opened = nt_hash::open_nt(&svc.master_key, &ct, uid, svc.cfg.key_ver).unwrap();
    assert_eq!(opened, nt_hash::nt_hash("erinpassword1"));
}

// ---- #4b Recovery codes: remaining count, reissue invalidates old codes --

/// Enrolls TOTP for `uid` with a fresh random secret and returns the 10
/// recovery codes minted — the same enroll dance `totp_enable_deletes_nt_...`
/// above does inline, pulled out since every test in this section needs it.
fn enroll_totp(svc: &AuthService, uid: sc_vfs::UserId, password: &str) -> Vec<String> {
    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    let code = totp.generate_current().unwrap();
    svc.totp_enroll(uid, &pw(password), &secret, &code).unwrap()
}

#[tokio::test]
async fn recovery_codes_remaining_starts_at_ten_and_decrements_on_use() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("gina", &pw("ginapassword1")).unwrap();
    assert_eq!(
        svc.recovery_codes_remaining(uid).unwrap(),
        0,
        "no TOTP enrolled yet -- nothing to count"
    );

    let codes = enroll_totp(&svc, uid, "ginapassword1");
    assert_eq!(svc.recovery_codes_remaining(uid).unwrap(), 10);

    let LoginResult::TotpRequired { challenge } = svc.login("gina", &pw("ginapassword1"), local_ip()).await else {
        panic!("expected totp_required");
    };
    let used = svc.verify_totp(&challenge, &codes[0]).await.unwrap();
    assert!(used.is_some(), "a fresh recovery code must complete the login");
    assert_eq!(svc.recovery_codes_remaining(uid).unwrap(), 9, "using a code must consume exactly one");
}

#[test]
fn reissue_recovery_codes_requires_totp_enabled() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("hank", &pw("hankpassword1")).unwrap();
    // No TOTP on this account -- a recovery code could never be presented
    // anywhere (no `login_challenge` is ever issued for it), so reissuing
    // would mint 10 credentials that can't do anything.
    assert_eq!(
        svc.reissue_recovery_codes(uid, &pw("hankpassword1")),
        Err(RecoveryReissueError::TotpNotEnabled)
    );
}

#[test]
fn reissue_recovery_codes_requires_correct_password() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("iris", &pw("irispassword1")).unwrap();
    enroll_totp(&svc, uid, "irispassword1");

    assert_eq!(
        svc.reissue_recovery_codes(uid, &pw("wrongpassword1")),
        Err(RecoveryReissueError::BadPassword)
    );
    // A rejected re-confirmation must not touch anything.
    assert_eq!(svc.recovery_codes_remaining(uid).unwrap(), 10);
}

/// The regression this task exists to prevent: a reissue that only inserts
/// (or that never runs the `DELETE` in the same transaction as the inserts)
/// would leave the old list still valid alongside the new one.
#[tokio::test]
async fn reissue_recovery_codes_invalidates_every_old_code() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("jill", &pw("jillpassword1")).unwrap();
    let old_codes = enroll_totp(&svc, uid, "jillpassword1");
    assert_eq!(svc.recovery_codes_remaining(uid).unwrap(), 10);

    let new_codes = svc.reissue_recovery_codes(uid, &pw("jillpassword1")).unwrap();
    assert_eq!(new_codes.len(), 10);
    assert_eq!(
        svc.recovery_codes_remaining(uid).unwrap(),
        10,
        "reissue replaces the set, it does not grow it"
    );
    // ~56 bits of entropy per code -- a shared code between the two mints
    // would mean the fix isn't generating fresh randomness, not a fluke.
    assert!(old_codes.iter().all(|c| !new_codes.contains(c)));

    let LoginResult::TotpRequired { challenge } = svc.login("jill", &pw("jillpassword1"), local_ip()).await else {
        panic!("expected totp_required");
    };
    assert!(
        svc.verify_totp(&challenge, &old_codes[0]).await.unwrap().is_none(),
        "an old recovery code must be rejected once the set has been reissued"
    );
    // `verify_totp` only deletes the challenge on a *match* (TOTP or
    // recovery), so the same challenge is still live for this next attempt.
    let result = svc.verify_totp(&challenge, &new_codes[0]).await.unwrap();
    assert!(result.is_some(), "a code from the reissued set must still work");
    assert_eq!(svc.recovery_codes_remaining(uid).unwrap(), 9);
}

// ---- #5 Opportunistic NT backfill on plaintext verification -------------

#[tokio::test]
async fn basic_auth_backfills_missing_nt_hash() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("frank", &pw("frankpassword1")).unwrap();

    // Simulate a state where the NT hash is absent (e.g. admin force-reset
    // TOTP without a plaintext-bearing re-auth yet).
    {
        let conn = svc.pool.get().unwrap();
        conn.execute(
            "DELETE FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![uid.get()],
        )
        .unwrap();
    }
    assert!(!svc.nt_hash_present(uid).unwrap());

    let result = svc.verify_basic("frank", &pw("frankpassword1"), local_ip()).await;
    assert!(matches!(result, BasicResult::Ok(_)));
    assert!(svc.nt_hash_present(uid).unwrap(), "successful Basic auth must backfill the NT hash");
}

// ---- #6 Credential cache: second verify_basic must not re-run Argon2 ---

#[tokio::test]
async fn cred_cache_hit_skips_argon2() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_user("gina", &pw("ginapassword1")).unwrap();

    let r1 = svc.verify_basic("gina", &pw("ginapassword1"), local_ip()).await;
    assert!(matches!(r1, BasicResult::Ok(_)));
    let calls_after_first = svc.argon2_calls();
    assert!(calls_after_first >= 1);

    let r2 = svc.verify_basic("gina", &pw("ginapassword1"), local_ip()).await;
    assert!(matches!(r2, BasicResult::Ok(_)));
    assert_eq!(
        svc.argon2_calls(),
        calls_after_first,
        "second verify_basic with identical credentials must hit the cred cache, not Argon2"
    );
}

#[tokio::test]
async fn cred_cache_negative_hit_skips_argon2() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_user("henry", &pw("henrypassword1")).unwrap();

    let r1 = svc.verify_basic("henry", &pw("wrongpassword"), local_ip()).await;
    assert!(matches!(r1, BasicResult::Invalid));
    let calls_after_first = svc.argon2_calls();
    assert!(calls_after_first >= 1);

    let r2 = svc.verify_basic("henry", &pw("wrongpassword"), local_ip()).await;
    assert!(matches!(r2, BasicResult::Invalid));
    assert_eq!(
        svc.argon2_calls(),
        calls_after_first,
        "repeated wrong credentials must hit the negative cache, not Argon2"
    );
}

// ---- #7 Dual token bucket rate limit; no lockout -------------------------

#[tokio::test]
async fn ip_rate_limit_blocks_before_argon2() {
    let mut cfg = test_cfg();
    cfg.rate_ip_capacity = 2;
    cfg.rate_ip_refill = Duration::from_secs(3600); // effectively no refill during the test
    let (svc, _dir) = new_service(cfg);
    svc.create_user("iris", &pw("irispassword1")).unwrap();

    let ip = local_ip();
    let _ = svc.login("iris", &pw("wrong"), ip).await;
    let _ = svc.login("iris", &pw("wrong"), ip).await;
    let calls_before = svc.argon2_calls();
    let result = svc.login("iris", &pw("wrong"), ip).await;
    assert!(matches!(result, LoginResult::RateLimited { .. }));
    assert_eq!(svc.argon2_calls(), calls_before, "IP rate gate must reject before Argon2 runs");
}

#[test]
fn account_soft_delay_never_locks_out() {
    // Many failures must still allow the NEXT correct attempt to succeed
    // (checked structurally: AccountGate has no notion of "locked").
    let gate = rate_limit::AccountGate::new(Duration::from_secs(3600));
    for _ in 0..50 {
        gate.record_failure("victim");
    }
    gate.reset("victim"); // a real success always resets, never refuses to
}

// ---- #8 Account enumeration resistance ----------------------------------

#[tokio::test]
async fn unknown_user_login_still_runs_argon2_and_reports_generic_invalid() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_user("known", &pw("knownpassword1")).unwrap();

    let calls_before = svc.argon2_calls();
    let result = svc.login("nosuchuser", &pw("whatever12"), local_ip()).await;
    assert!(matches!(result, LoginResult::Invalid));
    assert!(
        svc.argon2_calls() > calls_before,
        "unknown username must still run a real Argon2 verify (DUMMY_HASH) for timing parity"
    );
}

// ---- #9 Session tokens ---------------------------------------------------

#[test]
fn session_token_format_and_lifecycle() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("jack", &pw("jackpassword1")).unwrap();
    let ua = "test-agent/1.0";
    let token = svc.create_session(uid, local_ip(), ua, AMR_PASSWORD).unwrap();

    // 256-bit CSPRNG, base64url-nopad -> 43 chars.
    assert_eq!(token.as_str().len(), 43);
    assert!(token.as_str().chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));

    let principal = svc.validate_session(token.as_str()).unwrap();
    assert!(principal.is_some());
    assert_eq!(principal.unwrap().user, uid);

    // Only the sha256 hash is stored, never the plaintext token.
    let conn = svc.pool.get().unwrap();
    let stored: Vec<String> = conn
        .prepare("SELECT id_hash FROM session")
        .unwrap()
        .query_map([], |r| r.get::<_, Vec<u8>>(0).map(|v| hex::encode(&v)))
        .unwrap()
        .collect::<Result<_, _>>()
        .unwrap();
    assert!(!stored.is_empty());
    assert!(stored.iter().all(|h| h != token.as_str()));

    svc.revoke_session(token.as_str()).unwrap();
    assert!(svc.validate_session(token.as_str()).unwrap().is_none());
}

#[test]
fn list_sessions_reports_active_sessions() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("karl", &pw("karlpassword1")).unwrap();
    svc.create_session(uid, local_ip(), "ua-1", AMR_PASSWORD).unwrap();
    svc.create_session(uid, local_ip(), "ua-2", AMR_PASSWORD).unwrap();
    let sessions = svc.list_sessions(uid).unwrap();
    assert_eq!(sessions.len(), 2);
}

// ---- #10 App passwords ----------------------------------------------------

#[tokio::test]
async fn app_password_format_and_verification_without_argon2() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("lena", &pw("lenapassword1")).unwrap();
    let (id, token) = svc.issue_app_password(uid, "test device", Scope::default()).unwrap();
    assert!(token.starts_with("stow_"));
    assert!(id > 0);

    let calls_before = svc.argon2_calls();
    let result = svc.verify_basic("lena", &pw(&token), local_ip()).await;
    match result {
        BasicResult::Ok(p) => {
            assert_eq!(p.user, uid);
            assert!(matches!(p.via, AuthVia::AppPassword(app_id) if app_id == id));
        }
        other => panic!("expected Ok, got {other:?}"),
    }
    assert_eq!(svc.argon2_calls(), calls_before, "app-password verification must never run Argon2");

    svc.revoke_app_password(id).unwrap();
    let result2 = svc.verify_basic("lena", &pw(&token), local_ip()).await;
    assert!(matches!(result2, BasicResult::Invalid));
}

#[test]
fn app_password_list_and_scope() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("mona", &pw("monapassword1")).unwrap();
    let scope = Scope {
        perms_mask: Some(0b0101),
        shares: Some(vec![sc_vfs::ShareId::new(9)]),
    };
    let (id, _token) = svc.issue_app_password(uid, "backup tool", scope).unwrap();
    let list = svc.list_app_passwords(uid).unwrap();
    assert_eq!(list.len(), 1);
    assert_eq!(list[0].id, id);
    assert_eq!(list[0].scope_perms, Some(0b0101));
}

mod hex {
    pub fn encode(bytes: &[u8]) -> String {
        bytes.iter().map(|b| format!("{b:02x}")).collect()
    }
}

// --------------------------------------------------------- role model --

/// `create_user` must never hand out the administrator role implicitly —
/// unlike the old `id.get() == 1` stand-in, account #1 is not special here.
/// Only an explicit `set_admin` call (the first-run bootstrap's job) makes
/// one.
#[test]
fn create_user_is_never_admin_by_default() {
    let (svc, _dir) = new_service(test_cfg());
    let first = svc.create_user("first", &pw("correct horse battery")).unwrap();
    let second = svc.create_user("second", &pw("correct horse battery")).unwrap();
    assert!(!svc.find_user_by_id(first).unwrap().unwrap().is_admin);
    assert!(!svc.find_user_by_id(second).unwrap().unwrap().is_admin);
}

#[test]
fn set_admin_grants_and_revokes() {
    let (svc, _dir) = new_service(test_cfg());
    let a = svc.create_user("alice", &pw("correct horse battery")).unwrap();
    let b = svc.create_user("bob", &pw("correct horse battery")).unwrap();
    svc.set_admin(a, true).unwrap();
    assert!(svc.find_user_by_id(a).unwrap().unwrap().is_admin);
    assert!(!svc.find_user_by_id(b).unwrap().unwrap().is_admin);

    // With two admins, demoting one is fine.
    svc.set_admin(b, true).unwrap();
    svc.set_admin(a, false).unwrap();
    assert!(!svc.find_user_by_id(a).unwrap().unwrap().is_admin);
    assert!(svc.find_user_by_id(b).unwrap().unwrap().is_admin);
}

#[test]
fn set_admin_unknown_user_is_reported() {
    let (svc, _dir) = new_service(test_cfg());
    let ghost = sc_vfs::UserId::new(999);
    assert_eq!(svc.set_admin(ghost, true), Err(AdminGuardError::NoSuchUser));
}

/// The heart of the last-admin guard: with exactly one active administrator,
/// none of demote / disable / delete may succeed.
#[test]
fn last_active_admin_cannot_be_demoted_disabled_or_deleted() {
    let (svc, _dir) = new_service(test_cfg());
    let admin = svc.create_user("admin", &pw("correct horse battery")).unwrap();
    svc.set_admin(admin, true).unwrap();

    assert_eq!(svc.set_admin(admin, false), Err(AdminGuardError::LastAdmin));
    assert_eq!(svc.disable_user(admin, true), Err(AdminGuardError::LastAdmin));
    assert_eq!(svc.delete_user(admin), Err(AdminGuardError::LastAdmin));

    // Untouched by all three refusals.
    let row = svc.find_user_by_id(admin).unwrap().unwrap();
    assert!(row.is_admin);
    assert!(!row.disabled);
}

/// A second active admin lifts the guard for the first.
#[test]
fn a_second_admin_lifts_the_last_admin_guard() {
    let (svc, _dir) = new_service(test_cfg());
    let a = svc.create_user("a", &pw("correct horse battery")).unwrap();
    let b = svc.create_user("b", &pw("correct horse battery")).unwrap();
    svc.set_admin(a, true).unwrap();
    svc.set_admin(b, true).unwrap();

    svc.disable_user(a, true).unwrap();
    let row = svc.find_user_by_id(a).unwrap().unwrap();
    assert!(row.disabled, "disabling is allowed once a second active admin exists");

    // Now `b` is the only *active* admin left (`a` is disabled) — `b` is
    // guarded even though `a` still nominally holds the role.
    assert_eq!(svc.set_admin(b, false), Err(AdminGuardError::LastAdmin));
    assert_eq!(svc.delete_user(b), Err(AdminGuardError::LastAdmin));
}

/// A disabled admin is not "the last admin" for guard purposes — it is
/// already inert, so removing it changes nothing about who can administer
/// the system.
#[test]
fn a_disabled_admin_does_not_block_deleting_a_disabled_admin() {
    let (svc, _dir) = new_service(test_cfg());
    let active = svc.create_user("active", &pw("correct horse battery")).unwrap();
    let stale = svc.create_user("stale", &pw("correct horse battery")).unwrap();
    svc.set_admin(active, true).unwrap();
    svc.set_admin(stale, true).unwrap();
    svc.disable_user(stale, true).unwrap();

    // `stale` is an admin but disabled, so it was never "the" active admin
    // being protected — deleting it must not trip the guard.
    svc.delete_user(stale).unwrap();
    assert!(svc.find_user_by_id(stale).unwrap().is_none());
    // `active` remains, untouched.
    assert!(svc.find_user_by_id(active).unwrap().unwrap().is_admin);
}

/// Re-enabling a disabled admin never needs the guard — it can only grow the
/// active-admin count, never shrink it.
#[test]
fn re_enabling_an_admin_needs_no_guard() {
    let (svc, _dir) = new_service(test_cfg());
    let a = svc.create_user("a", &pw("correct horse battery")).unwrap();
    let b = svc.create_user("b", &pw("correct horse battery")).unwrap();
    svc.set_admin(a, true).unwrap();
    svc.set_admin(b, true).unwrap();
    svc.disable_user(a, true).unwrap();
    svc.disable_user(b, true).unwrap_err(); // b is now the last active admin
    svc.disable_user(a, false).unwrap(); // re-enabling a is never guarded
    assert!(!svc.find_user_by_id(a).unwrap().unwrap().disabled);
}

/// `delete_user` must not leave orphaned rows behind in any of the tables
/// keyed by `user` — sessions, app passwords, and the SMB NT-hash secret in
/// particular, since a future account could otherwise collide with a
/// recycled id (`INTEGER PRIMARY KEY` in SQLite reuses freed rowids).
#[test]
fn delete_user_removes_dependent_rows() {
    let (svc, _dir) = new_service(test_cfg());
    let keep = svc.create_user("keep", &pw("correct horse battery")).unwrap();
    svc.set_admin(keep, true).unwrap();
    let gone = svc.create_user("gone", &pw("correct horse battery")).unwrap();

    // Give `gone` a session and an app password so there is something to
    // clean up.
    let _session = svc.create_session(gone, local_ip(), "test-agent", AMR_PASSWORD).unwrap();
    let (_app_pw_id, _token) = svc.issue_app_password(gone, "device", Scope::default()).unwrap();
    assert!(svc.nt_hash_present(gone).unwrap(), "new accounts are SMB-ready by construction");

    svc.delete_user(gone).unwrap();

    assert!(svc.find_user_by_id(gone).unwrap().is_none());
    assert!(svc.list_sessions(gone).unwrap().is_empty());
    assert!(svc.list_app_passwords(gone).unwrap().is_empty());
    assert!(!svc.nt_hash_present(gone).unwrap());
    // The other account is unaffected.
    assert!(svc.find_user_by_id(keep).unwrap().unwrap().is_admin);
}

#[test]
fn delete_user_unknown_is_reported() {
    let (svc, _dir) = new_service(test_cfg());
    let ghost = sc_vfs::UserId::new(999);
    assert_eq!(svc.delete_user(ghost), Err(AdminGuardError::NoSuchUser));
}

/// A database created before the `role` column existed must gain it (and
/// keep every existing row at the default, non-admin role) on the next open
/// — the migration in `db.rs` runs unconditionally and must be a no-op on a
/// database that already has the column, and must not lose data on one that
/// doesn't.
#[test]
fn a_pre_role_database_migrates_in_place() {
    let dir = tempfile::tempdir().unwrap();
    let db_path = dir.path().join("auth.db");

    // Build a database that predates the `role` column, by hand, mirroring
    // the pre-migration schema.
    {
        let conn = rusqlite::Connection::open(&db_path).unwrap();
        conn.execute_batch(
            "CREATE TABLE user (
                id INTEGER PRIMARY KEY,
                name TEXT UNIQUE NOT NULL COLLATE NOCASE,
                display TEXT,
                pw_hash TEXT NOT NULL,
                totp_secret BLOB,
                disabled INTEGER NOT NULL DEFAULT 0,
                quota_bytes INTEGER,
                created_ns INTEGER NOT NULL,
                smb_opt_out INTEGER NOT NULL DEFAULT 0,
                smb_enabled INTEGER NOT NULL DEFAULT 1
            );",
        )
        .unwrap();
        conn.execute(
            "INSERT INTO user (id, name, pw_hash, created_ns) VALUES (1, 'legacy', 'x', 0)",
            [],
        )
        .unwrap();
    }

    // Opening through `AuthService::new` runs the schema + migration.
    let svc = AuthService::new(&db_path, test_cfg(), [7u8; 32]).unwrap();
    let row = svc.find_user_by_id(sc_vfs::UserId::new(1)).unwrap().unwrap();
    assert_eq!(row.name, "legacy");
    // The migration must *carry the old rule forward*, not discard it. Before
    // this column existed, "administrator" was the rule `id == 1` — so adding
    // the column with `DEFAULT 0` and stopping there does not introduce a role
    // model, it removes the administrator from every existing deployment. And
    // it is unrecoverable from inside the product: promotion is admin-only, so
    // nobody is left who can perform it.
    //
    // This assertion previously read `!row.is_admin`, pinning that behaviour as
    // intended. It was observed for real: after the migration ran against a
    // live database the first account came back `is_admin: false`.
    assert!(
        row.is_admin,
        "the pre-migration administrator (id == 1) must stay an administrator"
    );

    // It is a real column now, not a rule about ids — but demoting *this*
    // account is still refused, because it is the only administrator the
    // deployment has. That is the last-admin guard doing its job, and it is
    // worth pinning here: the migration and the guard together are what make
    // an upgrade non-destructive.
    assert!(
        svc.set_admin(row.id, false).is_err(),
        "demoting the only administrator must be refused"
    );
    assert!(svc.find_user_by_id(row.id).unwrap().unwrap().is_admin);

    // Re-opening (the migration running a second time) must not error or
    // disturb the data.
    drop(svc);
    let svc2 = AuthService::new(&db_path, test_cfg(), [7u8; 32]).unwrap();
    assert!(svc2.find_user_by_id(sc_vfs::UserId::new(1)).unwrap().unwrap().is_admin);
}

// ------------------------------------------------------------- groups --
// `FEATURES.md` #48 — group CRUD + membership. `groups.rs` mirrors
// `users.rs`'s shape; these tests mirror its own (`create_user_reports_
// duplicate_name_distinctly`, `delete_user_removes_dependent_rows`, etc.).

#[test]
fn create_group_reports_duplicate_name_distinctly() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_group("engineering").unwrap();
    assert_eq!(svc.create_group("engineering"), Err(GroupNameError::DuplicateName));
}

#[test]
fn list_groups_reports_every_group_in_id_order() {
    let (svc, _dir) = new_service(test_cfg());
    let a = svc.create_group("alpha").unwrap();
    let b = svc.create_group("beta").unwrap();
    let rows = svc.list_groups().unwrap();
    assert_eq!(rows.iter().map(|g| g.id).collect::<Vec<_>>(), vec![a, b]);
    assert_eq!(rows[0].name, "alpha");
    assert_eq!(rows[1].name, "beta");
}

#[test]
fn rename_group_reports_duplicate_and_unknown_distinctly() {
    let (svc, _dir) = new_service(test_cfg());
    let a = svc.create_group("alpha").unwrap();
    svc.create_group("beta").unwrap();
    assert_eq!(svc.rename_group(a, "beta"), Err(GroupNameError::DuplicateName));
    svc.rename_group(a, "alpha2").unwrap();
    assert_eq!(svc.list_groups().unwrap()[0].name, "alpha2");

    let ghost = sc_vfs::GroupId::new(999);
    assert_eq!(svc.rename_group(ghost, "whatever"), Err(GroupNameError::NotFound));
}

#[test]
fn membership_add_remove_and_list_round_trip() {
    let (svc, _dir) = new_service(test_cfg());
    let alice = svc.create_user("alice", &pw("correct horse battery")).unwrap();
    let bob = svc.create_user("bob", &pw("correct horse battery")).unwrap();
    let eng = svc.create_group("engineering").unwrap();

    svc.add_membership(alice, eng).unwrap();
    svc.add_membership(bob, eng).unwrap();
    assert_eq!(svc.list_group_members(eng).unwrap(), vec![alice, bob]);

    svc.remove_membership(alice, eng).unwrap();
    assert_eq!(svc.list_group_members(eng).unwrap(), vec![bob]);

    let all = svc.list_memberships_all().unwrap();
    assert_eq!(all.get(&bob), Some(&vec![eng]));
    assert!(!all.contains_key(&alice));
}

/// `membership` has no foreign key on either column (`db.rs`) — both
/// `add_membership` and `remove_membership` must refuse a phantom user or
/// group themselves, rather than silently inserting/deleting a row naming
/// one.
#[test]
fn membership_refuses_a_phantom_user_or_group() {
    let (svc, _dir) = new_service(test_cfg());
    let alice = svc.create_user("alice", &pw("correct horse battery")).unwrap();
    let eng = svc.create_group("engineering").unwrap();
    let ghost_user = sc_vfs::UserId::new(999);
    let ghost_group = sc_vfs::GroupId::new(999);

    assert_eq!(svc.add_membership(ghost_user, eng), Err(GroupOpError::NotFound));
    assert_eq!(svc.add_membership(alice, ghost_group), Err(GroupOpError::NotFound));
    assert!(svc.list_group_members(eng).unwrap().is_empty());
}

/// Adding the same membership twice is a harmless no-op (`INSERT OR
/// IGNORE`), not a constraint-violation error — the admin UI's "add" button
/// does not need to first check whether the row already exists.
#[test]
fn adding_an_existing_membership_twice_is_a_no_op() {
    let (svc, _dir) = new_service(test_cfg());
    let alice = svc.create_user("alice", &pw("correct horse battery")).unwrap();
    let eng = svc.create_group("engineering").unwrap();
    svc.add_membership(alice, eng).unwrap();
    svc.add_membership(alice, eng).unwrap();
    assert_eq!(svc.list_group_members(eng).unwrap(), vec![alice]);
}

/// `delete_group` cascades to `membership` in one transaction — the same
/// property `delete_user_removes_dependent_rows` pins for accounts.
#[test]
fn delete_group_removes_membership_rows() {
    let (svc, _dir) = new_service(test_cfg());
    let alice = svc.create_user("alice", &pw("correct horse battery")).unwrap();
    let eng = svc.create_group("engineering").unwrap();
    svc.add_membership(alice, eng).unwrap();

    svc.delete_group(eng).unwrap();
    assert!(svc.list_groups().unwrap().is_empty());
    assert!(svc.list_memberships_all().unwrap().is_empty());
}

#[test]
fn delete_group_unknown_is_reported() {
    let (svc, _dir) = new_service(test_cfg());
    let ghost = sc_vfs::GroupId::new(999);
    assert_eq!(svc.delete_group(ghost), Err(GroupOpError::NotFound));
}

// ------------------------------------------------- master key rotation --
// `FEATURES.md` #156. These are end-to-end proofs, not unit-level seal/open
// round trips: they drive the exact same code paths a real client would
// (`login`+`verify_totp`, `verify_basic`, `export_smbpasswd`) against a
// freshly constructed `AuthService` built with the *new* key only, to prove
// nothing about the plaintext depends on the process that rotated still
// being alive.

/// SMB NT hash, TOTP secret, and app password all still authenticate against
/// a brand-new `AuthService` opened with the rotated key — proving the
/// rotation is real re-encryption, not just a version-number bump, and that
/// nothing about it depends on carrying state in the process that performed
/// the rotation.
#[tokio::test]
async fn rotation_survives_and_every_credential_kind_still_authenticates() {
    // `new_service` always builds against the fixed key `[7u8; 32]`.
    let new_key = [9u8; 32];
    let (svc, dir) = new_service(test_cfg());
    let db_path = dir.path().join("auth.db");

    // SMB NT hash: derived unconditionally at account creation.
    let alice = svc.create_user("alice", &pw("alicepassword1")).unwrap();
    assert!(svc.nt_hash_present(alice).unwrap());

    // TOTP secret + recovery codes.
    let bob = svc.create_user("bob", &pw("bobpassword1")).unwrap();
    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    let enroll_code = totp.generate_current().unwrap();
    svc.totp_enroll(bob, &pw("bobpassword1"), &secret, &enroll_code).unwrap();

    // App password: never master-key-encrypted (plain SHA-256), included
    // anyway so the proof covers all three kinds the task names.
    let (app_id, app_token) = svc.issue_app_password(alice, "test device", Scope::default()).unwrap();

    let report = svc.rotate_master_key(&new_key).unwrap();
    assert_eq!(report.old_key_ver, 1);
    assert_eq!(report.new_key_ver, 2);
    assert_eq!(report.smb_secrets_rotated, 1, "only alice has an NT hash (bob's was deleted by totp_enroll)");
    assert_eq!(report.totp_secrets_rotated, 1, "only bob has a TOTP secret");

    // Drop the pre-rotation service and open a fresh one against the same
    // database with *only* the new key, exactly as `sc-server`'s CLI does
    // after swapping the key file and the process restarts.
    drop(svc);
    let svc2 = AuthService::new(&db_path, test_cfg(), new_key).unwrap();

    // SMB: the real production decrypt path (`export_smbpasswd`), not a
    // direct `open_nt` call.
    let smbpasswd = svc2.export_smbpasswd(1000).unwrap();
    let expected_nt: String = nt_hash::nt_hash("alicepassword1").iter().map(|b| format!("{b:02X}")).collect();
    assert!(
        smbpasswd.contains(&format!(":{expected_nt}:")),
        "smbpasswd export must contain alice's correct NT hash after rotation: {smbpasswd}"
    );

    // TOTP: full login + second-factor flow, with a code generated fresh
    // right now from the *original* secret (rotation must not have touched
    // the secret's value, only what it's wrapped in).
    let LoginResult::TotpRequired { challenge } = svc2.login("bob", &pw("bobpassword1"), local_ip()).await else {
        panic!("expected totp_required after rotation");
    };
    let code_after_rotation = totp.generate_current().unwrap();
    let verified = svc2.verify_totp(&challenge, &code_after_rotation).await.unwrap();
    assert_eq!(verified, Some(bob), "bob's TOTP secret must still verify a live code after rotation");

    // App password: unaffected by rotation (not master-key material), but
    // checked so the proof covers all three kinds named in the task.
    let result = svc2.verify_basic("alice", &pw(&app_token), local_ip()).await;
    match result {
        BasicResult::Ok(p) => {
            assert_eq!(p.user, alice);
            assert!(matches!(p.via, AuthVia::AppPassword(id) if id == app_id));
        }
        other => panic!("expected Ok, got {other:?}"),
    }
}

/// A rotation that fails partway (simulated here by corrupting one row so
/// its decryption fails, standing in for a process kill between reading and
/// re-sealing another row) must leave the database exactly as it was: still
/// on the old `key_ver`, and every row still readable under the old key.
/// `rotate_master_key` never touches `key_version` or any row until its one
/// transaction commits, and it only commits after every row has been
/// re-sealed — a failure anywhere aborts the whole function before that
/// commit runs, so the in-progress `Transaction` is simply dropped, which
/// rusqlite rolls back automatically. That rollback, not any explicit
/// cleanup code, is what this test is proving.
#[test]
fn interrupted_rotation_leaves_every_record_readable_under_the_old_key() {
    let old_key = [7u8; 32];
    let new_key = [9u8; 32];
    let (svc, dir) = new_service(test_cfg());
    let db_path = dir.path().join("auth.db");

    // `alice` sorts before `mallory` in `user_smb_secret`'s natural (rowid)
    // order, so by the time the corrupted row is reached, alice's row has
    // already had its `UPDATE` executed *inside* the still-open transaction
    // — this is what makes the test meaningful: it proves that an update
    // already issued against the transaction, not just work never started,
    // is rolled back too.
    let alice = svc.create_user("alice", &pw("alicepassword1")).unwrap();
    let mallory = svc.create_user("mallory", &pw("malloryapassword1")).unwrap();

    {
        let conn = svc.pool.get().unwrap();
        let ct: Vec<u8> = conn
            .query_row(
                "SELECT nt_hash_ct FROM user_smb_secret WHERE user = ?1",
                rusqlite::params![mallory.get()],
                |r| r.get(0),
            )
            .unwrap();
        let mut corrupted = ct;
        *corrupted.last_mut().unwrap() ^= 0xFF; // breaks the AEAD tag
        conn.execute(
            "UPDATE user_smb_secret SET nt_hash_ct = ?1 WHERE user = ?2",
            rusqlite::params![corrupted, mallory.get()],
        )
        .unwrap();
    }

    let err = svc.rotate_master_key(&new_key).unwrap_err();
    assert!(
        err.to_string().contains("mallory") || err.chain().any(|c| c.to_string().contains(&mallory.get().to_string())),
        "error should point at the row that failed to decrypt: {err:#}"
    );

    // `key_version` must not have moved.
    {
        let conn = svc.pool.get().unwrap();
        assert_eq!(db::current_key_version(&conn).unwrap(), 1);
    }

    // Alice's row — already `UPDATE`d inside the aborted transaction before
    // mallory's row was reached — must still open under the OLD key at the
    // OLD key_ver, proving the partial update was rolled back, not merely
    // left un-bumped.
    {
        let conn = svc.pool.get().unwrap();
        let (ct, key_ver): (Vec<u8>, u32) = conn
            .query_row(
                "SELECT nt_hash_ct, key_ver FROM user_smb_secret WHERE user = ?1",
                rusqlite::params![alice.get()],
                |r| Ok((r.get(0)?, r.get::<_, i64>(1)? as u32)),
            )
            .unwrap();
        assert_eq!(key_ver, 1);
        let opened = nt_hash::open_nt(&old_key, &ct, alice, 1).unwrap();
        assert_eq!(opened, nt_hash::nt_hash("alicepassword1"));
    }

    // And the database as a whole is still fully readable under the old
    // key: opening a fresh `AuthService` with it must succeed, not just the
    // one row checked above (this is `verify_master_key`'s job).
    drop(svc);
    let reopened = AuthService::new(&db_path, test_cfg(), old_key).map(|_| ()).map_err(|e| e.to_string());
    assert!(reopened.is_ok(), "the database must still be fully readable under the old key: {reopened:?}");
}

// ------------------------------------------------------- OIDC flows --
// `docs/proposals/stowcloud-0-oidc-login.md` §4.2 and §4.3.1. A flow row is
// the only server-side state an authorization-code round trip has, so what
// matters is that it can be spent exactly once and that expiry is visible to
// the caller rather than swallowed.

fn digest(tag: u8) -> [u8; 32] {
    [tag; 32]
}

fn login_flow(state: u8) -> NewOidcFlow<'static> {
    // Leaked once per call: `NewOidcFlow` borrows the verifier, and a test
    // helper that returns the struct cannot also own it. Bounded by the
    // number of assertions in this file.
    let verifier: &'static SecretString = Box::leak(Box::new(pw("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")));
    NewOidcFlow {
        state_hash: digest(state),
        binding_hash: digest(state.wrapping_add(100)),
        nonce_hash: digest(state.wrapping_add(200)),
        code_verifier: verifier,
        mode: OidcFlowMode::Login,
        link_user: None,
        return_to: None,
    }
}

/// The whole point of storing `sha256(state)` server-side: a callback URL
/// that has already been redeemed is worth nothing when it is replayed, and
/// the second attempt is indistinguishable from a `state` that was never
/// issued at all.
#[test]
fn oidc_flow_is_redeemable_exactly_once() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_oidc_flow(login_flow(1)).unwrap();

    let taken = svc.take_oidc_flow(&digest(1)).unwrap().expect("first take sees the flow");
    assert_eq!(taken.binding_hash, digest(101).to_vec());
    assert_eq!(taken.nonce_hash, digest(201).to_vec());
    assert_eq!(taken.mode, OidcFlowMode::Login);
    assert!(taken.link_user.is_none());

    assert!(
        svc.take_oidc_flow(&digest(1)).unwrap().is_none(),
        "a replayed state must find nothing, exactly like a state that was never issued"
    );
    assert!(svc.take_oidc_flow(&digest(99)).unwrap().is_none());
}

/// A link flow carries the account that started it and where to land
/// afterwards; both have to survive the IdP round trip verbatim, since the
/// callback re-checks `link_user` against the live session (§4.3.2 step 2).
#[test]
fn oidc_link_flow_round_trips_its_mode_and_target() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("nina", &pw("correct horse battery")).unwrap();
    svc.create_oidc_flow(NewOidcFlow {
        mode: OidcFlowMode::Link,
        link_user: Some(uid),
        return_to: Some("/settings/security"),
        ..login_flow(2)
    })
    .unwrap();

    let taken = svc.take_oidc_flow(&digest(2)).unwrap().unwrap();
    assert_eq!(taken.mode, OidcFlowMode::Link);
    assert_eq!(taken.link_user, Some(uid));
    assert_eq!(taken.return_to.as_deref(), Some("/settings/security"));
    assert_eq!(
        taken.code_verifier.expose_secret(),
        "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
        "PKCE only works if the verifier comes back byte-identical"
    );
}

/// `take_oidc_flow` hands an expired row back rather than reporting "no such
/// flow": the login screen says `oidc.expired` for one and `oidc.bad_state`
/// for the other (§5-2 table B), and only the caller knows the difference is
/// worth telling a human. The row is still consumed -- an expired flow is
/// spent, not retryable.
#[test]
fn expired_oidc_flow_is_returned_once_and_then_gone() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_oidc_flow(login_flow(3)).unwrap();
    let expired_at = db::now_ns() - 1_000_000_000;
    {
        let conn = svc.pool.get().unwrap();
        conn.execute(
            "UPDATE oidc_flow SET expires_ns = ?1 WHERE state_hash = ?2",
            rusqlite::params![expired_at, digest(3).to_vec()],
        )
        .unwrap();
    }

    let taken = svc.take_oidc_flow(&digest(3)).unwrap().expect("expiry is the caller's call to make");
    assert_eq!(taken.expires_ns, expired_at);
    assert!(svc.take_oidc_flow(&digest(3)).unwrap().is_none());
}

/// Two independent cleanup paths, because each covers the other's gap: a
/// deployment where callbacks arrive gets swept by `take_oidc_flow`, and one
/// where nobody ever finishes a login gets swept by the periodic pass.
#[test]
fn expired_oidc_flows_are_swept_both_opportunistically_and_periodically() {
    let (svc, _dir) = new_service(test_cfg());
    for state in [4u8, 5, 6] {
        svc.create_oidc_flow(login_flow(state)).unwrap();
    }
    let count = |svc: &AuthService| -> i64 {
        svc.pool
            .get()
            .unwrap()
            .query_row("SELECT COUNT(*) FROM oidc_flow", [], |r| r.get(0))
            .unwrap()
    };
    {
        // 4 and 5 are stale; 6 is still live.
        let conn = svc.pool.get().unwrap();
        conn.execute(
            "UPDATE oidc_flow SET expires_ns = ?1 WHERE state_hash IN (?2, ?3)",
            rusqlite::params![db::now_ns() - 1, digest(4).to_vec(), digest(5).to_vec()],
        )
        .unwrap();
    }

    // Redeeming an unrelated (missing) state still sweeps.
    assert!(svc.take_oidc_flow(&digest(200)).unwrap().is_none());
    assert_eq!(count(&svc), 1, "the opportunistic sweep drops both stale rows and keeps the live one");
    assert_eq!(svc.sweep_oidc_flows().unwrap(), 0, "nothing left for the periodic pass to find");

    {
        let conn = svc.pool.get().unwrap();
        conn.execute(
            "UPDATE oidc_flow SET expires_ns = ?1 WHERE state_hash = ?2",
            rusqlite::params![db::now_ns() - 1, digest(6).to_vec()],
        )
        .unwrap();
    }
    assert_eq!(svc.sweep_oidc_flows().unwrap(), 1);
    assert_eq!(count(&svc), 0);
}

/// The one plaintext column in the table is plaintext because PKCE requires
/// it (§4.2); `state` and the browser-binding cookie are not, and storing
/// either in the clear would hand a database reader a working callback.
#[test]
fn oidc_flow_stores_no_plaintext_state_or_binding() {
    let (svc, _dir) = new_service(test_cfg());
    svc.create_oidc_flow(login_flow(7)).unwrap();
    let conn = svc.pool.get().unwrap();
    let (state_hash, binding_hash): (Vec<u8>, Vec<u8>) = conn
        .query_row("SELECT state_hash, binding_hash FROM oidc_flow", [], |r| {
            Ok((r.get(0)?, r.get(1)?))
        })
        .unwrap();
    assert_eq!(state_hash.len(), 32);
    assert_eq!(binding_hash.len(), 32);
}

/// `create_session` stores whatever the caller says the method was, instead
/// of the literal `1` it used to write for everything. Asserted through
/// `list_sessions`, the only reader of the column, so this covers the round
/// trip rather than the `INSERT` alone.
///
/// The `AMR_TOTP` half of `DESIGN-AUTH.md` §3.2 is still unset by every
/// production path -- see `create_session`'s doc comment for why that
/// pre-existing bug is left alone here.
#[test]
fn create_session_records_the_method_it_was_told() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("olive", &pw("correct horse battery")).unwrap();
    svc.create_session(uid, local_ip(), "password-ua", AMR_PASSWORD).unwrap();
    svc.create_session(uid, local_ip(), "oidc-ua", AMR_OIDC).unwrap();

    let sessions = svc.list_sessions(uid).unwrap();
    let amr_of = |ua: &str| {
        sessions
            .iter()
            .find(|s| s.ua_first.as_deref() == Some(ua))
            .unwrap_or_else(|| panic!("no session for {ua}"))
            .amr
    };
    assert_eq!(amr_of("password-ua"), AMR_PASSWORD);
    assert_eq!(amr_of("oidc-ua"), AMR_OIDC);
}

// ---------------------------------------------------- OIDC identities --
// §4.2, §4.3.6. Linking is not just a row: it is the moment an account's
// password stops being an SMB credential, and unlinking is the moment the
// access the IdP vouched for has to end.

const ISSUER: &str = "https://idp.example.test/realms/sc";

/// Counts `republish` calls so a test can prove the passdb was asked to be
/// rewritten, not merely that the database row went away. The distinction is
/// the whole of §4.3.6 step 2.
#[derive(Default)]
struct CountingPassdb(std::sync::atomic::AtomicU64);

impl PassdbSink for CountingPassdb {
    fn republish(&self) {
        self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    }
}

impl CountingPassdb {
    fn count(&self) -> u64 {
        self.0.load(std::sync::atomic::Ordering::SeqCst)
    }
}

fn with_passdb_sink(svc: &AuthService) -> Arc<CountingPassdb> {
    let sink = Arc::new(CountingPassdb::default());
    assert!(svc.set_passdb_sink(sink.clone()));
    sink
}

#[test]
fn link_then_unlink_round_trip() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("paula", &pw("correct horse battery")).unwrap();
    assert!(!svc.oidc_linked(uid).unwrap());
    assert!(svc.find_oidc_identity(ISSUER, "sub-paula").unwrap().is_none());

    svc.link_oidc_identity(uid, ISSUER, "sub-paula").unwrap();
    assert!(svc.oidc_linked(uid).unwrap());
    assert_eq!(svc.find_oidc_identity(ISSUER, "sub-paula").unwrap(), Some(uid));

    let row = svc.oidc_identity_of(uid).unwrap().unwrap();
    assert_eq!(row.issuer, ISSUER);
    assert_eq!(row.subject, "sub-paula");
    assert!(row.linked_ns > 0);
    assert!(row.last_login_ns.is_none(), "no OIDC login has happened yet");
    svc.touch_oidc_last_login(uid);
    assert!(svc.oidc_identity_of(uid).unwrap().unwrap().last_login_ns.is_some());

    // A different issuer is a different identity: the primary key is the
    // pair, and lookup must not fall back to matching the subject alone.
    assert!(svc.find_oidc_identity("https://other.test", "sub-paula").unwrap().is_none());

    let out = svc.unlink_oidc_identity(uid, Some(&pw("correct horse battery"))).unwrap();
    assert!(out.smb_nt_restored);
    assert!(!svc.oidc_linked(uid).unwrap());
    assert!(svc.oidc_identity_of(uid).unwrap().is_none());
    assert!(svc.find_oidc_identity(ISSUER, "sub-paula").unwrap().is_none());
}

/// Re-linking the identity an account already has is success, not a
/// conflict: §4.3.2 makes the callback the only place that judges
/// linkability, so a double-submitted callback must not look like a failure.
#[test]
fn relinking_the_same_identity_is_idempotent() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("quinn", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-quinn").unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-quinn").unwrap();
    let conn = svc.pool.get().unwrap();
    let n: i64 = conn.query_row("SELECT COUNT(*) FROM oidc_identity", [], |r| r.get(0)).unwrap();
    assert_eq!(n, 1);
}

/// Both uniqueness rules, in both directions. One subject reaching two
/// accounts would make "whose access does cutting this link end" ambiguous;
/// one account holding two subjects would mean cutting one link leaves the
/// other still logging in.
#[test]
fn one_identity_per_account_in_both_directions() {
    let (svc, _dir) = new_service(test_cfg());
    let first = svc.create_user("rosa", &pw("correct horse battery")).unwrap();
    let second = svc.create_user("sven", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(first, ISSUER, "sub-shared").unwrap();

    assert_eq!(
        svc.link_oidc_identity(second, ISSUER, "sub-shared"),
        Err(OidcLinkError::SubjectTaken),
        "the same subject must not reach a second account"
    );
    assert_eq!(
        svc.link_oidc_identity(first, ISSUER, "sub-another"),
        Err(OidcLinkError::AlreadyLinked),
        "a linked account must not collect a second subject"
    );
    assert_eq!(
        svc.link_oidc_identity(second, ISSUER, ""),
        Err(OidcLinkError::InvalidSubject)
    );

    // Neither refusal left anything behind.
    assert!(!svc.oidc_linked(second).unwrap());
    assert_eq!(svc.oidc_identity_of(first).unwrap().unwrap().subject, "sub-shared");
}

/// The bypass §4.3.6 exists to close: `create_user` derives an NT hash
/// unconditionally, so an account linked later already has a live one and a
/// published `smbpasswd` line. Linking has to delete it *and* ask for the
/// file to be written again.
#[test]
fn linking_deletes_the_account_derived_nt_hash_and_republishes() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("tomas", &pw("correct horse battery")).unwrap();
    assert!(svc.nt_hash_present(uid).unwrap(), "new accounts are SMB-ready by construction");

    svc.link_oidc_identity(uid, ISSUER, "sub-tomas").unwrap();

    assert!(!svc.nt_hash_present(uid).unwrap());
    assert!(
        !svc.export_smbpasswd(1000).unwrap().contains("tomas"),
        "the rendered passdb must no longer carry a linked account"
    );
    assert_eq!(sink.count(), 1, "deleting the row without republishing leaves SMB open");
}

/// A dedicated SMB password is not the account password, so linking has no
/// business removing it, the same exception `totp_enroll` makes (§2.4).
#[test]
fn linking_leaves_a_dedicated_smb_password_alone() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("ulla", &pw("correct horse battery")).unwrap();
    {
        let conn = svc.pool.get().unwrap();
        svc.store_nt_from_plaintext(&conn, uid, "a separate smb password", nt_ops::NT_SOURCE_DEDICATED)
            .unwrap();
    }

    svc.link_oidc_identity(uid, ISSUER, "sub-ulla").unwrap();

    assert!(svc.nt_hash_present(uid).unwrap());
    let expected: String = nt_hash::nt_hash("a separate smb password").iter().map(|b| format!("{b:02X}")).collect();
    assert!(svc.export_smbpasswd(1000).unwrap().contains(&format!(":{expected}:")));
    assert_eq!(sink.count(), 0, "nothing changed, so nothing to republish");
}

/// Every path that could hand a linked account a working NT hash back again.
/// Each of these is a bypass that would repair itself the first time the user
/// did something ordinary.
#[tokio::test]
async fn nothing_re_derives_an_nt_hash_while_an_identity_is_linked() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("vera", &pw("correct horse battery")).unwrap();

    // TOTP as well as OIDC, so the disable path below has something to do.
    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    svc.totp_enroll(uid, &pw("correct horse battery"), &secret, &totp.generate_current().unwrap()).unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-vera").unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap());

    // Opportunistic backfill on a successful password login (`nt_ops.rs`).
    let LoginResult::TotpRequired { .. } = svc.login("vera", &pw("correct horse battery"), local_ip()).await else {
        panic!("expected totp_required");
    };
    assert!(!svc.nt_hash_present(uid).unwrap(), "login backfill must stay suppressed while linked");

    // Turning TOTP back off re-derives from the plaintext it just verified.
    svc.totp_disable(uid, &pw("correct horse battery")).await.unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap(), "totp_disable must not restore SMB for a linked account");

    // An explicit password change (`users.rs`), the loudest of the three.
    svc.set_password(uid, &pw("a brand new password")).unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap(), "set_password must not restore SMB for a linked account");

    // And once unlinked, the ordinary lifecycle resumes.
    svc.unlink_oidc_identity(uid, Some(&pw("a brand new password"))).unwrap();
    assert!(svc.nt_hash_present(uid).unwrap());
}

/// The self-service unlink holds the plaintext and restores SMB on the spot;
/// the admin unlink does not hold it, cannot, and has to say so rather than
/// leave the account quietly unable to reach its files over SMB (§4.3.6).
#[test]
fn unlink_restores_smb_only_when_it_is_given_the_password() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);

    let admin_path = svc.create_user("wanda", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(admin_path, ISSUER, "sub-wanda").unwrap();
    let out = svc.unlink_oidc_identity(admin_path, None).unwrap();
    assert!(!out.smb_nt_restored);
    assert!(!svc.nt_hash_present(admin_path).unwrap());

    let self_path = svc.create_user("xenia", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(self_path, ISSUER, "sub-xenia").unwrap();
    let republished_after_links = sink.count();
    let out = svc.unlink_oidc_identity(self_path, Some(&pw("correct horse battery"))).unwrap();
    assert!(out.smb_nt_restored);
    let expected: String = nt_hash::nt_hash("correct horse battery").iter().map(|b| format!("{b:02X}")).collect();
    assert!(svc.export_smbpasswd(1000).unwrap().contains(&format!(":{expected}:")));
    assert_eq!(sink.count(), republished_after_links + 1, "a restored hash needs publishing too");

    // A wrong password re-confirmation neither unlinks nor re-derives.
    let guarded = svc.create_user("yusuf", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(guarded, ISSUER, "sub-yusuf").unwrap();
    assert_eq!(
        svc.unlink_oidc_identity(guarded, Some(&pw("not the password"))),
        Err(OidcUnlinkError::BadPassword)
    );
    assert!(svc.oidc_linked(guarded).unwrap());
    assert!(!svc.nt_hash_present(guarded).unwrap());

    assert_eq!(
        svc.unlink_oidc_identity(admin_path, None),
        Err(OidcUnlinkError::NotLinked),
        "unlinking twice is a 404, not a silent success"
    );
}

// ------------------------------------------------ passdb publication --
// §4.3.6 step 2 is not the OIDC link's alone. `smbd` authenticates against
// the file this server last published, so every path that changes what
// `export_smbpasswd` would produce has to ask for that file to be written
// again. `PassdbSink`'s own doc comment lists the paths and the two that
// deliberately stay silent.

/// Without this the password a user just replaced keeps working over SMB:
/// the database has the new hash and the published file has the old one.
#[test]
fn a_password_change_republishes_the_passdb() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("bertil", &pw("correct horse battery")).unwrap();
    assert_eq!(
        sink.count(),
        0,
        "a new account only ever adds a hash, so a stale file there refuses access rather than granting it"
    );

    svc.set_password(uid, &pw("a different long password")).unwrap();

    let expected: String = nt_hash::nt_hash("a different long password").iter().map(|b| format!("{b:02X}")).collect();
    assert!(svc.export_smbpasswd(1000).unwrap().contains(&format!(":{expected}:")));
    assert_eq!(sink.count(), 1, "the file smbd reads still holds the previous password's hash until this fires");
}

/// The linked account's password change is the one that must *not* publish:
/// §4.3.6 step 3 stops it re-deriving at all, so there is nothing new to
/// write and a republish would only claim otherwise.
#[test]
fn a_linked_accounts_password_change_has_nothing_to_republish() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("cecilia", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-cecilia").unwrap();
    let after_link = sink.count();
    assert_eq!(after_link, 1);

    svc.set_password(uid, &pw("a different long password")).unwrap();

    assert!(!svc.nt_hash_present(uid).unwrap());
    assert_eq!(sink.count(), after_link, "no derivation happened, so there is no new file to write");
}

/// Field 2 of every smbpasswd line is the service uid the caller asks for,
/// the same one the passwd entries beside it carry — never the account's row
/// id. Samba resolves the line through that uid, and `pdbedit -i` imports
/// nothing at all, without an error or a log line, when it names no passwd
/// entry. That is what shipped: the row id went into field 2, so an SMB login
/// failed `NT_STATUS_NO_SUCH_USER` on every deployment whose ids did not
/// happen to equal the service uid.
///
/// Three accounts, so the assertion cannot pass on a coincidence: row ids 1,
/// 2 and 3 all have to render as 1000. Every other test here matches on the
/// NT hash substring, which is exactly why none of them saw this.
#[test]
fn smbpasswd_uids_are_the_base_plus_the_row_id_and_all_distinct() {
    let (svc, _dir) = new_service(test_cfg());
    let ids: Vec<u32> = ["alice", "bob", "carol"]
        .iter()
        .map(|n| svc.create_user(n, &pw("correct horse battery")).unwrap().get())
        .collect();

    let out = svc.export_smbpasswd(1000).unwrap();
    let uids: Vec<&str> = out
        .lines()
        .map(|l| l.split(':').nth(1).unwrap())
        .collect();
    assert_eq!(uids.len(), 3, "every enabled account exports: {out:?}");

    let want: Vec<String> = ids.iter().map(|id| (1000 + id).to_string()).collect();
    assert_eq!(uids, want, "field 2 is base + row id: {out:?}");

    // The property that actually matters to `pdbedit -i`, stated directly:
    // duplicates here mean it resolves several names to one account and
    // imports only that one.
    let mut sorted = uids.clone();
    sorted.sort_unstable();
    sorted.dedup();
    assert_eq!(sorted.len(), 3, "uids must be distinct per account");

    // The base is `smb.service_uid`, not a constant.
    assert!(svc
        .export_smbpasswd(200_000)
        .unwrap()
        .starts_with(&format!("alice:{}:", 200_000 + ids[0])));
}

/// Opting out is a withdrawal of consent, and it has to reach the published
/// file for the same reason linking does. Saving the toggles unchanged is
/// not a change, and re-rendering three files for it would be work nobody
/// asked for.
#[test]
fn the_smb_toggles_republish_only_when_they_change_something() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("dagny", &pw("correct horse battery")).unwrap();

    svc.set_smb_settings(uid, true, true).unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap());
    assert_eq!(sink.count(), 1);

    svc.set_smb_settings(uid, true, true).unwrap();
    assert_eq!(sink.count(), 1, "the settings screen saving the values it already had is not a change");

    // `export_smbpasswd` filters on `smb_enabled`, so that half counts too
    // even though it touches no NT hash.
    svc.set_smb_settings(uid, false, false).unwrap();
    assert_eq!(sink.count(), 2);
}

/// TOTP enrollment deletes the account-derived hash for exactly the reason
/// linking does, and left the published file behind for exactly as long.
#[tokio::test]
async fn turning_totp_on_and_off_republishes_both_ways() {
    let (svc, _dir) = new_service(test_cfg());
    let sink = with_passdb_sink(&svc);
    let uid = svc.create_user("evert", &pw("correct horse battery")).unwrap();

    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    svc.totp_enroll(uid, &pw("correct horse battery"), &secret, &totp.generate_current().unwrap()).unwrap();
    assert!(!svc.nt_hash_present(uid).unwrap());
    assert_eq!(sink.count(), 1, "the second factor is not a second factor if SMB still takes the password alone");

    svc.totp_disable(uid, &pw("correct horse battery")).await.unwrap();
    assert!(svc.nt_hash_present(uid).unwrap());
    assert_eq!(sink.count(), 2, "the hash is back in the database and the file does not know it yet");
}

/// `validate_session` never looks at `amr` or at the identity table, so
/// removing the link on its own would leave every session the IdP vouched for
/// alive. Password sessions are not the IdP's to revoke and stay.
#[test]
fn unlink_revokes_oidc_sessions_and_leaves_password_sessions_alone() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("zara", &pw("correct horse battery")).unwrap();
    let other = svc.create_user("adam", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-zara").unwrap();
    svc.link_oidc_identity(other, ISSUER, "sub-adam").unwrap();

    let from_password = svc.create_session(uid, local_ip(), "browser", AMR_PASSWORD).unwrap();
    let from_oidc = svc.create_session(uid, local_ip(), "browser", AMR_OIDC).unwrap();
    let untouched = svc.create_session(other, local_ip(), "browser", AMR_OIDC).unwrap();

    let out = svc.unlink_oidc_identity(uid, None).unwrap();
    assert_eq!(out.oidc_sessions_revoked, 1);

    assert!(svc.validate_session(from_oidc.as_str()).unwrap().is_none());
    assert!(
        svc.validate_session(from_password.as_str()).unwrap().is_some(),
        "unlinking withdraws one way in, not every way in"
    );
    assert!(
        svc.validate_session(untouched.as_str()).unwrap().is_some(),
        "another account's OIDC session is none of this unlink's business"
    );
}

/// SQLite recycles freed `INTEGER PRIMARY KEY` values, so an `oidc_identity`
/// row that outlived its account would not be an orphan. It would be a live
/// credential aimed at whoever gets that id next.
#[test]
fn deleting_an_account_takes_its_oidc_identity_with_it() {
    let (svc, _dir) = new_service(test_cfg());
    let keep = svc.create_user("keeper", &pw("correct horse battery")).unwrap();
    svc.set_admin(keep, true).unwrap();
    let gone = svc.create_user("goner", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(gone, ISSUER, "sub-goner").unwrap();

    svc.delete_user(gone).unwrap();

    assert!(svc.find_oidc_identity(ISSUER, "sub-goner").unwrap().is_none());
    let conn = svc.pool.get().unwrap();
    let n: i64 = conn.query_row("SELECT COUNT(*) FROM oidc_identity", [], |r| r.get(0)).unwrap();
    assert_eq!(n, 0);
}

/// §4.3.5, the whole carve-out: a linked account reaches DAV with an app
/// password and not with its account password.
#[tokio::test]
async fn dav_basic_refuses_a_linked_account_password_but_takes_its_app_password() {
    let (svc, _dir) = new_service(test_cfg());
    let uid = svc.create_user("bruno", &pw("correct horse battery")).unwrap();
    let (_id, app_token) = svc.issue_app_password(uid, "sync client", Scope::default()).unwrap();

    // Before linking, both work.
    assert!(matches!(svc.verify_basic("bruno", &pw("correct horse battery"), local_ip()).await, BasicResult::Ok(_)));

    svc.link_oidc_identity(uid, ISSUER, "sub-bruno").unwrap();

    assert!(
        matches!(
            svc.verify_basic("bruno", &pw("correct horse battery"), local_ip()).await,
            BasicResult::AppPasswordRequired
        ),
        "the account password must stop working over DAV the moment an identity is linked"
    );
    assert!(
        matches!(svc.verify_basic("bruno", &pw(&app_token), local_ip()).await, BasicResult::Ok(_)),
        "app passwords are the supported route and are unaffected"
    );

    // And the refusal is not sticky: unlinking gives the password back.
    svc.unlink_oidc_identity(uid, Some(&pw("correct horse battery"))).unwrap();
    assert!(matches!(svc.verify_basic("bruno", &pw("correct horse battery"), local_ip()).await, BasicResult::Ok(_)));
}

/// The reason the refusal above sits after Argon2 rather than beside the
/// `totp_enabled` check: a wrong password has to cost the same, and answer
/// the same, whether or not the account uses SSO. Otherwise an
/// unauthenticated caller can enumerate which accounts are linked by timing
/// alone.
///
/// Timing itself is not assertable in a unit test, so this asserts the thing
/// that produces it: the same verdict, reached through the same real Argon2
/// verification.
#[tokio::test]
async fn a_wrong_dav_password_reveals_nothing_about_whether_an_account_is_linked() {
    let (svc, _dir) = new_service(test_cfg());
    let linked = svc.create_user("carla", &pw("correct horse battery")).unwrap();
    svc.create_user("dario", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(linked, ISSUER, "sub-carla").unwrap();

    let before = svc.argon2_calls();
    let linked_result = svc.verify_basic("carla", &pw("wrong password here"), local_ip()).await;
    let linked_cost = svc.argon2_calls() - before;

    let before = svc.argon2_calls();
    let plain_result = svc.verify_basic("dario", &pw("wrong password here"), local_ip()).await;
    let plain_cost = svc.argon2_calls() - before;

    assert!(matches!(linked_result, BasicResult::Invalid), "not AppPasswordRequired: that would be the giveaway");
    assert!(matches!(plain_result, BasicResult::Invalid));
    assert_eq!(linked_cost, 1, "a linked account must still pay for a real Argon2 verification");
    assert_eq!(linked_cost, plain_cost);
}

/// `oidc.local_password_login` (§4.3.5). The default stays permissive because
/// `deny` has no way out when the IdP is the thing that broke; when an
/// operator does choose `deny`, it refuses a linked account and nobody else,
/// and it refuses with the same answer a wrong password gets.
#[tokio::test]
async fn local_password_login_deny_blocks_only_linked_accounts() {
    assert_eq!(
        AuthConfig::default().oidc_local_password_login,
        OidcLocalPasswordLogin::Allow,
        "a deployment whose IdP breaks must still be recoverable by its administrator"
    );

    let (allowing, _dir) = new_service(test_cfg());
    let uid = allowing.create_user("elif", &pw("correct horse battery")).unwrap();
    allowing.link_oidc_identity(uid, ISSUER, "sub-elif").unwrap();
    assert!(
        matches!(allowing.login("elif", &pw("correct horse battery"), local_ip()).await, LoginResult::Ok(_)),
        "the default lets a linked account keep using its local password"
    );

    let denying_cfg = AuthConfig {
        oidc_local_password_login: OidcLocalPasswordLogin::Deny,
        ..test_cfg()
    };
    let (denying, _dir2) = new_service(denying_cfg);
    let linked = denying.create_user("elif", &pw("correct horse battery")).unwrap();
    denying.create_user("fatima", &pw("correct horse battery")).unwrap();
    denying.link_oidc_identity(linked, ISSUER, "sub-elif").unwrap();

    assert!(
        matches!(denying.login("elif", &pw("correct horse battery"), local_ip()).await, LoginResult::Invalid),
        "under deny, a linked account's local password does not open the web UI"
    );
    assert!(
        matches!(denying.login("fatima", &pw("correct horse battery"), local_ip()).await, LoginResult::Ok(_)),
        "an unlinked account is untouched by the policy"
    );
}

/// The refusal is applied after the password verifies, so a wrong password
/// costs a real Argon2 verification and answers `Invalid` whether or not the
/// account is linked. Same argument as the DAV carve-out's placement,
/// on the web login path.
#[tokio::test]
async fn local_password_deny_does_not_answer_faster_for_a_linked_account() {
    let cfg = AuthConfig {
        oidc_local_password_login: OidcLocalPasswordLogin::Deny,
        ..test_cfg()
    };
    let (svc, _dir) = new_service(cfg);
    let linked = svc.create_user("gunnar", &pw("correct horse battery")).unwrap();
    svc.create_user("hilde", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(linked, ISSUER, "sub-gunnar").unwrap();

    let before = svc.argon2_calls();
    let linked_result = svc.login("gunnar", &pw("wrong password here"), local_ip()).await;
    let linked_cost = svc.argon2_calls() - before;

    let before = svc.argon2_calls();
    let plain_result = svc.login("hilde", &pw("wrong password here"), local_ip()).await;
    let plain_cost = svc.argon2_calls() - before;

    assert!(matches!(linked_result, LoginResult::Invalid));
    assert!(matches!(plain_result, LoginResult::Invalid));
    assert_eq!(linked_cost, 1);
    assert_eq!(linked_cost, plain_cost);
}

/// Password re-confirmation (`DESIGN-AUTH.md` §6.4) is what
/// `POST /api/auth/oidc/link/start` charges before it will start a flow that
/// ends in a new permanent credential on the account.
///
/// The three assertions here are the three ways `login()` would have been the
/// wrong function to reuse: a linked account under
/// `oidc.local_password_login = "deny"` must still be able to re-confirm (or
/// it could never unlink), a TOTP account must not be asked for a second
/// factor its live session already established, and a wrong password is still
/// wrong.
#[tokio::test]
async fn reconfirm_password_answers_for_accounts_login_would_refuse() {
    let cfg = AuthConfig {
        oidc_local_password_login: OidcLocalPasswordLogin::Deny,
        ..test_cfg()
    };
    let (svc, _dir) = new_service(cfg);
    let uid = svc.create_user("ingrid", &pw("correct horse battery")).unwrap();
    svc.link_oidc_identity(uid, ISSUER, "sub-ingrid").unwrap();

    // `login` refuses this account entirely under `deny`...
    assert!(matches!(
        svc.login("ingrid", &pw("correct horse battery"), local_ip()).await,
        LoginResult::Invalid
    ));
    // ...while re-confirmation, which asks a different question, does not.
    assert!(svc.reconfirm_password(uid, &pw("correct horse battery")).await);
    assert!(!svc.reconfirm_password(uid, &pw("wrong password here")).await);

    // A TOTP account re-confirms with the password alone: `login` would have
    // answered `TotpRequired`, which is not an answer this caller can use.
    let totp_uid = svc.create_user("jonas", &pw("correct horse battery")).unwrap();
    let secret = random_totp_secret();
    let secret_bytes = totp_rs::Secret::Encoded(secret.clone()).to_bytes().unwrap();
    let totp = totp_rs::TOTP::new(totp_rs::Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new()).unwrap();
    svc.totp_enroll(totp_uid, &pw("correct horse battery"), &secret, &totp.generate_current().unwrap()).unwrap();
    assert!(svc.reconfirm_password(totp_uid, &pw("correct horse battery")).await);

    // An account that does not exist answers the same as a wrong password.
    assert!(!svc.reconfirm_password(sc_vfs::UserId::new(4242), &pw("correct horse battery")).await);
}
