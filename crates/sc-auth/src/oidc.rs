//! OIDC storage. See `docs/proposals/stowcloud-0-oidc-login.md` §4.2 for the
//! schema and §4.3.1 for the flow this half of it exists to protect.
//!
//! This module deliberately knows nothing about HTTP, JWTs, or the IdP. It
//! stores what an authorization-code round trip has to survive on, and it
//! hands that record back exactly once. Everything about *talking* to a
//! provider lives in `sc-oidc`, for the dependency-isolation reason
//! 's first line gives for keeping this crate
//! protocol-agnostic.

use crate::db::now_ns;
use crate::nt_ops::{NT_SOURCE_ACCOUNT, NT_SOURCE_DEDICATED};
use crate::AuthService;
use anyhow::Result;
use sc_vfs::UserId;
use rusqlite::{Connection, OptionalExtension, TransactionBehavior};
use secrecy::{ExposeSecret, SecretString};
use std::time::Duration;

/// How long an `oidc_flow` row stays usable (§4.2: `created + 10 minutes`).
///
/// Fixed rather than configurable. The window has to be long enough for a
/// person to finish whatever their IdP asks of them -- a password, a hardware
/// key, a push notification on a phone that is in another room -- and short
/// enough that a leaked row is worthless before anyone can act on it. A knob
/// here would only offer new ways to get that trade wrong, and the value is
/// not one an operator has any information to tune.
pub const OIDC_FLOW_TTL: Duration = Duration::from_secs(10 * 60);

/// What a flow was started for. Persisted as the `oidc_flow.mode` integer.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OidcFlowMode {
    /// Issue a session for whichever account the returned identity is
    /// *already* linked to. Never creates one (§2.3: no JIT provisioning).
    Login,
    /// Attach the returned identity to [`OidcFlow::link_user`], the account
    /// whose session started the flow.
    Link,
}

const MODE_LOGIN: i64 = 0;
const MODE_LINK: i64 = 1;

impl OidcFlowMode {
    fn as_i64(self) -> i64 {
        match self {
            OidcFlowMode::Login => MODE_LOGIN,
            OidcFlowMode::Link => MODE_LINK,
        }
    }

    /// `None` for any value this build does not know. A row written by a
    /// future version with a third mode is treated as no flow at all rather
    /// than silently taken for a login -- the two modes differ in whether a
    /// session gets issued, so guessing is not an option.
    fn from_i64(v: i64) -> Option<Self> {
        match v {
            MODE_LOGIN => Some(OidcFlowMode::Login),
            MODE_LINK => Some(OidcFlowMode::Link),
            _ => None,
        }
    }
}

/// A flow as it goes in. `state_hash` is the key it is taken back by, and
/// `created_ns`/`expires_ns` are set by [`AuthService::create_oidc_flow`]
/// from [`OIDC_FLOW_TTL`], so neither appears here.
///
/// A struct rather than eight positional parameters: four of the fields are
/// 32-byte digests of four different secrets, and swapping two of them at a
/// call site would compile, run, and quietly disable the browser binding.
#[derive(Debug)]
pub struct NewOidcFlow<'a> {
    pub state_hash: [u8; 32],
    /// `sha256` of the `__Host-sc_oidc` cookie value handed to the browser in
    /// the same response that starts the flow (§4.3.1).
    pub binding_hash: [u8; 32],
    pub nonce_hash: [u8; 32],
    pub code_verifier: &'a SecretString,
    pub mode: OidcFlowMode,
    /// Required when `mode` is [`OidcFlowMode::Link`]; ignored otherwise.
    pub link_user: Option<UserId>,
    /// An already-validated same-origin path. This crate stores it verbatim
    /// and never inspects it -- the rules (leading `/`, no `//` or `/\`,
    /// printable ASCII only) belong to the HTTP layer that puts the value in
    /// a `Location` header (§5-1).
    pub return_to: Option<&'a str>,
}

/// A flow as it comes back out of [`AuthService::take_oidc_flow`].
#[derive(Clone, Debug)]
pub struct OidcFlow {
    pub binding_hash: Vec<u8>,
    pub nonce_hash: Vec<u8>,
    pub code_verifier: SecretString,
    pub mode: OidcFlowMode,
    pub link_user: Option<UserId>,
    pub return_to: Option<String>,
    pub created_ns: i64,
    pub expires_ns: i64,
}

