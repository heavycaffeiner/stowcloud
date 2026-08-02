use crate::db::now_ns;
use crate::nt_hash::open_nt;
use crate::nt_ops::NT_SOURCE_ACCOUNT;
use crate::password::needs_rehash;
use crate::{AdminGuardError, AuthService, ChangePasswordError, CreateUserError, UserRow};
use anyhow::{anyhow, bail, Result};
use sc_vfs::UserId;
use secrecy::{ExposeSecret, SecretString};

impl AuthService {
    /// Always creates a plain (non-admin) account — `role` starts at its
    /// column default of 0 regardless of caller. The one place account #1
    /// needs to be an administrator, `sc-server::setup`, calls
    /// [`Self::set_admin`] explicitly right after; the admin user-management
    /// API creates every account after that through this same function with
    /// no special-casing.
    pub fn create_user(&self, name: &str, password: &SecretString) -> std::result::Result<UserId, CreateUserError> {
        if password.expose_secret().chars().count() < self.cfg.min_password_len {
            return Err(CreateUserError::TooShort { min: self.cfg.min_password_len });
        }
        let pw_hash = self.hash_password_gated(password).map_err(|e| CreateUserError::Internal(e.to_string()))?;
        let conn = self.pool.get().map_err(|e| CreateUserError::Internal(e.to_string()))?;
        let created = now_ns();
        let inserted = conn.execute(
            "INSERT INTO user (name, display, pw_hash, disabled, created_ns, smb_opt_out, smb_enabled) \
             VALUES (?1, NULL, ?2, 0, ?3, 0, 1)",
            rusqlite::params![name, pw_hash, created],
        );
        let id = match inserted {
            Ok(_) => UserId::new(conn.last_insert_rowid() as u32),
            Err(rusqlite::Error::SqliteFailure(e, _)) if e.code == rusqlite::ErrorCode::ConstraintViolation => {
                return Err(CreateUserError::DuplicateName);
            }
            Err(e) => return Err(CreateUserError::Internal(e.to_string())),
        };

        // §2.4: NT hash is derived unconditionally at account creation
        // (new users never have TOTP enabled yet, and smb_opt_out defaults
        // to false), so every account starts SMB-ready.
        //
        // No OIDC check here, unlike the other derivation sites: `id` was
        // handed out by the `INSERT` a few lines up, so nothing can already
        // be linked to it. It is precisely because this one is unconditional
        // that linking has to *delete* the hash rather than only stop future
        // derivations (§4.3.6).
        self.store_nt_from_plaintext(&conn, id, password.expose_secret(), NT_SOURCE_ACCOUNT)
            .map_err(|e| CreateUserError::Internal(e.to_string()))?;

        drop(conn);
        self.audit(None, "auth.user_created", Some(name), None, true, None);
        Ok(id)
    }

