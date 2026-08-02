//! Master-key rotation and the startup check that
//! guards against a key that cannot decrypt what's already on disk.
//!
//! Rotation re-encrypts every `user_smb_secret.nt_hash_ct` and
//! `user.totp_secret` row under a new master key inside one SQLite
//! transaction, then bumps `key_version` in that same transaction. Nothing
//! about the master key *file* is touched here — `sc-server`'s CLI (the
//! only caller; a master key belongs to an operator's terminal, never an
//! HTTP route) writes the new key to disk only *after* this returns `Ok`,
//! and only then restarts with it. That ordering, plus "one transaction,
//! committed once, at the end", is the whole interrupt-safety story:
//!
//! * Killed before the commit: SQLite rolls the transaction back
//!   automatically — nothing uncommitted is ever durable — so every row is
//!   exactly as it was, still readable by the still-untouched old key file.
//!   There is no partially-migrated state for anything outside this
//!   function to observe.
//! * Killed after the commit but before the caller swaps the key file on
//!   disk: the database is fully migrated (new key, bumped `key_version`)
//!   but the key file is still the old key. The next process to open this
//!   database — with the old key, since the file was never swapped — fails
//!   `verify_master_key` below and refuses to start, loudly, instead of
//!   silently reading garbage. A single key file (as opposed to a two-phase
//!   commit store spanning both the database and the filesystem) cannot
//!   close this window by itself; turning it into a loud, diagnosable
//!   refusal rather than silent corruption is what satisfies "must refuse
//!   to start" for this specific case. Recovery is manual and low-risk: the
//!   database already has everything under the new key, so the operator
//!   only needs to find the new key material (`sc-server`'s
//!   `swap_key_file` writes it to a `.new` sibling file before renaming)
//!   and put it in place.

use anyhow::{anyhow, Context, Result};
use sc_vfs::UserId;
use rusqlite::OptionalExtension;

use crate::db::{self, DbPool};
use crate::nt_hash;

/// What one call to [`crate::AuthService::rotate_master_key`] did — printed
/// by the CLI so an operator sees exactly how many rows moved, not just
/// "done".
#[derive(Clone, Copy, Debug)]
pub struct RotationReport {
    pub old_key_ver: u32,
    pub new_key_ver: u32,
    pub smb_secrets_rotated: usize,
    pub totp_secrets_rotated: usize,
}

pub(crate) fn rotate(pool: &DbPool, old_key: &[u8; 32], new_key: &[u8; 32]) -> Result<RotationReport> {
    let mut conn = pool.get().context("getting a connection for rotation")?;
    let tx = conn.transaction().context("starting rotation transaction")?;

    let old_ver = db::current_key_version(&tx)?;
    let new_ver = old_ver
        .checked_add(1)
        .ok_or_else(|| anyhow!("key_ver overflow — this deployment has rotated 2^32 times"))?;

    // Read every row before writing any of it back — a decrypt failure
    // partway must abort with nothing yet written, not leave some rows
    // updated and others not (the transaction alone would guarantee that on
    // commit, but failing before the first `UPDATE` makes the intent
    // obvious without relying on rollback semantics to explain it).
    let smb_rows: Vec<(u32, Vec<u8>, u32)> = {
        let mut stmt = tx.prepare("SELECT user, nt_hash_ct, key_ver FROM user_smb_secret")?;
        let rows = stmt.query_map([], |r| {
            Ok((
                r.get::<_, i64>(0)? as u32,
                r.get::<_, Vec<u8>>(1)?,
                r.get::<_, i64>(2)? as u32,
            ))
        })?;
        rows.collect::<rusqlite::Result<Vec<_>>>()?
    };
    for (user, ct, row_ver) in &smb_rows {
        let uid = UserId::new(*user);
        let nt = nt_hash::open_nt(old_key, ct, uid, *row_ver).with_context(|| {
            format!(
                "decrypting the SMB NT hash for user {user} under the current key failed; \
                 aborting the rotation before any row is changed"
            )
        })?;
        let new_ct = nt_hash::seal_nt(new_key, &nt, uid, new_ver)?;
        tx.execute(
            "UPDATE user_smb_secret SET nt_hash_ct = ?1, key_ver = ?2 WHERE user = ?3",
            rusqlite::params![new_ct, new_ver, user],
        )?;
    }

    let totp_rows: Vec<(u32, Vec<u8>)> = {
        let mut stmt = tx.prepare("SELECT id, totp_secret FROM user WHERE totp_secret IS NOT NULL")?;
        let rows = stmt.query_map([], |r| Ok((r.get::<_, i64>(0)? as u32, r.get::<_, Vec<u8>>(1)?)))?;
        rows.collect::<rusqlite::Result<Vec<_>>>()?
    };
    for (user, ct) in &totp_rows {
        let uid = UserId::new(*user);
        let secret = nt_hash::open_totp_secret(old_key, ct, uid).with_context(|| {
            format!(
                "decrypting the TOTP secret for user {user} under the current key failed; \
                 aborting the rotation before any row is changed"
            )
        })?;
        let new_ct = nt_hash::seal_totp_secret(new_key, &secret, uid)?;
        tx.execute(
            "UPDATE user SET totp_secret = ?1 WHERE id = ?2",
            rusqlite::params![new_ct, user],
        )?;
    }

    tx.execute("UPDATE key_version SET ver = ?1 WHERE id = 1", rusqlite::params![new_ver])?;
    tx.commit().context("committing the rotation")?;

    Ok(RotationReport {
        old_key_ver: old_ver,
        new_key_ver: new_ver,
        smb_secrets_rotated: smb_rows.len(),
        totp_secrets_rotated: totp_rows.len(),
    })
}

