use crate::db::now_ns;
use crate::{AuthService, AuthVia, Principal, Scope, SessionInfo, SessionToken};
use anyhow::{anyhow, Result};
use sc_vfs::UserId;
use sha2::{Digest, Sha256};
use std::net::IpAddr;

fn random_token() -> Result<String> {
    let mut buf = [0u8; 32]; // 256-bit CSPRNG
    getrandom::getrandom(&mut buf).map_err(|e| anyhow!("getrandom failed: {e}"))?;
    Ok(data_encoding::BASE64URL_NOPAD.encode(&buf)) // 43 chars, matches __Host- cookie format
}

pub(crate) fn hash_token(token: &str) -> [u8; 32] {
    let mut h = Sha256::new();
    h.update(token.as_bytes());
    h.finalize().into()
}

/// Hex-encodes `sha256(token)` — the same identifier
/// [`crate::SessionInfo::id_hash_hex`] exposes for every *other* session in
/// [`AuthService::list_sessions`]. A caller holding the live request's own
/// plaintext token (the HTTP layer, from the session cookie) uses this to
/// compute which list entry is "the session you're on right now" without
/// reaching into this module's private `hash_token`.
pub fn token_hash_hex(token: &str) -> String {
    hash_token(token).iter().map(|b| format!("{b:02x}")).collect()
}

/// Decodes a lowercase-hex `sha256` digest as produced by
/// [`token_hash_hex`]/`SessionInfo::id_hash_hex`. `None` on anything
/// malformed — callers treat that identically to "no such session" rather
/// than surfacing a parse error.
fn decode_hash_hex(s: &str) -> Option<Vec<u8>> {
    if s.len() != 64 || !s.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).ok())
        .collect()
}

/// `session.amr` bits: which authentication method issued this session.
/// describes the column as a `pw | totp | recovery`
/// bitmask, and until these constants existed the code wrote the literal `1`
/// for every session regardless of method.
///
/// Account password.
pub const AMR_PASSWORD: u32 = 1;
/// TOTP second factor. **Nothing sets this today** -- see
/// [`AuthService::create_session`].
pub const AMR_TOTP: u32 = 2;
/// One-time recovery code used in place of a TOTP code. Not set today, for
/// the same reason `AMR_TOTP` is not.
pub const AMR_RECOVERY: u32 = 4;
/// Session issued by the OIDC callback. This is the bit that makes "unlinking
/// an identity cuts off the access it granted" implementable at all: unlink
/// selects sessions by it (proposal §4.3.6), which is why it is the one bit
/// this change actually starts writing.
pub const AMR_OIDC: u32 = 8;

impl AuthService {
    /// Creates a session row and returns the plaintext token (shown once).
    ///
    /// `amr` records **how** the caller authenticated, as the bitmask
    /// describes. It used to be the literal `1` for
    /// every session regardless of method; making it a parameter is what lets
    /// an OIDC-issued session be told apart from a password one, which
    /// unlinking depends on (proposal §4.3.6).
    ///
    /// Every pre-existing call site passes [`AMR_PASSWORD`], preserving
    /// today's behaviour exactly -- **including the fact that a TOTP-gated
    /// login still does not set [`AMR_TOTP`]**. That is a separate
    /// pre-existing bug, and this change deliberately does not fix it:
    /// correcting it changes what the connected-sessions screen shows for
    /// accounts that have nothing to do with OIDC, which does not belong in a
    /// commit whose subject is adding a provider.
    pub fn create_session(&self, u: UserId, ip: IpAddr, ua: &str, amr: u32) -> Result<SessionToken> {
        let token = random_token()?;
        let hash = hash_token(&token);
        let now = now_ns();
        let absolute_expiry = now + self.cfg.session_absolute.as_nanos() as i64;
        let conn = self.pool.get()?;
        conn.execute(
            "INSERT INTO session (id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr) \
             VALUES (?1, ?2, ?3, ?3, ?4, ?5, ?6, ?7)",
            rusqlite::params![
                hash.to_vec(),
                u.get(),
                now,
                absolute_expiry,
                ip.to_string(),
                ua,
                amr as i64,
            ],
        )?;
        Ok(SessionToken(token))
    }