/// One `oidc_identity` row, for the screens that display a link rather than
/// authenticate against it (`GET /api/auth/session`, the admin lookup).
#[derive(Clone, Debug)]
pub struct OidcIdentity {
    pub issuer: String,
    pub subject: String,
    pub linked_ns: i64,
    pub last_login_ns: Option<i64>,
}

/// Why [`AuthService::link_oidc_identity`] refused. The three variants are
/// the three answers §4.3.2 step 3 distinguishes, plus the empty-string guard
/// that backs `oidc.invalid_subject` (§5-2 table A).
///
/// Note what is *not* here: "already linked to this same subject" is not an
/// error. Re-linking an identity an account already has is idempotent
/// success, so a user who double-submits the callback does not see a failure
/// for a state that is exactly what they asked for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum OidcLinkError {
    /// This `(issuer, subject)` is already linked to a different account.
    SubjectTaken,
    /// This account already has a *different* subject linked. One identity
    /// per account (§4.2).
    AlreadyLinked,
    /// Empty issuer or subject. An ID token that produced one is malformed
    /// and `sc-oidc` rejects it first (§4.3.3 step 11); this is the guard
    /// against an admin manual link typing nothing into the box.
    InvalidSubject,
    Internal(String),
}

impl std::fmt::Display for OidcLinkError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            OidcLinkError::SubjectTaken => write!(f, "that identity is already linked to another account"),
            OidcLinkError::AlreadyLinked => write!(f, "this account already has a linked identity"),
            OidcLinkError::InvalidSubject => write!(f, "issuer and subject must both be non-empty"),
            OidcLinkError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for OidcLinkError {}

/// Why [`AuthService::unlink_oidc_identity`] refused. Maps one-to-one onto
/// the three answers `DELETE /api/auth/oidc/link` gives (§5-1): `401
/// auth.invalid_credentials`, `404 oidc.not_linked`, `500 internal`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum OidcUnlinkError {
    /// Password re-confirmation failed. Only reachable on the self-service
    /// path; the admin path passes no password and cannot produce this.
    BadPassword,
    NotLinked,
    Internal(String),
}

impl std::fmt::Display for OidcUnlinkError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            OidcUnlinkError::BadPassword => write!(f, "bad password"),
            OidcUnlinkError::NotLinked => write!(f, "no linked identity"),
            OidcUnlinkError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for OidcUnlinkError {}

/// What an unlink actually did, beyond removing the row.
///
/// Both fields exist so the caller can *say* what happened rather than leave
/// the operator to find out later. §4.3.6 is explicit that an admin unlink,
/// which has no plaintext password, cannot restore SMB access and must
/// announce that in its response and its audit event instead of leaving the
/// account quietly broken.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct OidcUnlink {
    /// Whether the account-password NT hash was re-derived on the spot.
    /// `false` means SMB stays closed for this account until its owner
    /// changes their password. Always `false` when no password was supplied,
    /// and also `false` when the account opted out of SMB or holds a
    /// dedicated SMB password that was never removed in the first place.
    pub smb_nt_restored: bool,
    /// Sessions deleted because they carried [`crate::AMR_OIDC`]. Password
    /// sessions are left alone: unlinking withdraws one way in, not every
    /// way in.
    pub oidc_sessions_revoked: u64,
}

/// `true` when `user` has an OIDC identity, read through an existing
/// connection or open transaction.
///
/// A free function rather than a method because the three NT-hash derivation
/// sites that consult it (`maybe_backfill_nt`, `set_password`,
/// `totp_disable`) all already hold a connection, and taking a second one
/// from the pool mid-transaction is how a self-deadlock gets written.
pub(crate) fn linked_on(conn: &Connection, user: UserId) -> rusqlite::Result<bool> {
    conn.query_row(
        "SELECT EXISTS(SELECT 1 FROM oidc_identity WHERE user = ?1)",
        rusqlite::params![user.get()],
        |r| r.get(0),
    )
}

