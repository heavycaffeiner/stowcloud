use crate::config::AuthConfig;
use crate::AuthService;
use anyhow::{anyhow, Result};
use argon2::password_hash::rand_core::OsRng;
use argon2::password_hash::{PasswordHash, PasswordHasher, PasswordVerifier, SaltString};
use argon2::{Algorithm, Argon2, Params, Version};
use secrecy::{ExposeSecret, SecretString};

fn argon2_for(cfg: &AuthConfig) -> Result<Argon2<'static>> {
    let params = Params::new(cfg.argon2_m_cost_kib, cfg.argon2_t_cost, cfg.argon2_p_cost, Some(32))
        .map_err(|e| anyhow!("bad argon2 params: {e}"))?;
    Ok(Argon2::new(Algorithm::Argon2id, Version::V0x13, params))
}

/// Hashes a password to a PHC string using the current configured params.
/// This is the CPU-heavy part; callers on the async path should run it
/// inside `spawn_blocking` under the Argon2 semaphore permit.
pub(crate) fn hash_password(cfg: &AuthConfig, pw: &SecretString) -> Result<String> {
    let argon2 = argon2_for(cfg)?;
    let salt = SaltString::generate(&mut OsRng);
    let hash = argon2
        .hash_password(pw.expose_secret().as_bytes(), &salt)
        .map_err(|e| anyhow!("argon2 hash failed: {e}"))?;
    Ok(hash.to_string())
}

/// Verifies `pw` against a stored PHC string. Never panics on malformed
/// hashes (returns false instead) so a corrupt row can't crash a request.
pub(crate) fn verify_password_sync(hash_phc: &str, pw: &str) -> bool {
    let parsed = match PasswordHash::new(hash_phc) {
        Ok(h) => h,
        Err(_) => return false,
    };
    Argon2::default().verify_password(pw.as_bytes(), &parsed).is_ok()
}

/// True if `hash_phc`'s embedded Argon2 params differ from the currently
/// configured defaults (i.e. it should be rehashed on next successful login).
pub(crate) fn needs_rehash(hash_phc: &str, cfg: &AuthConfig) -> bool {
    let parsed = match PasswordHash::new(hash_phc) {
        Ok(h) => h,
        Err(_) => return true,
    };
    let m = parsed
        .params
        .get("m")
        .and_then(|v| v.decimal().ok())
        .unwrap_or(0);
    let t = parsed
        .params
        .get("t")
        .and_then(|v| v.decimal().ok())
        .unwrap_or(0);
    let p = parsed
        .params
        .get("p")
        .and_then(|v| v.decimal().ok())
        .unwrap_or(0);
    m != cfg.argon2_m_cost_kib || t != cfg.argon2_t_cost || p != cfg.argon2_p_cost
}

/// Generates a DUMMY_HASH at startup with current params, for account
/// enumeration resistance (§7.2): unknown users still run a real Argon2
/// verify against this hash so timing doesn't reveal existence.
pub(crate) fn make_dummy_hash(cfg: &AuthConfig) -> Result<String> {
    let mut buf = [0u8; 32];
    getrandom::getrandom(&mut buf).map_err(|e| anyhow!("getrandom failed: {e}"))?;
    let pw = SecretString::from(data_encoding::BASE64URL_NOPAD.encode(&buf));
    hash_password(cfg, &pw)
}

// ---------------------------------------------------------------------------
// Reusable primitives for *other* low-frequency secrets
// ---------------------------------------------------------------------------
//
// Share-link passwords ("Argon2 — low frequency, so
// a slow hash is fine") are hashed with the same Argon2id parameters as
// account passwords.
// They are exported here rather than reimplemented in `sc-core` so there is
// exactly one place in the workspace where those cost parameters live: a
// second copy would drift, and the weaker of the two would silently become
// the one an attacker targets.

/// Hash `pw` to a PHC string using `cfg`'s Argon2id parameters. CPU-heavy —
/// async callers must wrap this in `spawn_blocking`.
pub fn hash_phc(cfg: &AuthConfig, pw: &SecretString) -> Result<String> {
    hash_password(cfg, pw)
}

