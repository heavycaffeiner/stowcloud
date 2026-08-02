use crate::crockford;
use crate::db::now_ns;
use crate::nt_hash::{open_totp_secret, seal_totp_secret};
use crate::nt_ops::{NT_SOURCE_ACCOUNT, NT_SOURCE_DEDICATED};
use crate::{AuthService, TotpSetup};
use anyhow::{anyhow, bail, Result};
use sc_vfs::UserId;
use secrecy::{ExposeSecret, SecretString};
use sha2::{Digest, Sha256};
use totp_rs::{Algorithm, Secret, TOTP};

fn decode_secret(s: &str) -> Result<Vec<u8>> {
    Secret::Encoded(s.to_string())
        .to_bytes()
        .map_err(|e| anyhow!("bad totp secret: {e:?}"))
}

fn build_totp(secret_bytes: Vec<u8>) -> Result<TOTP> {
    TOTP::new(Algorithm::SHA1, 6, 1, 30, secret_bytes, None, String::new())
        .map_err(|e| anyhow!("bad totp params: {e}"))
}

fn recovery_hash(code: &str) -> Vec<u8> {
    Sha256::digest(code.as_bytes()).to_vec()
}

/// Mints 10 fresh recovery codes (10 × 10-char
/// Crockford-Base32, `sha256` stored, one-time use). Shared by `totp_enroll`
/// and `reissue_recovery_codes` so there is exactly one place deciding the
/// alphabet, length, and hash function a code is minted with — two
/// independent generators is how a later change to either would silently
/// leave the two paths hashing codes differently.
fn generate_recovery_codes() -> Result<(Vec<String>, Vec<Vec<u8>>)> {
    let mut codes = Vec::with_capacity(10);
    let mut hashes = Vec::with_capacity(10);
    for _ in 0..10 {
        let mut raw = [0u8; 7]; // ~56 bits -> 10 Crockford chars (well over 50 bits)
        getrandom::getrandom(&mut raw).map_err(|e| anyhow!("getrandom failed: {e}"))?;
        let code = crockford::encode(&raw)[..10].to_string();
        hashes.push(recovery_hash(&code));
        codes.push(code);
    }
    Ok((codes, hashes))
}

impl AuthService {
    /// Generates a random secret and its `otpauth://` URL for `account_name`
    /// but **persists nothing** — this is step one of a two-step enrollment:
    /// the caller shows the QR/manual-entry secret,
    /// the user scans it and types back a current code, and only
    /// [`Self::totp_enroll`] (which re-verifies that code and the account
    /// password) actually writes the seal secret to the database. Calling
    /// this twice in a row simply throws the first secret away; nothing to
    /// clean up.
    pub fn totp_setup(&self, account_name: &str) -> Result<TotpSetup> {
        let mut raw = [0u8; 20]; // 160-bit, same entropy budget as recovery codes
        getrandom::getrandom(&mut raw).map_err(|e| anyhow!("getrandom failed: {e}"))?;
        let secret_b32 = Secret::Raw(raw.to_vec()).to_encoded().to_string();
        let totp = TOTP::new(
            Algorithm::SHA1,
            6,
            1,
            30,
            raw.to_vec(),
            Some("Stowcloud".to_string()),
            account_name.to_string(),
        )
        .map_err(|e| anyhow!("bad totp params: {e}"))?;
        Ok(TotpSetup { secret: secret_b32, otpauth_url: totp.get_url() })
    }

