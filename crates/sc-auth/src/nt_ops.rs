//! Shared NT-hash derive/store/backfill helpers used by user creation,
//! password change, TOTP enable/disable, and opportunistic backfill.
//! See DESIGN-AUTH.md §2.4.

use crate::db::now_ns;
use crate::nt_hash::{nt_hash, seal_nt};
use crate::AuthService;
use anyhow::Result;
use sc_vfs::UserId;
use rusqlite::Connection;

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
/// is expected to mark work and return, which is what `smb_sync.mark_dirty`
/// means in `DESIGN-AUTH.md` §2.4.
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

    pub(crate) fn republish_passdb(&self) {
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

    /// Opportunistic NT-hash backfill (DESIGN-AUTH §2.4 "opportunistic backfill"):
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
}
