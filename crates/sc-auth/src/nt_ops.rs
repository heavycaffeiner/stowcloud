//! Shared NT-hash derive/store/backfill helpers used by user creation,
//! password change, TOTP enable/disable, and opportunistic backfill.

use crate::db::now_ns;
use crate::nt_hash::{nt_hash, seal_nt};
use crate::{
    AuthService, SmbCredential, SmbPasswordError, SmbPasswordSet, SmbTotpPolicy, SmbUnavailable,
};
use anyhow::Result;
use rusqlite::Connection;
use sc_vfs::UserId;
use secrecy::{ExposeSecret, SecretString};

pub(crate) const NT_SOURCE_ACCOUNT: i64 = 0;
pub(crate) const NT_SOURCE_DEDICATED: i64 = 1;

/// The seam through which this crate can ask for `smbpasswd` to be written
/// again.
///
/// Deleting a `user_smb_secret` row closes SMB in the database and nowhere
/// else: `smbd` authenticates against the file this server last published, so
/// until that file is rewritten the account keeps working over SMB with the
/// credential that was just revoked. Everything needed to rewrite it --
/// share projection, the bind check, the config directory -- lives in
/// `sc-server`, several layers above this crate, so this is a callback rather
/// than a call.
///
/// Installed once at startup via [`AuthService::set_passdb_sink`]. The
/// serving process installs `sc-server`'s `PassdbPublisher`, and only when
/// SMB is enabled; the CLI paths (`gc`, `smb-sync`, `masterkey rotate`) build
/// their own short-lived `AuthService`, install nothing, and render
/// explicitly instead. Where there is no sink,
/// [`AuthService::republish_passdb`] says so out loud rather than pretending
/// the file was rewritten.
///
/// **What republishes.** Every path that changes what `export_smbpasswd`
/// would produce for a *running* server: the OIDC link and unlink (§4.3.6),
/// `set_password`, `set_smb_settings`, `totp_enroll` and `totp_disable`. Each
/// of them either revokes a credential the published file still honours, or
/// restores one the file does not yet carry.
///
/// Two derivation sites deliberately stay silent. `create_user` and
/// `maybe_backfill_nt` only ever *add* a hash, so a stale file there refuses
/// an access that should have been allowed rather than allowing one that
/// should have been refused, and `maybe_backfill_nt` sits on the login path
/// where a file rewrite has no business being. Both converge at the next
/// republish or the next `smb-sync`.
///
/// Implementations must not call back into [`AuthService`]: this fires from
/// inside its write paths, sometimes with a connection still open. The sink
/// is expected to mark work and return: it marks the passdb dirty and lets
/// the publisher thread do the render.
pub trait PassdbSink: Send + Sync {
    fn republish(&self);
}

impl AuthService {
    /// Installs the [`PassdbSink`] for this process. Returns `false` if one
    /// was already installed, in which case the existing sink is kept --
    /// swapping it at runtime would mean an NT-hash change could be published
    /// by whichever of two sinks happened to win, and there is no deployment
    /// that needs two.
    pub fn set_passdb_sink(&self, sink: std::sync::Arc<dyn PassdbSink>) -> bool {
        self.passdb_sink.set(sink).is_ok()
    }

    /// Mark Samba's published files stale. Public because the *registry*
    /// half of what they contain — which accounts appear in `smb.conf`'s
    /// `valid users`, and with what — lives outside this crate, in
    /// `sc-core`'s grants and shares. Those mutations have to say so too,
    /// and the sink they have to reach is here (`sc-server`'s admin routes
    /// call this after every one of them).
    ///
    /// Revocation is the reason this is not optional. `smbd` authenticates
    /// against the last file this server published, so a grant deleted, an
    /// account disabled or a group emptied in the web UI changes nothing at
    /// all over SMB until something rewrites it — and nothing does on its
    /// own, not even a restart.
    pub fn republish_passdb(&self) {
        match self.passdb_sink.get() {
            Some(sink) => sink.republish(),
            // `debug`, and it used to be `warn`. Both remaining producers of
            // this line are by design: the CLI paths, which render
            // explicitly, and a server with `smb.enabled = false`, which
            // publishes no file for the change to be stale in. The case that
            // would be a real fault, a serving process with SMB on and no
            // sink, cannot reach here quietly any more: `App::
            // arm_passdb_publisher` logs an error if the install fails. A
            // warning on every password change of an SMB-less deployment is
            // how operators learn to stop reading warnings.
            None => tracing::debug!(
                "an SMB NT hash changed with no passdb sink installed: any published smbpasswd \
                 keeps the old entry until the next `sc-server smb-sync`"
            ),
        }
    }
}