impl AuthService {
    /// Resolves an IdP identity to a local account. `Ok(None)` means "no such
    /// link": the caller answers `oidc.not_linked` and **must not** create an
    /// account (§2.3, no JIT provisioning).
    ///
    /// Deliberately does not check `user.disabled`. The caller does that, so
    /// that "linked but disabled" and "not linked" stay distinguishable in
    /// the audit log while remaining identical on the wire (§5-2).
    pub fn find_oidc_identity(&self, issuer: &str, subject: &str) -> Result<Option<UserId>> {
        let conn = self.pool.get()?;
        let uid: Option<i64> = conn
            .query_row(
                "SELECT user FROM oidc_identity WHERE issuer = ?1 AND subject = ?2",
                rusqlite::params![issuer, subject],
                |r| r.get(0),
            )
            .optional()?;
        Ok(uid.map(|v| UserId::new(v as u32)))
    }

    /// `true` when `u` has an OIDC identity. Read at DAV Basic auth time
    /// (§4.3.5) and at every NT-hash derivation site (§4.3.6).
    pub fn oidc_linked(&self, u: UserId) -> Result<bool> {
        let conn = self.pool.get()?;
        Ok(linked_on(&conn, u)?)
    }

    /// The link itself, for display. Separate from [`Self::oidc_linked`]
    /// because the hot paths only need the boolean and have no business
    /// pulling a subject out of the database to throw it away.
    pub fn oidc_identity_of(&self, u: UserId) -> Result<Option<OidcIdentity>> {
        let conn = self.pool.get()?;
        let row = conn
            .query_row(
                "SELECT issuer, subject, linked_ns, last_login_ns FROM oidc_identity WHERE user = ?1",
                rusqlite::params![u.get()],
                |r| {
                    Ok(OidcIdentity {
                        issuer: r.get(0)?,
                        subject: r.get(1)?,
                        linked_ns: r.get(2)?,
                        last_login_ns: r.get(3)?,
                    })
                },
            )
            .optional()?;
        Ok(row)
    }

    /// Stamps `last_login_ns` after a successful OIDC login. Display only --
    /// nothing authenticates against this column -- so a failure here is
    /// logged and swallowed rather than failing a login that already
    /// succeeded, same rule `audit` follows.
    pub fn touch_oidc_last_login(&self, u: UserId) {
        let res = (|| -> Result<()> {
            let conn = self.pool.get()?;
            conn.execute(
                "UPDATE oidc_identity SET last_login_ns = ?1 WHERE user = ?2",
                rusqlite::params![now_ns(), u.get()],
            )?;
            Ok(())
        })();
        if let Err(e) = res {
            tracing::warn!(error = %e, user = u.get(), "recording oidc last_login_ns failed");
        }
    }

    /// Links `subject` to `u` and, **in the same transaction**, removes the
    /// SMB NT hash derived from the account password.
    ///
    /// That second half is not bookkeeping. SMB authentication never reaches
    /// this crate: `smbd` checks the `smbpasswd` file this server publishes,
    /// so there is no authentication-time hook where a linked account could
    /// be refused, the way DAV Basic refuses one (§4.3.5). The only way to
    /// close the account password as an SMB credential is for the hash not to
    /// exist. `create_user` derives one unconditionally, so an account linked
    /// later already has a live hash and a published `smbpasswd` line; adding
    /// a condition to the *future* derivation sites alone would leave the
    /// bypass wide open on every existing account. This is the correction
    /// §4.3.6 records at length.
    ///
    /// A [`NT_SOURCE_DEDICATED`] secret is left alone: the user set that one
    /// separately and on purpose, and it is not the account password.
    ///
    /// `totp_enroll` does the identical thing for the identical reason
    /// (`totp.rs`, §2.4) and is the precedent this follows.
    ///
    /// Re-linking the same identity to the same account is idempotent
    /// success, and still re-runs the NT-hash removal, so the postcondition
    /// "linked, and no account-derived SMB credential" holds however many
    /// times it is called.
    pub fn link_oidc_identity(
        &self,
        u: UserId,
        issuer: &str,
        subject: &str,
    ) -> std::result::Result<(), OidcLinkError> {
        use OidcLinkError as E;
        if issuer.is_empty() || subject.is_empty() {
            return Err(E::InvalidSubject);
        }
        let mut conn = self.pool.get().map_err(|e| E::Internal(e.to_string()))?;
        // `IMMEDIATE` for the same reason `take_oidc_flow` uses it: the
        // uniqueness decision is read before it is written, and under the
        // default deferred behaviour two concurrent links would both read
        // "free" and then collide on the write.
        let tx = conn
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|e| E::Internal(e.to_string()))?;