    pub fn validate_session(&self, token: &str) -> Result<Option<Principal>> {
        let hash = hash_token(token);
        let conn = self.pool.get()?;
        let row: Option<(u32, i64, i64, bool)> = conn
            .query_row(
                "SELECT s.user, s.last_seen_ns, s.absolute_expiry_ns, u.disabled \
                 FROM session s JOIN user u ON u.id = s.user \
                 WHERE s.id_hash = ?1",
                rusqlite::params![hash.to_vec()],
                |r| Ok((r.get::<_, i64>(0)? as u32, r.get(1)?, r.get(2)?, r.get(3)?)),
            )
            .ok();
        let Some((uid, last_seen_ns, absolute_expiry_ns, disabled)) = row else {
            return Ok(None);
        };
        let now = now_ns();
        let idle_ns = self.cfg.session_idle.as_nanos() as i64;
        if disabled || now > absolute_expiry_ns || now - last_seen_ns > idle_ns {
            // Idle/absolute expiry or disabled account: clean up and reject.
            conn.execute(
                "DELETE FROM session WHERE id_hash = ?1",
                rusqlite::params![hash.to_vec()],
            )?;
            return Ok(None);
        }
        conn.execute(
            "UPDATE session SET last_seen_ns = ?1 WHERE id_hash = ?2",
            rusqlite::params![now, hash.to_vec()],
        )?;
        Ok(Some(Principal {
            user: UserId::new(uid),
            scope: Scope::default(),
            via: AuthVia::Session,
        }))
    }

    pub fn revoke_session(&self, token: &str) -> Result<()> {
        let hash = hash_token(token);
        let conn = self.pool.get()?;
        conn.execute(
            "DELETE FROM session WHERE id_hash = ?1",
            rusqlite::params![hash.to_vec()],
        )?;
        drop(conn);
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        Ok(())
    }

    /// Revokes exactly one of `u`'s own sessions, identified by the hex
    /// `id_hash_hex` a client got from [`Self::list_sessions`]. Scoped to `u`
    /// in the `DELETE`'s `WHERE` clause — not looked up and checked
    /// separately — so there is no window where a caller could learn
    /// whether a hash belongs to *someone else's* live session. Returns
    /// `false` for "no matching row for this user" (already gone, wrong
    /// hash, or somebody else's session), which the HTTP layer maps to a
    /// plain `404` either way.
    pub fn revoke_session_by_hash(&self, u: UserId, id_hash_hex: &str) -> Result<bool> {
        let Some(bytes) = decode_hash_hex(id_hash_hex) else {
            return Ok(false);
        };
        let conn = self.pool.get()?;
        let n = conn.execute(
            "DELETE FROM session WHERE id_hash = ?1 AND user = ?2",
            rusqlite::params![bytes, u.get()],
        )?;
        drop(conn);
        if n > 0 {
            self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        }
        Ok(n > 0)
    }

    pub fn list_sessions(&self, u: UserId) -> Result<Vec<SessionInfo>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare(
            "SELECT id_hash, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr \
             FROM session WHERE user = ?1 ORDER BY last_seen_ns DESC",
        )?;
        let rows = stmt
            .query_map(rusqlite::params![u.get()], |r| {
                let id_hash: Vec<u8> = r.get(0)?;
                Ok(SessionInfo {
                    id_hash_hex: id_hash.iter().map(|b| format!("{b:02x}")).collect(),
                    created_ns: r.get(1)?,
                    last_seen_ns: r.get(2)?,
                    absolute_expiry_ns: r.get(3)?,
                    ip_first: r.get(4)?,
                    ua_first: r.get(5)?,
                    amr: r.get::<_, i64>(6)? as u32,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }
}
