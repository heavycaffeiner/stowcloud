use crate::crockford;
use crate::db::now_ns;
use crate::{AppPwInfo, AuthService, AuthVia, Principal, Scope};
use anyhow::{anyhow, Result};
use sc_vfs::{ShareId, UserId};
use sha2::{Digest, Sha256};
use std::net::IpAddr;

const SCOPE_PERMS_UNRESTRICTED: i64 = 0xFFFF;

/// `(id, user, scope_perms, scope_shares, expires_ns)` — one row of the
/// `app_password` table, as read by `verify_app_password`.
type AppPasswordRow = (u32, u32, i64, Option<Vec<u8>>, Option<i64>);

fn hash_token(token: &str) -> [u8; 32] {
    let mut h = Sha256::new();
    h.update(token.as_bytes());
    h.finalize().into()
}

impl AuthService {
    pub fn issue_app_password(&self, u: UserId, name: &str, scope: Scope) -> Result<(u32, String)> {
        let mut raw = [0u8; 20]; // 160-bit entropy
        getrandom::getrandom(&mut raw).map_err(|e| anyhow!("getrandom failed: {e}"))?;
        let body = crockford::encode(&raw);
        let token = format!("stow_{}", crockford::group(&body, 5));
        let hash = hash_token(&token);

        let scope_perms = scope.perms_mask.map(|m| m as i64).unwrap_or(SCOPE_PERMS_UNRESTRICTED);
        let scope_shares = scope
            .shares
            .as_ref()
            .map(|ids| serde_json::to_vec(&ids.iter().map(|s| s.get()).collect::<Vec<_>>()))
            .transpose()?;

        let conn = self.pool.get()?;
        conn.execute(
            "INSERT INTO app_password (token_hash, user, name, scope_perms, scope_shares, created_ns) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            rusqlite::params![hash.to_vec(), u.get(), name, scope_perms, scope_shares, now_ns()],
        )?;
        let id = conn.last_insert_rowid() as u32;
        drop(conn);
        self.audit(Some(u), "apppw.created", Some(name), None, true, None);
        Ok((id, token))
    }

    pub fn revoke_app_password(&self, id: u32) -> Result<()> {
        let conn = self.pool.get()?;
        conn.execute(
            "DELETE FROM app_password WHERE id = ?1",
            rusqlite::params![id],
        )?;
        drop(conn);
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(None, "apppw.revoked", Some(&id.to_string()), None, true, None);
        Ok(())
    }

    /// Same delete, scoped to `u` in the `WHERE` clause. `revoke_app_password`
    /// above takes a bare `id` with no ownership check at all — fine for a
    /// future admin-initiated revoke, but the self-service
    /// `DELETE /api/auth/app-passwords/:id` route must not let one account
    /// delete another's token by guessing a small integer id. Returns
    /// `false` for "no matching row for this user" so the HTTP layer can
    /// answer `404` uniformly whether the id never existed or belongs to
    /// someone else.
    pub fn revoke_app_password_owned(&self, u: UserId, id: u32) -> Result<bool> {
        let conn = self.pool.get()?;
        let n = conn.execute(
            "DELETE FROM app_password WHERE id = ?1 AND user = ?2",
            rusqlite::params![id, u.get()],
        )?;
        drop(conn);
        if n > 0 {
            self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            self.audit(Some(u), "apppw.revoked", Some(&id.to_string()), None, true, None);
        }
        Ok(n > 0)
    }

    pub fn list_app_passwords(&self, u: UserId) -> Result<Vec<AppPwInfo>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare(
            "SELECT id, name, scope_perms, created_ns, last_used_ns, expires_ns \
             FROM app_password WHERE user = ?1 ORDER BY created_ns DESC",
        )?;
        let rows = stmt
            .query_map(rusqlite::params![u.get()], |r| {
                let scope_perms: i64 = r.get(2)?;
                Ok(AppPwInfo {
                    id: r.get::<_, i64>(0)? as u32,
                    name: r.get(1)?,
                    scope_perms: if scope_perms == SCOPE_PERMS_UNRESTRICTED {
                        None
                    } else {
                        Some(scope_perms as u16)
                    },
                    created_ns: r.get(3)?,
                    last_used_ns: r.get(4)?,
                    expires_ns: r.get(5)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// App-password Basic-auth verification: fast (sha256, no Argon2),
    /// cached per DESIGN-AUTH §5.3. Returns `None` if `pw` doesn't match any
    /// live app password for any user (caller decides what that means).
    pub(crate) fn verify_app_password(&self, pw: &str, ip: IpAddr) -> Result<Option<Principal>> {
        let hash = hash_token(pw);
        let gen = self.generation();
        if let Some(p) = self.token_cache.get(&hash, gen) {
            self.coalesce_app_password_last_used(&hash, ip)?;
            return Ok(Some(p));
        }
        let conn = self.pool.get()?;
        let row: Option<AppPasswordRow> = conn
            .query_row(
                "SELECT id, user, scope_perms, scope_shares, expires_ns FROM app_password WHERE token_hash = ?1",
                rusqlite::params![hash.to_vec()],
                |r| {
                    Ok((
                        r.get::<_, i64>(0)? as u32,
                        r.get::<_, i64>(1)? as u32,
                        r.get(2)?,
                        r.get(3)?,
                        r.get(4)?,
                    ))
                },
            )
            .ok();
        let Some((id, user, scope_perms, scope_shares, expires_ns)) = row else {
            return Ok(None);
        };
        if let Some(exp) = expires_ns {
            if now_ns() > exp {
                return Ok(None);
            }
        }
        let shares: Option<Vec<ShareId>> = match scope_shares {
            Some(bytes) => {
                let ids: Vec<u32> = serde_json::from_slice(&bytes)?;
                Some(ids.into_iter().map(ShareId::new).collect())
            }
            None => None,
        };
        let scope = Scope {
            perms_mask: if scope_perms == SCOPE_PERMS_UNRESTRICTED {
                None
            } else {
                Some(scope_perms as u16)
            },
            shares,
        };
        let principal = Principal {
            user: UserId::new(user),
            scope,
            via: AuthVia::AppPassword(id),
        };
        self.token_cache.put(hash, principal.clone(), gen);
        self.coalesce_app_password_last_used(&hash, ip)?;
        Ok(Some(principal))
    }

    /// Coalesces `last_used_ns`/`last_ip` writes to at most once per
    /// `cfg.last_used_coalesce` per token, so a busy sync client doesn't
    /// turn every request into a write.
    fn coalesce_app_password_last_used(&self, token_hash: &[u8; 32], ip: IpAddr) -> Result<()> {
        let now = std::time::Instant::now();
        let should_write = {
            let mut map = self.app_pw_last_write.lock();
            match map.get(token_hash) {
                Some(last) if now.duration_since(*last) < self.cfg.last_used_coalesce => false,
                _ => {
                    map.insert(*token_hash, now);
                    true
                }
            }
        };
        if should_write {
            let conn = self.pool.get()?;
            conn.execute(
                "UPDATE app_password SET last_used_ns = ?1, last_ip = ?2 WHERE token_hash = ?3",
                rusqlite::params![now_ns(), ip.to_string(), token_hash.to_vec()],
            )?;
        }
        Ok(())
    }
}