        let dropped_nt = (|| -> rusqlite::Result<std::result::Result<bool, E>> {
            let owner: Option<i64> = tx
                .query_row(
                    "SELECT user FROM oidc_identity WHERE issuer = ?1 AND subject = ?2",
                    rusqlite::params![issuer, subject],
                    |r| r.get(0),
                )
                .optional()?;
            match owner {
                Some(existing) if existing != i64::from(u.get()) => return Ok(Err(E::SubjectTaken)),
                Some(_) => {} // already ours: idempotent, fall through to the SMB half
                None => {
                    if linked_on(&tx, u)? {
                        return Ok(Err(E::AlreadyLinked));
                    }
                    tx.execute(
                        "INSERT INTO oidc_identity (issuer, subject, user, linked_ns, last_login_ns) \
                         VALUES (?1, ?2, ?3, ?4, NULL)",
                        rusqlite::params![issuer, subject, u.get(), now_ns()],
                    )?;
                }
            }

            // §4.3.6 step 1.
            let source: Option<i64> = tx
                .query_row(
                    "SELECT source FROM user_smb_secret WHERE user = ?1",
                    rusqlite::params![u.get()],
                    |r| r.get(0),
                )
                .optional()?;
            let drop_it = matches!(source, Some(s) if s != NT_SOURCE_DEDICATED);
            if drop_it {
                tx.execute(
                    "DELETE FROM user_smb_secret WHERE user = ?1",
                    rusqlite::params![u.get()],
                )?;
            }
            Ok(Ok(drop_it))
        })()
        .map_err(|e| E::Internal(e.to_string()))??;

        tx.commit().map_err(|e| E::Internal(e.to_string()))?;

