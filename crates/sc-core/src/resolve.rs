//! The single entry point that turns a virtual path (`/{label}/sub/path`)
//! into `(ShareRoot, SafePath)` and checks the ACL — `ARCHITECTURE.md` §3.2:
//! "this mapping is the core of path concealment... the label → (ShareRoot,
//! SafePath) conversion happens in exactly one place, the request entry
//! point." Every other `Core` method goes through
//! `resolve_want`; nothing else in this crate parses a virtual path.

use std::sync::Arc;

use sc_acl::Perms;
use sc_vfs::{SafePath, ShareId, ShareRoot, UserId};

use crate::error::CoreError;

#[derive(Clone)]
pub struct Resolved {
    pub share: ShareId,
    pub root: Arc<ShareRoot>,
    pub path: SafePath,
    /// The caller's full effective permission set at `path`. Carried on the
    /// resolution because every protocol layer that resolves a path also has
    /// to report the permissions on it, and re-deriving them means a second
    /// walk of the grant list.
    pub perms: Perms,
}

impl crate::Core {
    /// Resolve a virtual path with a plain `READ` check. Operations that need
    /// a different permission (`CREATE`, `WRITE`, ...) go through the
    /// crate-private `resolve_want` instead, which is identical except for
    /// which `Perms` bit(s) are checked.
    pub fn resolve(&self, user: UserId, vpath: &str) -> Result<Resolved, CoreError> {
        self.resolve_want(user, vpath, Perms::READ)
    }

    /// Resolve a virtual path with the `WRITE` check an upload's destination
    /// needs — mirrors `write_text` (`ops.rs`), which checks only `WRITE` for
    /// both a brand-new file and an overwrite, never `CREATE`. Upload session
    /// creation must call this, not `resolve`: `resolve`'s hardcoded `READ`
    /// let a read-only grant open a session that then wrote bytes no `fs`
    /// write call would ever have been allowed to make. Purpose-named and
    /// parameterless (no `Perms` argument to get wrong) rather than exposing
    /// `resolve_want` itself, so the next upload call site cannot repeat the
    /// mistake by passing the wrong bit.
    pub fn resolve_for_upload(&self, user: UserId, vpath: &str) -> Result<Resolved, CoreError> {
        self.resolve_want(user, vpath, Perms::WRITE)
    }

    pub(crate) fn resolve_want(&self, user: UserId, vpath: &str, want: Perms) -> Result<Resolved, CoreError> {
        // No-op when homes are disabled (`FEATURES.md` #47); otherwise a
        // one-time-per-user mkdir + grant so "/Home/..." resolves the same
        // way any other grant-projected label does, with no branch below
        // this point needing to know homes exist at all.
        if let Err(e) = self.ensure_home(user) {
            tracing::warn!(user = user.get(), error = %e, "home creation failed; continuing without it");
        }

        let trimmed = vpath.trim_start_matches('/');
        let mut parts = trimmed.splitn(2, '/');
        let label = parts.next().unwrap_or("");
        let rest = parts.next().unwrap_or("");
        if label.is_empty() {
            return Err(CoreError::NotFound);
        }

        let roots = self.acl.roots(user);
        let root_entry = roots.iter().find(|r| r.label == label).ok_or(CoreError::NotFound)?;

        let share_entry = self.shares.get(&root_entry.share).ok_or(CoreError::NotFound)?;
        let max_depth = share_entry.def.policy.max_depth;
        let root = share_entry.root.clone();
        drop(share_entry);

        let rest_path = if rest.is_empty() {
            SafePath::root()
        } else {
            SafePath::parse(rest, max_depth)?
        };

        let mut full = root_entry.subpath.clone();
        for comp in rest_path.components() {
            full = full.join(comp.as_str(), max_depth)?;
        }

        let decision = self.acl.evaluate(user, root_entry.share, &full, want);
        if !decision.is_allowed() {
            let by = match decision {
                sc_acl::Decision::Denied { by } => by,
                sc_acl::Decision::Allowed { .. } => None,
            };
            return Err(CoreError::Denied { by });
        }

        let perms = self.acl.effective(user, root_entry.share, &full);
        Ok(Resolved {
            share: root_entry.share,
            root,
            path: full,
            perms,
        })
    }

    /// Resolves the per-share flags from the share table on the way out: the
    /// ACL engine has no share registry, so it leaves both `false`.
    pub fn roots(&self, user: UserId) -> Vec<sc_acl::RootEntry> {
        // One-time home creation (`FEATURES.md` #47) so a brand new user's
        // very first root listing already includes "Home" instead of only
        // showing it after some other call happens to trigger it first.
        if let Err(e) = self.ensure_home(user) {
            tracing::warn!(user = user.get(), error = %e, "home creation failed; continuing without it");
        }
        let mut roots = self.acl.roots(user);
        for r in &mut roots {
            let Some(def) = self.share_def(r.share) else { continue };
            r.shared_externally = def.shared_externally;
            r.trash_enabled = def.policy.trash != sc_vfs::TrashMode::Off;
        }
        roots
    }

    /// Which `ShareId` a virtual path's label maps to for `user`, without
    /// asking the ACL whether any particular permission is granted on the
    /// rest of the path.
    ///
    /// `resolve`/`resolve_want` above also derive the share this way as
    /// their very first step — `root_entry.share`, from `roots(user)` — the
    /// `evaluate()` call that follows only decides whether `want` is granted
    /// *within* that share, never which share a label names. This exposes
    /// that first step alone for a caller that only needs to answer "which
    /// share does this virtual path address", not "is this specific
    /// operation allowed on it": an app password's `Scope::shares`
    /// restriction has to be checked before it makes sense to ask the ACL a
    /// permission question at all, and doing that via `resolve`/
    /// `resolve_want` would wrongly require `Perms::READ` (or whatever
    /// `want` happens to be) just to learn the share — an assumption
    /// scope-checking has no business making about a token that might be
    /// scoped to, say, a write-only drop path.
    ///
    /// Still fails closed the same way `resolve_want` does for an unknown
    /// label: `CoreError::NotFound` if `user` has no root by that name at
    /// all. It does not, however, distinguish "no such share" from "a share
    /// exists but every grant on it is a deny" — callers that need that
    /// distinction (like `resolve_want`'s own 403-vs-404 DAV mapping) must
    /// still use `resolve`/`resolve_want`.
    pub fn resolve_share(&self, user: UserId, vpath: &str) -> Result<ShareId, CoreError> {
        let trimmed = vpath.trim_start_matches('/');
        let label = trimmed.split('/').next().unwrap_or("");
        if label.is_empty() {
            return Err(CoreError::NotFound);
        }
        // Same one-time home creation as `resolve_want` -- an app-password
        // scope check on a brand new user's first request must not 404
        // before `ensure_home` ever gets a chance to run.
        if let Err(e) = self.ensure_home(user) {
            tracing::warn!(user = user.get(), error = %e, "home creation failed; continuing without it");
        }
        self.acl
            .roots(user)
            .iter()
            .find(|r| r.label == label)
            .map(|r| r.share)
            .ok_or(CoreError::NotFound)
    }

    /// Whether `user` can read `path` within `share` — the ACL check
    /// `sc-search`'s walker needs to gate *descent*
    /// (`DESIGN-SEARCH.md` §3.2), exposed here so callers outside this crate
    /// never need `sc-acl`'s `AclEngine` directly.
    pub fn can_read(&self, user: UserId, share: ShareId, path: &SafePath) -> bool {
        self.acl.effective(user, share, path).contains(Perms::READ)
    }
}