    /// Verifies `pw` (re-confirmation), `secret`+`code` (proof of possession
    /// of the authenticator), then activates TOTP for `u` and returns 10
    /// one-time recovery codes. Per §2.4, enabling TOTP deletes the
    /// account-derived NT hash (unless it's a dedicated SMB password).
    pub fn totp_enroll(&self, u: UserId, pw: &SecretString, secret: &str, code: &str) -> Result<Vec<String>> {
        let stored_hash = self.pw_hash_of(u)?;
        if !self.verify_password_sync_gated(&stored_hash, pw.expose_secret()) {
            bail!("bad password");
        }
        let secret_bytes = decode_secret(secret)?;
        let totp = build_totp(secret_bytes.clone())?;
        if !totp.check_current(code).map_err(|e| anyhow!("totp check failed: {e}"))? {
            bail!("invalid totp code");
        }

        let sealed = seal_totp_secret(&self.master_key, &secret_bytes, u)?;

        let (recovery_codes, recovery_hashes) = generate_recovery_codes()?;

        let mut conn = self.pool.get()?;
        let tx = conn.transaction()?;
        tx.execute(
            "UPDATE user SET totp_secret = ?1 WHERE id = ?2",
            rusqlite::params![sealed, u.get()],
        )?;
        tx.execute("DELETE FROM recovery_code WHERE user = ?1", rusqlite::params![u.get()])?;
        for hash in &recovery_hashes {
            tx.execute(
                "INSERT INTO recovery_code (user, code_hash, used_ns) VALUES (?1, ?2, NULL)",
                rusqlite::params![u.get(), hash],
            )?;
        }
        // §2.4: turning TOTP on deletes an AccountPassword-derived NT hash —
        // there's no point keeping a credential that can no longer be used.
        // A Dedicated SMB password (set separately) is left untouched.
        let source = tx
            .query_row(
                "SELECT source FROM user_smb_secret WHERE user = ?1",
                rusqlite::params![u.get()],
                |r| r.get::<_, i64>(0),
            )
            .ok();
        let dropped_nt = source.is_some() && source != Some(NT_SOURCE_DEDICATED);
        if dropped_nt {
            tx.execute("DELETE FROM user_smb_secret WHERE user = ?1", rusqlite::params![u.get()])?;
        }
        tx.commit()?;

        if dropped_nt {
            // Exactly the OIDC link's problem: the
            // row is gone and the published `smbpasswd` still lists the
            // account, so without this the account password keeps working
            // over SMB despite the second factor.
            self.republish_passdb();
        }
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "auth.totp_enrolled", None, None, true, None);
        Ok(recovery_codes)
    }

    /// Re-confirms `pw`, disables TOTP, and — in the same transaction —
    /// re-derives the AccountPassword NT hash (unless the user has a
    /// Dedicated SMB password or opted out), since the plaintext is
    /// available right here (§2.4 "re-derive immediately on TOTP disable").
    pub async fn totp_disable(&self, u: UserId, pw: &SecretString) -> Result<()> {
        let stored_hash = self.pw_hash_of(u)?;
        let ok = self.verify_password_async(&stored_hash, pw.clone()).await;
        if !ok {
            bail!("bad password");
        }

        let (smb_opt_out, existing_source, oidc_linked): (bool, Option<i64>, bool) = {
            let conn = self.pool.get()?;
            let smb_opt_out: bool = conn.query_row(
                "SELECT smb_opt_out FROM user WHERE id = ?1",
                rusqlite::params![u.get()],
                |r| r.get(0),
            )?;
            let source = conn
                .query_row(
                    "SELECT source FROM user_smb_secret WHERE user = ?1",
                    rusqlite::params![u.get()],
                    |r| r.get::<_, i64>(0),
                )
                .ok();
            (smb_opt_out, source, crate::oidc::linked_on(&conn, u)?)
        };

        let mut conn = self.pool.get()?;
        let tx = conn.transaction()?;
        tx.execute(
            "UPDATE user SET totp_secret = NULL WHERE id = ?1",
            rusqlite::params![u.get()],
        )?;
        tx.execute("DELETE FROM totp_used WHERE user = ?1", rusqlite::params![u.get()])?;
        tx.execute("DELETE FROM recovery_code WHERE user = ?1", rusqlite::params![u.get()])?;
        // The third condition is not in §2.4's lifecycle table because OIDC
        // did not exist when it was written. An account that is both TOTP
        // enrolled and OIDC linked would otherwise get a working SMB
        // credential back by turning TOTP off, which is a carve-out cancelling
        // a carve-out. The proposal names `users.rs` and `nt_ops.rs` as the
        // derivation sites to gate (§4.3.6) and misses this one.
        let re_derived = !smb_opt_out && existing_source != Some(NT_SOURCE_DEDICATED) && !oidc_linked;
        if re_derived {
            let nt = crate::nt_hash::nt_hash(pw.expose_secret());
            let ct = crate::nt_hash::seal_nt(&self.master_key, &nt, u, self.cfg.key_ver)?;
            tx.execute(
                "INSERT INTO user_smb_secret (user, nt_hash_ct, key_ver, source, updated_ns) \
                 VALUES (?1, ?2, ?3, ?4, ?5) \
                 ON CONFLICT(user) DO UPDATE SET nt_hash_ct=excluded.nt_hash_ct, \
                   key_ver=excluded.key_ver, source=excluded.source, updated_ns=excluded.updated_ns",
                rusqlite::params![u.get(), ct, self.cfg.key_ver, NT_SOURCE_ACCOUNT, now_ns()],
            )?;
        }
        tx.commit()?;

        if re_derived {
            // The mirror of the enroll path above: the hash is back in the
            // database, and until the file is rewritten SMB does not know it.
            self.republish_passdb();
        }
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "auth.totp_disabled", None, None, true, None);
        Ok(())
    }

    /// How many of `u`'s recovery codes are still unused (the "≤3 remain" nudge). Zero for an account with TOTP disabled
    /// — `totp_disable` deletes every row, and `create_user` never inserts
    /// any. Not a secret from the account's own owner (unlike the codes
    /// themselves, never returned again once shown at mint time), which is
    /// why this is a plain count rather than routed through the same
    /// re-confirmation `reissue_recovery_codes` requires.
    pub fn recovery_codes_remaining(&self, u: UserId) -> Result<u32> {
        let conn = self.pool.get()?;
        let n: i64 = conn.query_row(
            "SELECT COUNT(*) FROM recovery_code WHERE user = ?1 AND used_ns IS NULL",
            rusqlite::params![u.get()],
            |r| r.get(0),
        )?;
        Ok(n as u32)
    }

    /// Re-confirms `pw`, then atomically replaces every recovery code
    /// belonging to `u` with a fresh set of 10, minted by the same
    /// [`generate_recovery_codes`] `totp_enroll` uses — one generator, so the
    /// two mints are never hashed differently from each other.
    ///
    /// **Every old code stops working the instant this returns `Ok`**: the
    /// `DELETE` and the 10 `INSERT`s run in one transaction, so there is no
    /// window where both the old and new lists validate, and no way back to
    /// the old list once this succeeds — the caller must show the returned
    /// codes to the user now, because this is the only time they exist in
    /// plaintext.
    ///
    /// Requires TOTP to already be enabled. An account without it has no
    /// `login_challenge` a recovery code could ever be presented against
    /// (`verify_totp`, below, is only reachable once a password login has
    /// already answered `totp_required`), so minting codes here would produce
    /// ten credentials that can never be used for anything.
    ///
    /// No forced re-login, by the same reasoning `totp_enroll`/`totp_disable`
    /// document: a recovery code is an alternate route
    /// into an account a live session already has full access to, so the
    /// "someone briefly at an unlocked device" threat this re-confirmation
    /// defends against is identical whether they disable 2FA outright or
    /// just mint themselves a fresh set of recovery codes. The password
    /// re-confirmation is the defense; forcing a fresh login on top of it
    /// would add friction without closing any gap that re-confirmation
    /// leaves open, and the project's standing rule is that re-login is never
    /// forced — in-session re-authentication is the accepted mechanism.
    pub fn reissue_recovery_codes(
        &self,
        u: UserId,
        pw: &SecretString,
    ) -> std::result::Result<Vec<String>, crate::RecoveryReissueError> {
        use crate::RecoveryReissueError as Err_;

        let stored_hash = self.pw_hash_of(u).map_err(|e| Err_::Internal(e.to_string()))?;
        if !self.verify_password_sync_gated(&stored_hash, pw.expose_secret()) {
            return Err(Err_::BadPassword);
        }

        let mut conn = self.pool.get().map_err(|e| Err_::Internal(e.to_string()))?;
        let totp_enabled: bool = conn
            .query_row(
                "SELECT totp_secret IS NOT NULL FROM user WHERE id = ?1",
                rusqlite::params![u.get()],
                |r| r.get(0),
            )
            .map_err(|e| Err_::Internal(e.to_string()))?;
        if !totp_enabled {
            return Err(Err_::TotpNotEnabled);
        }

        let (recovery_codes, recovery_hashes) =
            generate_recovery_codes().map_err(|e| Err_::Internal(e.to_string()))?;

        let tx = conn.transaction().map_err(|e| Err_::Internal(e.to_string()))?;
        tx.execute("DELETE FROM recovery_code WHERE user = ?1", rusqlite::params![u.get()])
            .map_err(|e| Err_::Internal(e.to_string()))?;
        for hash in &recovery_hashes {
            tx.execute(
                "INSERT INTO recovery_code (user, code_hash, used_ns) VALUES (?1, ?2, NULL)",
                rusqlite::params![u.get(), hash],
            )
            .map_err(|e| Err_::Internal(e.to_string()))?;
        }
        tx.commit().map_err(|e| Err_::Internal(e.to_string()))?;

        self.audit(Some(u), "auth.recovery_codes_reissued", None, None, true, None);
        Ok(recovery_codes)
    }

    /// Completes a partial (post-password, pre-2FA) login. `challenge` is
    /// the server-side single-use token minted by `login()` when TOTP is
    /// required; `code` is either a 6-digit TOTP or a 10-char recovery code.
    pub async fn verify_totp(&self, challenge: &str, code: &str) -> Result<Option<UserId>> {
        let token_hash = Sha256::digest(challenge.as_bytes()).to_vec();
        let conn = self.pool.get()?;
        let row: Option<(u32, i64)> = conn
            .query_row(
                "SELECT user, expires_ns FROM login_challenge WHERE token_hash = ?1",
                rusqlite::params![token_hash],
                |r| Ok((r.get::<_, i64>(0)? as u32, r.get(1)?)),
            )
            .ok();
        let Some((uid, expires_ns)) = row else {
            return Ok(None);
        };
        if now_ns() > expires_ns {
            conn.execute(
                "DELETE FROM login_challenge WHERE token_hash = ?1",
                rusqlite::params![token_hash],
            )?;
            return Ok(None);
        }
        let user = UserId::new(uid);

        let secret_ct: Option<Vec<u8>> = conn.query_row(
            "SELECT totp_secret FROM user WHERE id = ?1",
            rusqlite::params![user.get()],
            |r| r.get(0),
        )?;

        if let Some(ct) = secret_ct {
            if let Ok(secret_bytes) = open_totp_secret(&self.master_key, &ct, user) {
                if let Ok(totp) = build_totp(secret_bytes) {
                    let now = std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_secs();
                    let time_step = (now / 30) as i64;
                    // ±1 step window (90s total), replay-checked per (user, time_step).
                    for step in [time_step - 1, time_step, time_step + 1] {
                        let step_secs = (step as u64) * 30;
                        let matches = totp
                            .check(code, step_secs)
                            .then_some(())
                            .is_some();
                        if matches {
                            let already_used: bool = conn
                                .query_row(
                                    "SELECT EXISTS(SELECT 1 FROM totp_used WHERE user = ?1 AND time_step = ?2)",
                                    rusqlite::params![user.get(), step],
                                    |r| r.get(0),
                                )
                                .unwrap_or(false);
                            if already_used {
                                return Ok(None); // reuse of a consumed code
                            }
                            conn.execute(
                                "INSERT OR IGNORE INTO totp_used (user, time_step) VALUES (?1, ?2)",
                                rusqlite::params![user.get(), step],
                            )?;
                            conn.execute(
                                "DELETE FROM login_challenge WHERE token_hash = ?1",
                                rusqlite::params![token_hash],
                            )?;
                            return Ok(Some(user));
                        }
                    }
                }
            }
        }

        // Fall back to recovery codes (single use).
        let hash = recovery_hash(code);
        let n = conn.execute(
            "UPDATE recovery_code SET used_ns = ?1 \
             WHERE user = ?2 AND code_hash = ?3 AND used_ns IS NULL",
            rusqlite::params![now_ns(), user.get(), hash],
        )?;
        if n > 0 {
            conn.execute(
                "DELETE FROM login_challenge WHERE token_hash = ?1",
                rusqlite::params![token_hash],
            )?;
            self.audit(Some(user), "auth.recovery_code_used", None, None, true, None);
            return Ok(Some(user));
        }

        Ok(None)
    }
}