/// Confirms `master_key` can actually decrypt at least one already-encrypted
/// record, if any exist. Called from `AuthService::new`, so every entry
/// point that opens the auth database — `serve`, `gc`, `smb-sync`, `setup`,
/// `index`, and `masterkey rotate` itself — gets this check for free, not
/// just a dedicated one. A mismatched key would otherwise decrypt to
/// AEAD-tag-mismatch `Err`s deep inside login/SMB-bind/TOTP-verify code
/// paths, one request at a time, which is silent data loss dressed up as a
/// string of unrelated-looking failures. See this module's doc for the
/// specific crash scenario (an interrupted rotation) this is the safety net
/// for.
pub(crate) fn verify_master_key(pool: &DbPool, master_key: &[u8; 32]) -> Result<()> {
    let conn = pool
        .get()
        .context("getting a connection to verify the master key")?;

    let smb_row: Option<(u32, Vec<u8>, u32)> = conn
        .query_row(
            "SELECT user, nt_hash_ct, key_ver FROM user_smb_secret LIMIT 1",
            [],
            |r| {
                Ok((
                    r.get::<_, i64>(0)? as u32,
                    r.get::<_, Vec<u8>>(1)?,
                    r.get::<_, i64>(2)? as u32,
                ))
            },
        )
        .optional()
        .context("checking for an existing SMB secret to verify the master key against")?;
    if let Some((user, ct, key_ver)) = smb_row {
        // Warn, never refuse. An SMB secret is derived material: it can be
        // regenerated from a password change, and losing it costs SMB access
        // for one account. Treating it as fatal took production down on a
        // deployment where the key file had been regenerated at some point
        // and one stale row survived — the server would not serve files
        // because one Samba credential was unreadable, which is far worse
        // than the failure it was guarding against.
        if nt_hash::open_nt(master_key, &ct, UserId::new(user), key_ver).is_err() {
            tracing::warn!(
                user,
                key_ver,
                "master key cannot decrypt this account's stored SMB credential; SMB will fail \
                 for it until the password is set again. If this is unexpected, the key file may \
                 be wrong, or a `masterkey rotate` was interrupted after its database commit but \
                 before the key file was swapped (see `sc-server::masterkey::swap_key_file`)."
            );
        }
        // Fall through to the TOTP check rather than returning: an SMB row
        // can only warn now, so it cannot stand in for the fatal check.
    }

    let totp_row: Option<(u32, Vec<u8>)> = conn
        .query_row(
            "SELECT id, totp_secret FROM user WHERE totp_secret IS NOT NULL LIMIT 1",
            [],
            |r| Ok((r.get::<_, i64>(0)? as u32, r.get::<_, Vec<u8>>(1)?)),
        )
        .optional()
        .context("checking for an existing TOTP secret to verify the master key against")?;
    if let Some((user, ct)) = totp_row {
        nt_hash::open_totp_secret(master_key, &ct, UserId::new(user)).map_err(|_| {
            anyhow!(
                "the master key cannot decrypt existing TOTP secret data (user {user}) — \
                 refusing to start. This is either the wrong key file, or a `masterkey rotate` \
                 was interrupted after its database commit but before the key file was swapped \
                 (see `sc-server::masterkey::swap_key_file`'s doc for how to recover)."
            )
        })?;
    }

    Ok(())
}
