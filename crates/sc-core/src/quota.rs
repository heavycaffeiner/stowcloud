//! Per-user quota enforcement seam (`FEATURES.md` #49).
//!
//! `Core` never computes usage itself — walking the filesystem on every
//! write is exactly what this exists to avoid. It only reports byte deltas
//! it already has on hand (a copy's known size, a finalized upload's known
//! length, a permanent delete's known freed size) to whatever [`QuotaSink`]
//! `sc-server` attaches. The sink owns the actual ledger (`sc-auth`'s
//! `user.usage_bytes` column) and the configured cap (`user.quota_bytes`,
//! ).
//!
//! Same optional-attach shape as `links`/`acl_store`/`share_store`: a `Core`
//! with no sink attached enforces nothing, which is a legitimate (if
//! quota-less) deployment state, not a bug.
//!
//! Not confused with [`Core::quota`] (`ops.rs`), which answers filesystem
//! free/used space for RFC 4331 (`FEATURES.md` #72) — a different question
//! about a different resource (the disk, not an account).
//!
//! The ledger is a running total of bytes the acting user's own writes have
//! added minus what their own deletes have freed, not a live recomputation
//! of "bytes currently attributable to this user" — this codebase has no
//! per-file ownership record, so crediting a delete back to whoever
//! originally wrote those bytes isn't possible without one. A user who
//! deletes something another account uploaded into a shared folder credits
//! their own ledger, not the uploader's; this can drift the ledger away
//! from "what's really on disk for this user" in a shared-folder
//! deployment. Accepted trade-off: the alternative (tracking an owner per
//! file) is a real schema change out of scope here, and a filesystem walk
//! per write is the one option explicitly ruled out.
//!
//! Checking and charging are two separate calls, not one atomic
//! reserve-then-commit: [`QuotaSink::check`] runs before the write,
//! [`QuotaSink::charge`] only after it actually succeeds. Two concurrent
//! writes can both pass `check` and jointly land the user slightly over
//! their cap — accepted as a soft limit rather than serializing every write
//! through a global lock for a narrow race.

use sc_vfs::UserId;
use std::sync::Arc;

use crate::error::CoreError;

pub trait QuotaSink: Send + Sync {
    /// `Err(CoreError::QuotaExceeded)` if charging `user` `additional` more
    /// bytes would exceed their configured cap. Read-only — no side effect.
    fn check(&self, user: UserId, additional: u64) -> Result<(), CoreError>;

    /// Adjust `user`'s running usage by `delta` bytes (negative on a freed
    /// delete). Called only once the corresponding filesystem change has
    /// already succeeded.
    fn charge(&self, user: UserId, delta: i64);
}

impl crate::Core {
    pub fn attach_quota_sink(&self, sink: Arc<dyn QuotaSink>) -> anyhow::Result<()> {
        self.quota_sink
            .set(sink)
            .map_err(|_| anyhow::anyhow!("quota sink already attached"))
    }

    pub fn check_quota(&self, user: UserId, additional: u64) -> Result<(), CoreError> {
        match self.quota_sink.get() {
            Some(s) => s.check(user, additional),
            None => Ok(()),
        }
    }

    pub fn charge_quota(&self, user: UserId, delta: i64) {
        if delta == 0 {
            return;
        }
        if let Some(s) = self.quota_sink.get() {
            s.charge(user, delta);
        }
    }
}