impl AuthService {
    /// Unconditionally (re)derives and stores the NT hash from `pw`,
    /// tagged with `source`. Used at account creation, explicit password
    /// change, and TOTP-disable re-derivation — all points where a plaintext
    /// password was just re-confirmed and the caller has already decided
    /// this derivation should happen.
    pub(crate) fn store_nt_from_plaintext(
        &self,
        conn: &Connection,
        user: UserId,
        pw: &str,
        source: i64,
    ) -> Result<()> {
        let nt = nt_hash(pw);
        let ct = seal_nt(&self.master_key, &nt, user, self.cfg.key_ver)?;
        conn.execute(
            "INSERT INTO user_smb_secret (user, nt_hash_ct, key_ver, source, updated_ns) \
             VALUES (?1, ?2, ?3, ?4, ?5) \
             ON CONFLICT(user) DO UPDATE SET nt_hash_ct=excluded.nt_hash_ct, \
               key_ver=excluded.key_ver, source=excluded.source, updated_ns=excluded.updated_ns",
            rusqlite::params![user.get(), ct, self.cfg.key_ver, source, now_ns()],
        )?;
        Ok(())
    }

    /// Unconditional single-row delete. TOTP enroll does its own delete via
    /// raw SQL inside its transaction (it needs the `source` check first);
    /// this is the helper for callers that don't — currently
    /// `users::set_smb_settings` when a user flips `smb_opt_out` on.
    pub(crate) fn delete_nt(&self, conn: &Connection, user: UserId) -> Result<()> {
        conn.execute(
            "DELETE FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![user.get()],
        )?;
        Ok(())
    }

    pub(crate) fn nt_source(&self, conn: &Connection, user: UserId) -> Result<Option<i64>> {
        let v: Option<i64> = conn
            .query_row(
                "SELECT source FROM user_smb_secret WHERE user = ?1",
                rusqlite::params![user.get()],
                |r| r.get(0),
            )
            .ok();
        Ok(v)
    }

    /// Opportunistic NT-hash backfill ("opportunistic backfill"):
    /// called from every path where a plaintext account password was just
    /// verified successfully (login, DAV Basic). No-op if TOTP is enabled,
    /// the user opted out, an OIDC identity is linked, an NT hash already
    /// exists, or it's a dedicated SMB password (never silently overwritten).
    ///
    /// The OIDC condition is what stops linking from being undone by the next
    /// successful password check (proposal §4.3.6 step 3). Without it a linked
    /// account gets its hash deleted at link time and derived again the first
    /// time its owner logs in with the local password, which is a bypass that
    /// heals itself.
    pub(crate) fn maybe_backfill_nt(&self, conn: &Connection, user: UserId, pw: &str) -> Result<()> {
        let (totp_enabled, smb_opt_out): (bool, bool) = conn.query_row(
            "SELECT totp_secret IS NOT NULL, smb_opt_out FROM user WHERE id = ?1",
            rusqlite::params![user.get()],
            |r| Ok((r.get::<_, bool>(0)?, r.get::<_, bool>(1)?)),
        )?;
        if totp_enabled || smb_opt_out || crate::oidc::linked_on(conn, user)? {
            return Ok(());
        }
        match self.nt_source(conn, user)? {
            None => self.store_nt_from_plaintext(conn, user, pw, NT_SOURCE_ACCOUNT)?,
            Some(s) if s == NT_SOURCE_DEDICATED => {} // respect the user's own separate SMB password
            Some(_) => {}                             // already present, nothing to do
        }
        Ok(())
    }

    /// The three account flags that decide whether an account may hold an
    /// account-derived NT hash, read in one statement so the four callers that
    /// need them cannot drift apart on which they check.
    pub(crate) fn smb_flags(&self, conn: &Connection, u: UserId) -> rusqlite::Result<SmbFlags> {
        let (totp_enabled, smb_opt_out, smb_enabled): (bool, bool, bool) = conn.query_row(
            "SELECT totp_secret IS NOT NULL, smb_opt_out, smb_enabled FROM user WHERE id = ?1",
            rusqlite::params![u.get()],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
        )?;
        Ok(SmbFlags {
            totp_enabled,
            smb_opt_out,
            smb_enabled,
            oidc_linked: crate::oidc::linked_on(conn, u)?,
        })
    }
}

/// Everything about one account that decides what it may hold and what gets
/// published for it.
#[derive(Clone, Copy, Debug)]
pub(crate) struct SmbFlags {
    pub totp_enabled: bool,
    pub smb_opt_out: bool,
    pub smb_enabled: bool,
    pub oidc_linked: bool,
}