        // Linking changes what this account's password is allowed to do, so
        // every cached credential decision about it has to be made again --
        // the DAV credential cache and connection memo both key off this
        // counter.
        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        if dropped_nt {
            // §4.3.6 step 2. Without this the row is gone from the database
            // and the already-published `smbpasswd` still lists the account,
            // so SMB stays open on exactly the credential this call was
            // supposed to close.
            self.republish_passdb();
        }
        self.audit(
            Some(u),
            "auth.oidc_linked",
            Some(issuer),
            None,
            true,
            Some(if dropped_nt { "smb_nt_deleted" } else { "no_smb_nt_to_delete" }),
        );
        Ok(())
    }

    /// Unlinks `u`'s identity, re-deriving the SMB NT hash when `pw` is
    /// `Some`, and revokes every session that OIDC issued.
    ///
    /// `pw` is verified here, not assumed verified. `Some` is the
    /// self-service path (`DELETE /api/auth/oidc/link`, which re-confirms the
    /// password for the same reason disabling TOTP does -- a live session
    /// alone must not be enough to change a security control) and its
    /// plaintext is what makes the NT hash re-derivable on the spot. `None`
    /// is the admin path, which has no plaintext and therefore **cannot**
    /// restore SMB access; [`OidcUnlink::smb_nt_restored`] says so, and the
    /// caller is expected to pass that on rather than let the operator
    /// discover it from a user's bug report.
    ///
    /// The session sweep is the other half of "cutting the link cuts the
    /// access". `validate_session` looks at expiry and `user.disabled` and
    /// nothing else, so removing the identity row on its own would leave
    /// every session the IdP had already vouched for alive and working.
    pub fn unlink_oidc_identity(
        &self,
        u: UserId,
        pw: Option<&SecretString>,
    ) -> std::result::Result<OidcUnlink, OidcUnlinkError> {
        use OidcUnlinkError as E;

        if let Some(pw) = pw {
            let stored = self.pw_hash_of(u).map_err(|_| E::BadPassword)?;
            if !self.verify_password_sync_gated(&stored, pw.expose_secret()) {
                return Err(E::BadPassword);
            }
        }

        let mut conn = self.pool.get().map_err(|e| E::Internal(e.to_string()))?;
        let tx = conn
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|e| E::Internal(e.to_string()))?;

        let outcome = (|| -> rusqlite::Result<std::result::Result<OidcUnlink, E>> {
            let removed = tx.execute(
                "DELETE FROM oidc_identity WHERE user = ?1",
                rusqlite::params![u.get()],
            )?;
            if removed == 0 {
                return Ok(Err(E::NotLinked));
            }

            // Re-derivation mirrors `totp_disable`'s conditions exactly: an
            // account that opted out of SMB does not want a hash at all, and
            // a dedicated SMB password is not ours to overwrite.
            let smb_nt_restored = match pw {
                None => false,
                Some(pw) => {
                    let smb_opt_out: bool = tx.query_row(
                        "SELECT smb_opt_out FROM user WHERE id = ?1",
                        rusqlite::params![u.get()],
                        |r| r.get(0),
                    )?;
                    let source: Option<i64> = tx
                        .query_row(
                            "SELECT source FROM user_smb_secret WHERE user = ?1",
                            rusqlite::params![u.get()],
                            |r| r.get(0),
                        )
                        .optional()?;
                    if smb_opt_out || source == Some(NT_SOURCE_DEDICATED) {
                        false
                    } else {
                        // `store_nt_from_plaintext` returns `anyhow`, not
                        // `rusqlite`, because sealing can fail on its own.
                        self.store_nt_from_plaintext(&tx, u, pw.expose_secret(), NT_SOURCE_ACCOUNT)
                            .map_err(|e| {
                                rusqlite::Error::ToSqlConversionFailure(Box::new(std::io::Error::other(
                                    e.to_string(),
                                )))
                            })?;
                        true
                    }
                }
            };

            let oidc_sessions_revoked = tx.execute(
                "DELETE FROM session WHERE user = ?1 AND (amr & ?2) != 0",
                rusqlite::params![u.get(), i64::from(crate::AMR_OIDC)],
            )? as u64;

            Ok(Ok(OidcUnlink { smb_nt_restored, oidc_sessions_revoked }))
        })()
        .map_err(|e| E::Internal(e.to_string()))??;

        tx.commit().map_err(|e| E::Internal(e.to_string()))?;

        self.generation.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        if outcome.smb_nt_restored {
            self.republish_passdb();
        }
        self.audit(
            Some(u),
            "auth.oidc_unlinked",
            None,
            None,
            true,
            Some(if outcome.smb_nt_restored {
                "smb_nt_restored"
            } else {
                "smb access stays closed until this account's password is changed or a dedicated SMB password is set"
            }),
        );
        Ok(outcome)
    }

    /// Records a flow that is about to send a browser to the IdP.
    ///
    /// Nothing here is checked against the account model on purpose: a login
    /// flow has no account yet, and a link flow's `link_user` is re-checked
    /// against the live session when the callback lands (§4.3.2 step 2)
    /// because the session can change during the round trip. Checking it here
    /// as well would only produce a TOCTOU pair that reads like a guarantee.
    pub fn create_oidc_flow(&self, f: NewOidcFlow<'_>) -> Result<()> {
        let created = now_ns();
        let conn = self.pool.get()?;
        conn.execute(
            "INSERT INTO oidc_flow \
             (state_hash, binding_hash, nonce_hash, code_verifier, mode, link_user, return_to, created_ns, expires_ns) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
            rusqlite::params![
                f.state_hash.to_vec(),
                f.binding_hash.to_vec(),
                f.nonce_hash.to_vec(),
                f.code_verifier.expose_secret(),
                f.mode.as_i64(),
                f.link_user.map(|u| u.get()),
                f.return_to,
                created,
                created + OIDC_FLOW_TTL.as_nanos() as i64,
            ],
        )?;
        Ok(())
    }

    /// Looks a flow up **and deletes it**, in one transaction. `Ok(None)`
    /// means no such flow, which is also what a second attempt at the same
    /// `state` gets: the row is gone the moment the first attempt commits, so
    /// a replayed callback URL cannot be redeemed twice.
    ///
    /// The transaction is `IMMEDIATE` so the write lock is taken before the
    /// `SELECT` rather than upgraded after it. Under the default deferred
    /// behaviour two concurrent replays would both read the row and then race
    /// on the `DELETE`, and in WAL mode the loser gets a snapshot conflict --
    /// a storage error, when what actually happened is exactly the reuse this
    /// function exists to refuse.
    ///
    /// **An expired row is returned, not hidden.** The caller compares
    /// `expires_ns` itself, because `oidc.expired` and `oidc.bad_state` are
    /// different answers with different meanings for whoever is reading the
    /// login screen (§5-2 table B), and this function cannot tell them apart
    /// once it has swallowed the row. Consuming it either way is deliberate:
    /// an expired flow is spent, not retryable.
    ///
    /// Sweeps every other expired row in the same transaction -- §4.2's
    /// "delete opportunistically on every callback". This is the sweep that is
    /// certain to run, since the callback is the only thing that ever consumes
    /// a flow; [`Self::sweep_oidc_flows`] covers a deployment where nobody
    /// ever finishes one.
    pub fn take_oidc_flow(&self, state_hash: &[u8; 32]) -> Result<Option<OidcFlow>> {
        let key = state_hash.to_vec();
        let mut conn = self.pool.get()?;
        let tx = conn.transaction_with_behavior(TransactionBehavior::Immediate)?;
        let flow = tx
            .query_row(
                "SELECT binding_hash, nonce_hash, code_verifier, mode, link_user, return_to, created_ns, expires_ns \
                 FROM oidc_flow WHERE state_hash = ?1",
                rusqlite::params![key],
                |r| {
                    Ok((
                        r.get::<_, Vec<u8>>(0)?,
                        r.get::<_, Vec<u8>>(1)?,
                        r.get::<_, String>(2)?,
                        r.get::<_, i64>(3)?,
                        r.get::<_, Option<i64>>(4)?,
                        r.get::<_, Option<String>>(5)?,
                        r.get::<_, i64>(6)?,
                        r.get::<_, i64>(7)?,
                    ))
                },
            )
            .optional()?;
        if flow.is_some() {
            tx.execute("DELETE FROM oidc_flow WHERE state_hash = ?1", rusqlite::params![key])?;
        }
        tx.execute("DELETE FROM oidc_flow WHERE expires_ns < ?1", rusqlite::params![now_ns()])?;
        tx.commit()?;

        let Some((binding_hash, nonce_hash, code_verifier, mode, link_user, return_to, created_ns, expires_ns)) = flow
        else {
            return Ok(None);
        };
        // An unrecognised mode is "no flow" (see `OidcFlowMode::from_i64`).
        // The row is already deleted by then, which is the right outcome for
        // a record this build cannot act on.
        let Some(mode) = OidcFlowMode::from_i64(mode) else {
            return Ok(None);
        };
        Ok(Some(OidcFlow {
            binding_hash,
            nonce_hash,
            code_verifier: SecretString::from(code_verifier),
            mode,
            link_user: link_user.map(|v| UserId::new(v as u32)),
            return_to,
            created_ns,
            expires_ns,
        }))
    }

    /// Deletes every flow whose TTL has passed, returning how many went.
    ///
    /// Not the primary cleanup path -- [`Self::take_oidc_flow`] sweeps on
    /// every callback -- but a deployment where OIDC is configured and nobody
    /// ever completes a login would otherwise accumulate one abandoned row
    /// per attempt forever. `spawn_audit_sweeper` calls this; see the note
    /// there about why this crate's one periodic loop does two jobs.
    pub fn sweep_oidc_flows(&self) -> Result<u64> {
        let conn = self.pool.get()?;
        let n = conn.execute(
            "DELETE FROM oidc_flow WHERE expires_ns < ?1",
            rusqlite::params![now_ns()],
        )?;
        Ok(n as u64)
    }
}
