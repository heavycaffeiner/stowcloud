use crate::db::now_ns;
use crate::AuthService;
use crate::LoginResult;
use anyhow::Result;
use secrecy::{ExposeSecret, SecretString};
use sha2::{Digest, Sha256};
use std::net::IpAddr;

impl AuthService {
    /// Web/WebDAV login (§7.2). Account-enumeration
    /// resistant: an unknown username still runs a real Argon2 verify
    /// against `DUMMY_HASH` so timing doesn't reveal existence, and the
    /// per-account soft delay is applied regardless of outcome.
    pub async fn login(&self, name: &str, pw: &SecretString, ip: IpAddr) -> LoginResult {
        if let Some(retry_after_s) = self.ip_gate.check(ip) {
            return LoginResult::RateLimited { retry_after_s };
        }

        let user_row = self.find_user(name).ok().flatten();
        let hash = user_row
            .as_ref()
            .map(|u| self.pw_hash_of(u.id).unwrap_or_else(|_| self.dummy_hash.clone()))
            .unwrap_or_else(|| self.dummy_hash.clone());

        let verified = self.verify_password_async(&hash, pw.clone()).await;
        let ok = verified && user_row.as_ref().is_some_and(|u| !u.disabled);

        let account_key = name.to_lowercase();
        let delay = self.account_gate.record_failure(&account_key);
        if !delay.is_zero() {
            tokio::time::sleep(delay).await;
        }

        if !ok {
            self.audit(None, "auth.login_failed", Some(name), Some(ip), false, None);
            return LoginResult::Invalid;
        }
        self.account_gate.reset(&account_key);

        let user = user_row.expect("ok implies user_row is Some");

        // `oidc.local_password_login = "deny"` (proposal §4.3.5): a linked
        // account uses the IdP for the web UI, so that the second factor the
        // IdP enforces cannot be sidestepped by knowing the local password.
        //
        // Applied *after* the password verified, for the same reason the DAV
        // carve-out is (`basic.rs`): refusing earlier would let a wrong
        // password against a linked account return sooner than a wrong
        // password against any other, which is an enumeration oracle
        // The refusal is `Invalid`, the same answer
        // and the same code a wrong password gets, so the wire tells nobody
        // which accounts are linked; the audit `detail` is where an operator
        // reads what actually happened.
        //
        // `unwrap_or(true)` fails closed, matching `verify_basic`: under an
        // explicit `deny` policy, "we could not read whether this account is
        // linked" must not resolve to letting the password through.
        if matches!(self.cfg.oidc_local_password_login, crate::config::OidcLocalPasswordLogin::Deny)
            && self.oidc_linked(user.id).unwrap_or(true)
        {
            self.audit(Some(user.id), "auth.login_failed", Some(name), Some(ip), false, Some("oidc_local_password_denied"));
            return LoginResult::Invalid;
        }

        // Rehash on changed params, then opportunistic NT backfill — both
        // require the plaintext, which we still hold here.
        let _ = self.maybe_rehash(user.id, pw.expose_secret(), &hash);
        if let Ok(conn) = self.pool.get() {
            let _ = self.maybe_backfill_nt(&conn, user.id, pw.expose_secret());
        }

        if user.totp_enabled {
            match self.issue_login_challenge(user.id) {
                Ok(challenge) => {
                    self.audit(Some(user.id), "auth.login", Some(name), Some(ip), true, Some("totp_required"));
                    return LoginResult::TotpRequired { challenge };
                }
                Err(_) => return LoginResult::Invalid,
            }
        }

        self.audit(Some(user.id), "auth.login", Some(name), Some(ip), true, None);
        LoginResult::Ok(user.id)
    }

    fn issue_login_challenge(&self, user: sc_vfs::UserId) -> Result<String> {
        let mut buf = [0u8; 32];
        getrandom::getrandom(&mut buf).map_err(|e| anyhow::anyhow!("getrandom failed: {e}"))?;
        let token = data_encoding::BASE64URL_NOPAD.encode(&buf);
        let hash = Sha256::digest(token.as_bytes()).to_vec();
        let expires = now_ns() + 15 * 60 * 1_000_000_000i64;
        let conn = self.pool.get()?;
        conn.execute(
            "INSERT INTO login_challenge (token_hash, user, expires_ns, amr) VALUES (?1, ?2, ?3, 1)",
            rusqlite::params![hash, user.get(), expires],
        )?;
        Ok(token)
    }
}