    pub fn set_password(&self, u: UserId, new: &SecretString) -> Result<()> {
        if new.expose_secret().chars().count() < self.cfg.min_password_len {
            bail!(
                "password too short: minimum {} characters",
                self.cfg.min_password_len
            );
        }
        let pw_hash = self.hash_password_gated(new)?;
        let conn = self.pool.get()?;
        let n = conn.execute(
            "UPDATE user SET pw_hash = ?1 WHERE id = ?2",
            rusqlite::params![pw_hash, u.get()],
        )?;
        if n == 0 {
            bail!("no such user");
        }
        let totp_enabled: bool = conn.query_row(
            "SELECT totp_secret IS NOT NULL FROM user WHERE id = ?1",
            rusqlite::params![u.get()],
            |r| r.get(0),
        )?;
        let smb_opt_out: bool = conn.query_row(
            "SELECT smb_opt_out FROM user WHERE id = ?1",
            rusqlite::params![u.get()],
            |r| r.get(0),
        )?;
        // §2.4 lifecycle table: password change always (re)derives the
        // AccountPassword-sourced NT hash, unconditionally overwriting even
        // a previously Dedicated one — unlike opportunistic backfill, this
        // is an explicit user action with the plaintext in hand.
        //
        // Unless an OIDC identity is linked. This is the derivation site the
        // OIDC carve-out most needs (proposal §4.3.6 step 3): without it, a
        // linked user changing their password hands themselves a working SMB
        // credential again, and neither they nor the admin would have any
        // reason to think they had.
        let oidc_linked = crate::oidc::linked_on(&conn, u)?;
        let re_derived = !totp_enabled && !smb_opt_out && !oidc_linked;
        if re_derived {
            self.store_nt_from_plaintext(&conn, u, new.expose_secret(), NT_SOURCE_ACCOUNT)?;
        }
        drop(conn);
        if re_derived {
            // The row now holds a hash the published `smbpasswd` does not,
            // and until the file is rewritten SMB still accepts the password
            // this call replaced. See [`crate::PassdbSink`]: after the
            // connection is closed, never inside the write.
            self.republish_passdb();
        }
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "auth.password_changed", None, None, true, None);
        Ok(())
    }

    /// `disabled = true` is refused with [`AdminGuardError::LastAdmin`] when
    /// `u` is the deployment's sole active administrator (see that variant's
    /// docs) — re-enabling never needs the guard, since it can only ever
    /// grow the active-admin count.
    pub fn disable_user(&self, u: UserId, disabled: bool) -> Result<(), AdminGuardError> {
        let conn = self.pool.get().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if disabled && is_last_active_admin(&conn, u).map_err(|e| AdminGuardError::Internal(e.to_string()))? {
            return Err(AdminGuardError::LastAdmin);
        }
        let n = conn
            .execute("UPDATE user SET disabled = ?1 WHERE id = ?2", rusqlite::params![disabled, u.get()])
            .map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if n == 0 {
            return Err(AdminGuardError::NoSuchUser);
        }
        drop(conn);
        // `export_smbpasswd` filters on `disabled = 0`, so this changes what
        // the file should contain — and until it is rewritten, a disabled
        // account keeps authenticating over SMB with the credential the
        // published file still carries. Verified on the testbed: the web
        // login went 401 and SMB kept working.
        self.republish_passdb();
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(
            Some(u),
            if disabled { "admin.user_disabled" } else { "admin.user_enabled" },
            None,
            None,
            true,
            None,
        );
        Ok(())
    }

    /// Grants or revokes the administrator role (`user.role`). Revoking is
    /// refused with [`AdminGuardError::LastAdmin`] under the same condition
    /// `disable_user` guards against — see that variant's docs.
    pub fn set_admin(&self, u: UserId, admin: bool) -> Result<(), AdminGuardError> {
        let conn = self.pool.get().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if !admin && is_last_active_admin(&conn, u).map_err(|e| AdminGuardError::Internal(e.to_string()))? {
            return Err(AdminGuardError::LastAdmin);
        }
        let n = conn
            .execute(
                "UPDATE user SET role = ?1 WHERE id = ?2",
                rusqlite::params![if admin { 1i64 } else { 0i64 }, u.get()],
            )
            .map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if n == 0 {
            return Err(AdminGuardError::NoSuchUser);
        }
        drop(conn);
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(
            Some(u),
            if admin { "admin.role_granted" } else { "admin.role_revoked" },
            None,
            None,
            true,
            None,
        );
        Ok(())
    }

    /// Permanently removes an account and everything keyed to it — sessions,
    /// app passwords, recovery codes, TOTP replay records, group membership,
    /// the SMB NT-hash secret, any outstanding login challenge, and any
    /// linked OIDC identity. Only the last of those declares
    /// `FOREIGN KEY ... ON DELETE CASCADE` (`db.rs`), and it is swept
    /// explicitly anyway: the cascade only fires while `PRAGMA foreign_keys`
    /// is on, and an `oidc_identity` row outliving its account is not an
    /// orphan but a live credential pointed at a recyclable id. Done inside
    /// one transaction so a
    /// crash mid-delete cannot leave orphaned rows a future account could
    /// collide with (SQLite recycles `INTEGER PRIMARY KEY` ids). Refused with
    /// [`AdminGuardError::LastAdmin`] under the same condition `disable_user`
    /// guards against — deleting is strictly more destructive than disabling,
    /// so it inherits the same floor.
    ///
    /// Destructive and irreversible — the HTTP layer is expected to require
    /// explicit confirmation before calling this (style
    /// "type the name to confirm" pattern), not to guard it itself.
    pub fn delete_user(&self, u: UserId) -> Result<(), AdminGuardError> {
        let mut conn = self.pool.get().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if is_last_active_admin(&conn, u).map_err(|e| AdminGuardError::Internal(e.to_string()))? {
            return Err(AdminGuardError::LastAdmin);
        }
        let exists: bool = conn
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM user WHERE id = ?1)",
                rusqlite::params![u.get()],
                |r| r.get(0),
            )
            .map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if !exists {
            return Err(AdminGuardError::NoSuchUser);
        }
        let tx = conn.transaction().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        (|| -> rusqlite::Result<()> {
            tx.execute("DELETE FROM session WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM app_password WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM recovery_code WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM totp_used WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM membership WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM user_smb_secret WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM login_challenge WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM oidc_identity WHERE user = ?1", rusqlite::params![u.get()])?;
            tx.execute("DELETE FROM user WHERE id = ?1", rusqlite::params![u.get()])?;
            Ok(())
        })()
        .map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        tx.commit().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        drop(conn);
        // The row and its NT hash are gone; the published file still has
        // both until this lands. Same reasoning as `disable_user`.
        self.republish_passdb();
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "admin.user_deleted", None, None, true, None);
        Ok(())
    }

    pub fn find_user(&self, name: &str) -> Result<Option<UserRow>> {
        let conn = self.pool.get()?;
        let row = conn
            .query_row(
                "SELECT id, name, display, disabled, totp_secret IS NOT NULL, smb_opt_out, smb_enabled, created_ns, role, quota_bytes, usage_bytes \
                 FROM user WHERE name = ?1 COLLATE NOCASE",
                rusqlite::params![name],
                |r| {
                    let id = UserId::new(r.get::<_, i64>(0)? as u32);
                    Ok(UserRow {
                        id,
                        name: r.get(1)?,
                        display: r.get(2)?,
                        disabled: r.get(3)?,
                        totp_enabled: r.get(4)?,
                        smb_opt_out: r.get(5)?,
                        smb_enabled: r.get(6)?,
                        created_ns: r.get(7)?,
                        is_admin: r.get::<_, i64>(8)? != 0,
                        quota_bytes: r.get::<_, Option<i64>>(9)?.map(|v| v as u64),
                        usage_bytes: r.get::<_, i64>(10)? as u64,
                    })
                },
            )
            .ok();
        Ok(row)
    }

    /// Every account, ordered by id.
    ///
    /// Needed by the parts of the server that have to enumerate principals
    /// rather than look one up: the Samba `passdb`/`smb.conf` sync
    /// (`DEPLOYMENT.md` §7.2/§7.3 renders one entry per user) and the
    /// startup grant projection, which turns configured shares into
    /// per-principal `sc-acl` grants.
    pub fn list_users(&self) -> Result<Vec<UserRow>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare(
            "SELECT id, name, display, disabled, totp_secret IS NOT NULL, smb_opt_out, smb_enabled, created_ns, role, quota_bytes, usage_bytes \
             FROM user ORDER BY id",
        )?;
        let rows = stmt.query_map([], |r| {
            let id = UserId::new(r.get::<_, i64>(0)? as u32);
            Ok(UserRow {
                id,
                name: r.get(1)?,
                display: r.get(2)?,
                disabled: r.get(3)?,
                totp_enabled: r.get(4)?,
                smb_opt_out: r.get(5)?,
                smb_enabled: r.get(6)?,
                created_ns: r.get(7)?,
                is_admin: r.get::<_, i64>(8)? != 0,
                quota_bytes: r.get::<_, Option<i64>>(9)?.map(|v| v as u64),
                usage_bytes: r.get::<_, i64>(10)? as u64,
            })
        })?;
        let mut out = Vec::new();
        for row in rows {
            out.push(row?);
        }
        Ok(out)
    }

    /// Look one account up by id. Used by the HTTP/compat layers, which get a
    /// `UserId` out of the credential and need the login/display name back.
    pub fn find_user_by_id(&self, u: UserId) -> Result<Option<UserRow>> {
        let conn = self.pool.get()?;
        let row = conn
            .query_row(
                "SELECT id, name, display, disabled, totp_secret IS NOT NULL, smb_opt_out, smb_enabled, created_ns, role, quota_bytes, usage_bytes \
                 FROM user WHERE id = ?1",
                rusqlite::params![u.get()],
                |r| {
                    let id = UserId::new(r.get::<_, i64>(0)? as u32);
                    Ok(UserRow {
                        id,
                        name: r.get(1)?,
                        display: r.get(2)?,
                        disabled: r.get(3)?,
                        totp_enabled: r.get(4)?,
                        smb_opt_out: r.get(5)?,
                        smb_enabled: r.get(6)?,
                        created_ns: r.get(7)?,
                        is_admin: r.get::<_, i64>(8)? != 0,
                        quota_bytes: r.get::<_, Option<i64>>(9)?.map(|v| v as u64),
                        usage_bytes: r.get::<_, i64>(10)? as u64,
                    })
                },
            )
            .ok();
        Ok(row)
    }

    /// Sets or clears (`None` = unlimited) the per-user quota cap reported
    /// through `/cloud/user`. Purely a stored
    /// value — no enforcement lives in this crate. Returns
    /// [`AdminGuardError`] (never its `LastAdmin` variant) rather than a bare
    /// `anyhow::Error` so the HTTP layer can tell "no such user" apart from a
    /// storage failure, same as every other admin-user mutation here.
    pub fn set_quota(&self, u: UserId, quota_bytes: Option<u64>) -> std::result::Result<(), AdminGuardError> {
        let conn = self.pool.get().map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        let n = conn
            .execute(
                "UPDATE user SET quota_bytes = ?1 WHERE id = ?2",
                rusqlite::params![quota_bytes.map(|v| v as i64), u.get()],
            )
            .map_err(|e| AdminGuardError::Internal(e.to_string()))?;
        if n == 0 {
            return Err(AdminGuardError::NoSuchUser);
        }
        drop(conn);
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "admin.quota_changed", None, None, true, None);
        Ok(())
    }

    /// Reads the ledger + cap for one account — the seam
    /// `sc_core::QuotaSink::check` calls into (`FEATURES.md` #49). `Ok(None)`
    /// for an unknown user; enforcement treats that the same as "allow",
    /// same as every other quota-less path (`quota.rs`'s optional-attach).
    pub fn quota_status(&self, u: UserId) -> Result<Option<crate::QuotaStatus>> {
        let conn = self.pool.get()?;
        let row: Option<(i64, Option<i64>)> = conn
            .query_row(
                "SELECT usage_bytes, quota_bytes FROM user WHERE id = ?1",
                rusqlite::params![u.get()],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok();
        Ok(row.map(|(used, limit)| crate::QuotaStatus {
            used: used as u64,
            limit: limit.map(|v| v as u64),
        }))
    }

    /// Adjusts `u`'s usage ledger by `delta` bytes (negative on a freed
    /// delete/purge). Clamped at 0 in SQL so a ledger that's already drifted
    /// slightly negative (e.g. a race between two concurrent charges) can
    /// never go further below zero — `MAX(0, ...)` rather than trusting the
    /// caller's bookkeeping. Best-effort: a failure here must not roll back
    /// the filesystem change that already happened, so it only logs, mirror
    /// of `audit`'s own never-propagate rule.
    pub fn add_usage(&self, u: UserId, delta: i64) {
        let res = (|| -> Result<()> {
            let conn = self.pool.get()?;
            conn.execute(
                "UPDATE user SET usage_bytes = MAX(0, usage_bytes + ?1) WHERE id = ?2",
                rusqlite::params![delta, u.get()],
            )?;
            Ok(())
        })();
        if let Err(e) = res {
            tracing::warn!(error = %e, user = u.get(), delta, "quota usage update failed");
        }
    }

    pub(crate) fn pw_hash_of(&self, u: UserId) -> Result<String> {
        let conn = self.pool.get()?;
        conn.query_row(
            "SELECT pw_hash FROM user WHERE id = ?1",
            rusqlite::params![u.get()],
            |r| r.get(0),
        )
        .map_err(|_| anyhow!("no such user"))
    }

    pub(crate) fn maybe_rehash(&self, u: UserId, pw: &str, current_hash: &str) -> Result<()> {
        if needs_rehash(current_hash, &self.cfg) {
            let new_hash = self.hash_password_gated(&SecretString::from(pw.to_string()))?;
            let conn = self.pool.get()?;
            conn.execute(
                "UPDATE user SET pw_hash = ?1 WHERE id = ?2",
                rusqlite::params![new_hash, u.get()],
            )?;
        }
        Ok(())
    }

    pub fn nt_hash_present(&self, u: UserId) -> Result<bool> {
        let conn = self.pool.get()?;
        let present: bool = conn.query_row(
            "SELECT EXISTS(SELECT 1 FROM user_smb_secret WHERE user = ?1)",
            rusqlite::params![u.get()],
            |r| r.get(0),
        )?;
        Ok(present)
    }

    /// Renders an smbpasswd(5)-format file for every user with `smb_enabled`
    /// and a stored (decryptable) NT hash. LANMAN hash is always disabled
    /// (32 'X's) — only NTLMv2 via the NT hash is supported.
    ///
    /// Field 2 of every line is `base_uid + <account row id>`, which must be
    /// the uid the account's passwd entry beside this file carries
    /// (`sc_smb::render_passwd_entries`, fed from `smb.service_uid`) —
    /// `DEPLOYMENT.md` §7.2 passdb sync point 3.
    ///
    /// Two properties, both learned the hard way, and both load-bearing:
    ///
    /// * **It must match the passwd entry.** `pdbedit -i` resolves a line to a
    ///   Unix account through this uid, and imports **nothing at all** when it
    ///   names none: no error, no log line, exit 0, an empty passdb, and every
    ///   login answered `NT_STATUS_NO_SUCH_USER`. This wrote a bare row id
    ///   against passwd entries on the service uid, so nobody could
    ///   authenticate.
    /// * **It must be unique per account.** `pdbedit -i` matches by uid, not
    ///   by name, so several names on one uid all import as whichever name
    ///   `getpwuid` answers with — observed as `Importing account for
    ///   admin...ok` twice for a two-line file, leaving one account in the
    ///   passdb. Aligning both files on a single service uid fixed the first
    ///   property and not this one.
    ///
    /// Uniqueness costs nothing elsewhere: `force user = scsvc` decides what a
    /// connection writes as, so a distinct uid here never reaches a file on
    /// disk (verified — a second account's uploads still land `scsvc:scsvc`).
    pub fn export_smbpasswd(&self, base_uid: u32) -> Result<String> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare(
            "SELECT u.id, u.name, s.nt_hash_ct, s.key_ver \
             FROM user u JOIN user_smb_secret s ON s.user = u.id \
             WHERE u.smb_enabled = 1 AND u.disabled = 0",
        )?;
        let rows = stmt.query_map([], |r| {
            Ok((
                r.get::<_, i64>(0)? as u32,
                r.get::<_, String>(1)?,
                r.get::<_, Vec<u8>>(2)?,
                r.get::<_, i64>(3)? as u32,
            ))
        })?;
        let mut out = String::new();
        for row in rows {
            // `row_id` is this account's primary key: the AAD `open_nt` needs
            // (that is what the hash was sealed against) and, offset by
            // `base_uid`, what makes the rendered uid unique.
            let (row_id, name, ct, key_ver) = row?;
            let nt = match open_nt(&self.master_key, &ct, UserId::new(row_id), key_ver) {
                Ok(nt) => nt,
                Err(e) => {
                    tracing::warn!(user = row_id, error = %e, "skipping undecryptable NT hash in smbpasswd export");
                    continue;
                }
            };
            let nt_hex: String = nt.iter().map(|b| format!("{b:02X}")).collect();
            let lct = format!("{:08X}", now_ns() / 1_000_000_000);
            out.push_str(&format!(
                "{name}:{uid}:{lm}:{nt}:[U          ]:LCT-{lct}:\n",
                name = name,
                uid = base_uid.saturating_add(row_id),
                lm = "X".repeat(32),
                nt = nt_hex,
                lct = lct,
            ));
        }
        Ok(out)
    }

    /// Self-service password change (`DESIGN-AUTH.md` §2.3): re-confirms
    /// `current` before touching anything, so the HTTP layer can hand back a
    /// precise `401` for a wrong current password versus a `422` for a new
    /// one under the minimum length — distinct outcomes `set_password` alone
    /// doesn't distinguish (it only ever sees the new password). On success,
    /// delegates to `set_password`, which unconditionally re-derives the NT
    /// hash (§2.4) and bumps the credential-cache generation.
    pub async fn change_password(
        &self,
        u: UserId,
        current: &SecretString,
        new: &SecretString,
    ) -> std::result::Result<(), ChangePasswordError> {
        if new.expose_secret().chars().count() < self.cfg.min_password_len {
            return Err(ChangePasswordError::TooShort { min: self.cfg.min_password_len });
        }
        let stored_hash = self.pw_hash_of(u).map_err(|_| ChangePasswordError::BadCurrentPassword)?;
        if !self.verify_password_async(&stored_hash, current.clone()).await {
            return Err(ChangePasswordError::BadCurrentPassword);
        }
        // The length check above already guarantees `set_password` won't
        // reject `new` on its own account; a failure past this point is a
        // storage-layer problem, not a credential one, but there is no
        // "internal" variant to report it under here — the HTTP layer maps
        // any `anyhow` error from `set_password` itself (which it never
        // reaches from this path) separately.
        self.set_password(u, new).map_err(|_| ChangePasswordError::BadCurrentPassword)?;
        Ok(())
    }

    /// Updates the two self-service SMB toggles (`DESIGN-AUTH.md` §2.4):
    /// `smb_opt_out` (refuse to hold *any* NT hash for this account, derived
    /// or dedicated) and `smb_enabled` (this account's half of "published" —
    /// the other half being the deployment-wide `smb.enabled`). Turning
    /// `opt_out` on deletes whatever NT hash is currently stored, regardless
    /// of its source: the user is withdrawing consent for the derivation
    /// existing at all, not just for it being published.
    pub fn set_smb_settings(&self, u: UserId, opt_out: bool, enabled: bool) -> Result<()> {
        let conn = self.pool.get()?;
        // Read first, so the republish below can tell an actual change from a
        // screen that saved the values it already had. `smb_enabled` counts
        // as much as `opt_out` here: `export_smbpasswd` filters on it, so
        // turning it off has to reach the published file too.
        let before: Option<(bool, bool)> = conn
            .query_row(
                "SELECT smb_opt_out, smb_enabled FROM user WHERE id = ?1",
                rusqlite::params![u.get()],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok();
        let n = conn.execute(
            "UPDATE user SET smb_opt_out = ?1, smb_enabled = ?2 WHERE id = ?3",
            rusqlite::params![opt_out, enabled, u.get()],
        )?;
        if n == 0 {
            bail!("no such user");
        }
        if opt_out {
            self.delete_nt(&conn, u)?;
        }
        drop(conn);
        if before != Some((opt_out, enabled)) {
            // Withdrawing consent has to reach the file smbd reads, or the
            // account stays reachable over SMB with the credential the user
            // just asked this server to stop holding.
            self.republish_passdb();
        }
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(Some(u), "auth.smb_settings_changed", None, None, true, None);
        Ok(())
    }
}

/// `true` iff `u` is currently an *active* administrator (`role = admin`,
/// `disabled = 0`) and no other account satisfies both conditions. A user who
/// is already disabled, or was never an admin, is never "the last admin" by
/// this definition — only an operation that would remove the one remaining
/// active administrator is refused, not one that touches an already-inert
/// account.
fn is_last_active_admin(conn: &rusqlite::Connection, u: UserId) -> Result<bool> {
    let row: Option<(i64, i64)> = conn
        .query_row(
            "SELECT role, disabled FROM user WHERE id = ?1",
            rusqlite::params![u.get()],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .ok();
    let Some((role, disabled)) = row else {
        return Ok(false); // no such user — the caller's own existence check reports that
    };
    if role == 0 || disabled != 0 {
        return Ok(false);
    }
    let others: i64 = conn.query_row(
        "SELECT COUNT(*) FROM user WHERE role != 0 AND disabled = 0 AND id != ?1",
        rusqlite::params![u.get()],
        |r| r.get(0),
    )?;
    Ok(others == 0)
}