/// Verify `pw` against a stored PHC string. Parameters come from the hash
/// itself, so this keeps validating hashes made with older costs. Never
/// panics on a malformed hash — returns `false`.
pub fn verify_phc(hash_phc: &str, pw: &str) -> bool {
    verify_password_sync(hash_phc, pw)
}

/// A hash of a random secret nobody holds, for existence-oracle resistance:
/// a lookup that found nothing still runs a real verify against this so the
/// "no such record" path costs the same as the "wrong secret" path.
pub fn dummy_phc(cfg: &AuthConfig) -> Result<String> {
    make_dummy_hash(cfg)
}

impl AuthService {
    /// Argon2 verification on the async path: bounded by the concurrency
    /// gate and run in `spawn_blocking` since Argon2 is
    /// CPU-bound — the gate's blocking acquire happens *inside* that
    /// blocking task, not on the runtime worker thread. Increments the
    /// (test-only-observed) invocation counter unconditionally, including
    /// for the account-enumeration dummy hash — that counter is exactly
    /// what proves the cred cache is skipping this path on a hit.
    pub(crate) async fn verify_password_async(&self, hash_phc: &str, pw: SecretString) -> bool {
        self.argon2_calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        let hash_phc = hash_phc.to_string();
        let gate = std::sync::Arc::clone(&self.argon2_gate);
        tokio::task::spawn_blocking(move || {
            let _permit = gate.acquire();
            verify_password_sync(&hash_phc, pw.expose_secret())
        })
        .await
        .unwrap_or(false)
    }

    /// Argon2 hashing on the *synchronous* path: `create_user`,
    /// `set_password`, `maybe_rehash`, and anything else that hashes a
    /// password without an executor to hand off to. Bounded by the same
    /// gate as the async path (§2.2) — see `argon_gate` module docs for why
    /// one shared gate, not a second independent one, is required.
    ///
    /// Blocks the calling OS thread while waiting for a permit. Callers
    /// that are themselves inside an async fn must run this via
    /// `spawn_blocking`, exactly as the CPU cost of Argon2 itself already
    /// requires.
    pub(crate) fn hash_password_gated(&self, pw: &SecretString) -> Result<String> {
        let _permit = self.argon2_gate.acquire();
        self.argon2_calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        hash_password(&self.cfg, pw)
    }

    /// Argon2 verification on the synchronous path (`totp_enroll`'s
    /// password re-confirmation). See `hash_password_gated` — same gate,
    /// same blocking-thread caveat.
    pub(crate) fn verify_password_sync_gated(&self, hash_phc: &str, pw: &str) -> bool {
        let _permit = self.argon2_gate.acquire();
        self.argon2_calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        verify_password_sync(hash_phc, pw)
    }

    /// Password re-confirmation for an *already authenticated* account:
    /// the caller holds a valid session and is about
    /// to change a security control, so it has to prove the password again.
    ///
    /// Deliberately not `login()`. That function answers a different question
    /// -- "may this name and password start a session" -- and carries three
    /// behaviours that are wrong here: it spends the login rate budget of
    /// whoever is already signed in, it can answer `TotpRequired` (a second
    /// factor the live session already established), and since
    /// `oidc.local_password_login = "deny"` landed it refuses a linked
    /// account outright, which would make an OIDC user unable to re-confirm
    /// in order to *unlink*. Re-confirmation asks only whether these bytes
    /// are this account's password.
    ///
    /// It takes no rate limit of its own for the same reason `totp_enroll`'s
    /// re-confirmation does not: reaching it already required a valid session
    /// cookie and a matching CSRF token, and the Argon2 gate bounds the CPU
    /// cost regardless of who is asking.
    ///
    /// `false` for an account that does not exist, same as a wrong password.
    pub async fn reconfirm_password(&self, u: sc_vfs::UserId, pw: &SecretString) -> bool {
        let Ok(stored) = self.pw_hash_of(u) else {
            return false;
        };
        self.verify_password_async(&stored, pw.clone()).await
    }
}