impl SmbFlags {
    /// Whether an account-derived NT hash may exist for this account. The
    /// dedicated credential has no such rule: it is precisely the SMB access
    /// path for accounts whose account password stopped being one.
    pub fn may_hold_account_password(&self) -> bool {
        !self.totp_enabled && !self.oidc_linked && !self.smb_opt_out
    }
}

impl AuthService {
    /// Replaces this account's SMB credential with one derived from `smb_pw`,
    /// marking it [`NT_SOURCE_DEDICATED`]. `account_pw` is verified first and
    /// the call fails without touching anything if it is wrong.
    ///
    /// Callable for any account, including one that is TOTP enrolled or OIDC
    /// linked: this credential exists precisely for accounts whose account
    /// password is no longer an SMB credential.
    ///
    /// An account holding `smb_opt_out` gets both of its own SMB toggles
    /// cleared in the same transaction, and the returned flag says so.
    /// Refusing instead would be a dead end the screen has no way out of, and
    /// leaving them alone would store a credential that either never works
    /// (`smb_enabled` off) or works despite a standing instruction not to hold
    /// one (`export_smbpasswd` does not filter on the opt-out). Setting an
    /// SMB-only password is an unambiguous request to reach SMB with it, and
    /// both toggles belong to the same account on the same screen.
    pub async fn set_dedicated_smb_password(
        &self,
        u: UserId,
        account_pw: &SecretString,
        smb_pw: &SecretString,
    ) -> std::result::Result<SmbPasswordSet, SmbPasswordError> {
        use SmbPasswordError as E;
        let stored = self.pw_hash_of(u).map_err(|_| E::BadPassword)?;
        if !self.verify_password_async(&stored, account_pw.clone()).await {
            return Err(E::BadPassword);
        }
        if smb_pw.expose_secret().chars().count() < self.cfg.min_password_len {
            return Err(E::TooShort { min: self.cfg.min_password_len });
        }

        let mut conn = self.pool.get().map_err(|e| E::Internal(e.to_string()))?;
        let tx = conn.transaction().map_err(|e| E::Internal(e.to_string()))?;
        let opt_out_cleared = (|| -> rusqlite::Result<bool> {
            let flags = self.smb_flags(&tx, u)?;
            let cleared = flags.smb_opt_out || !flags.smb_enabled;
            if cleared {
                tx.execute(
                    "UPDATE user SET smb_opt_out = 0, smb_enabled = 1 WHERE id = ?1",
                    rusqlite::params![u.get()],
                )?;
            }
            self.store_nt_from_plaintext(&tx, u, smb_pw.expose_secret(), NT_SOURCE_DEDICATED)
                .map_err(|e| {
                    rusqlite::Error::ToSqlConversionFailure(Box::new(std::io::Error::other(
                        e.to_string(),
                    )))
                })?;
            Ok(cleared)
        })()
        .map_err(|e| E::Internal(e.to_string()))?;
        tx.commit().map_err(|e| E::Internal(e.to_string()))?;

        // The row is in the database and the published file does not carry it,
        // so without this the password the user just set does not work.
        self.republish_passdb();
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(
            Some(u),
            "auth.smb_password_set",
            None,
            None,
            true,
            opt_out_cleared.then_some("smb_toggles_cleared"),
        );
        Ok(SmbPasswordSet { opt_out_cleared })
    }

    /// Deletes the [`NT_SOURCE_DEDICATED`] row and, when the account is
    /// eligible to hold one, derives an [`NT_SOURCE_ACCOUNT`] row from
    /// `account_pw` in the same transaction. The plaintext is in hand here,
    /// which is the only reason this can restore anything at all.
    ///
    /// Returns whether the account came out of this holding an SMB credential.
    /// `false` means TOTP, an OIDC link or an opt-out blocks the
    /// account-derived one, so the account now has no SMB access.
    pub async fn clear_dedicated_smb_password(
        &self,
        u: UserId,
        account_pw: &SecretString,
    ) -> std::result::Result<bool, SmbPasswordError> {
        use SmbPasswordError as E;
        let stored = self.pw_hash_of(u).map_err(|_| E::BadPassword)?;
        if !self.verify_password_async(&stored, account_pw.clone()).await {
            return Err(E::BadPassword);
        }

        let mut conn = self.pool.get().map_err(|e| E::Internal(e.to_string()))?;
        let tx = conn.transaction().map_err(|e| E::Internal(e.to_string()))?;
        let outcome = (|| -> rusqlite::Result<std::result::Result<bool, E>> {
            if self.nt_source_on(&tx, u)? != Some(NT_SOURCE_DEDICATED) {
                return Ok(Err(E::NotSet));
            }
            tx.execute(
                "DELETE FROM user_smb_secret WHERE user = ?1",
                rusqlite::params![u.get()],
            )?;
            let flags = self.smb_flags(&tx, u)?;
            if !flags.may_hold_account_password() {
                return Ok(Ok(false));
            }
            self.store_nt_from_plaintext(&tx, u, account_pw.expose_secret(), NT_SOURCE_ACCOUNT)
                .map_err(|e| {
                    rusqlite::Error::ToSqlConversionFailure(Box::new(std::io::Error::other(
                        e.to_string(),
                    )))
                })?;
            Ok(Ok(true))
        })()
        .map_err(|e| E::Internal(e.to_string()))?;
        let reverted = match outcome {
            Ok(v) => v,
            Err(e) => return Err(e),
        };
        tx.commit().map_err(|e| E::Internal(e.to_string()))?;

        // Either way the published file is now wrong: it carries a credential
        // that no longer exists, or it is missing the one that replaced it.
        self.republish_passdb();
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.audit(
            Some(u),
            "auth.smb_password_cleared",
            None,
            None,
            true,
            Some(if reverted { "reverted_to_account_password" } else { "no_smb_credential" }),
        );
        Ok(reverted)
    }

    /// What actually works over SMB for `u` right now, with the deployment's
    /// TOTP policy already folded in.
    ///
    /// Not "which row exists": a TOTP-enrolled account under
    /// [`SmbTotpPolicy::Block`] holds a dedicated row that is never exported,
    /// and reporting it as working would be a claim the user can only disprove
    /// by failing to connect.
    pub fn smb_credential_state(&self, u: UserId) -> Result<SmbCredential> {
        let conn = self.pool.get()?;
        let flags = self.smb_flags(&conn, u)?;
        let source = self.nt_source(&conn, u)?;
        Ok(fold_smb_credential(flags, source, self.smb_totp_policy()))
    }

    /// The live `smb.totp_policy`. Live rather than `AuthConfig`-fixed because
    /// the admin settings screen changes it without a restart, and a stale
    /// copy here would publish exactly the accounts the operator just excluded.
    pub fn smb_totp_policy(&self) -> SmbTotpPolicy {
        match self.smb_totp_policy.load(std::sync::atomic::Ordering::Relaxed) {
            0 => SmbTotpPolicy::RequireSeparate,
            _ => SmbTotpPolicy::Block,
        }
    }

    /// Called by the settings screen's SMB patch handler, in the same step
    /// that re-renders `smb.conf`.
    pub fn set_smb_totp_policy(&self, p: SmbTotpPolicy) {
        let v = match p {
            SmbTotpPolicy::RequireSeparate => 0,
            SmbTotpPolicy::Block => 1,
        };
        self.smb_totp_policy.store(v, std::sync::atomic::Ordering::Relaxed);
    }

    /// [`Self::nt_source`] against a transaction rather than a pooled
    /// connection, and reporting a read failure instead of swallowing it.
    fn nt_source_on(&self, conn: &Connection, u: UserId) -> rusqlite::Result<Option<i64>> {
        use rusqlite::OptionalExtension;
        conn.query_row(
            "SELECT source FROM user_smb_secret WHERE user = ?1",
            rusqlite::params![u.get()],
            |r| r.get::<_, i64>(0),
        )
        .optional()
    }
}

/// The three inputs that decide what an account can reach SMB with, folded
/// into the one answer a screen can state. Free-standing so the ordering is
/// testable without a database.
pub(crate) fn fold_smb_credential(
    flags: SmbFlags,
    source: Option<i64>,
    policy: SmbTotpPolicy,
) -> SmbCredential {
    if flags.totp_enabled && policy == SmbTotpPolicy::Block {
        return SmbCredential::None(SmbUnavailable::TotpBlocked);
    }
    // `smb_enabled` is the account's own publish toggle and `export_smbpasswd`
    // filters on it, so an account holding a row with it off reaches SMB with
    // nothing. It is the same answer as the opt-out from where the user sits.
    if flags.smb_opt_out || !flags.smb_enabled {
        return SmbCredential::None(SmbUnavailable::OptedOut);
    }
    match source {
        Some(s) if s == NT_SOURCE_DEDICATED => SmbCredential::Dedicated,
        Some(_) => SmbCredential::Account,
        None => SmbCredential::None(SmbUnavailable::NotSet),
    }
}
