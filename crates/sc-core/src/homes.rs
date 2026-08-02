//! Per-user home directories (`FEATURES.md` #47): off by default
//! (`sc-server::config::HomesConfig::enabled`). Not a new resolution
//! mechanism — the same grant-projected virtual root every other share
//! already uses (`resolve.rs`'s doc comment: "the label -> (ShareRoot,
//! SafePath) conversion happens in exactly one place"). One shared
//! `ShareRoot` (`HOME_SHARE_ID`) is opened once at startup under
//! `homes.root`; the first time any given user is resolved or lists their
//! roots, [`Core::ensure_home`] creates `{homes.root}/{user id}` (idempotent
//! — `VfsError::AlreadyExists` is not an error here), seeded from
//! `{homes.root}/.template` when that directory exists (`FEATURES.md` #47,
//! "creates from a template when enabled"; an empty directory otherwise —
//! see [`TEMPLATE_DIR_NAME`]), and persists exactly one grant scoping that
//! user to that one subpath, full permissions, label "Home".
//!
//! Directory name is the numeric `UserId`, not the username: `sc-core` has
//! no notion of usernames anywhere else (`sc-acl`/`sc-vfs`/`sc-meta` all
//! address a person by opaque `UserId` only), and the host directory name
//! is never shown to a client anyway — the virtual root label is
//! (`FEATURES.md` #46, "host paths fully hidden").
//!
//! Containment: the per-user subdirectory is created via `ShareRoot::mkdir`
//! (`openat2(RESOLVE_BENEATH)`, same as every other directory creation in
//! this crate), and once created is resolved exactly like any other
//! grant's subpath — same `SafePath` containment, same symlink policy
//! (`Deny`, least privilege), same ACL evaluation. A user's grant is scoped
//! to *their own* subpath only, so `evaluate()` denies by default the
//! instant a path steps outside it — no new isolation mechanism, one more
//! grant. `Core::seed_full_access` (`acl_store.rs`) explicitly skips
//! [`HOME_SHARE_ID`] for this reason: a root grant on the whole homes share
//! would hand whoever received it every other user's home at once.

use std::path::Path;

use sc_acl::{Perms, Principal};
use sc_vfs::{Kind, SafePath, ShareId, UserId, VfsError};

use crate::acl_store::GrantSpec;
use crate::error::CoreError;
use crate::share::ShareDef;

/// Reserved id for the single homes `ShareRoot`. Below
/// [`crate::DYNAMIC_SHARE_ID_BASE`] (never collides with an admin-created
/// share) and far above any realistic `config.toml` share count (never
/// collides with one of those either).
pub(crate) const HOME_SHARE_ID: ShareId = ShareId::new(999_999);

pub(crate) const HOME_LABEL: &str = "Home";

/// Optional template directory (`FEATURES.md` #47, "creates from a template
/// when enabled"), a sibling of the per-user directories directly under
/// `homes.root`. Convention over config: an admin who wants new homes
/// pre-populated drops files under `{homes.root}/.template`; one that
/// doesn't gets today's behavior (an empty home) with no config key to set.
/// Never reachable as a user's own home directory name: home directories
/// are named after a numeric `UserId` (see the module doc), and this name
/// is neither a valid `UserId` nor exposed through any grant, so it can
/// never collide with, or be confused for, a real user's home.
const TEMPLATE_DIR_NAME: &str = ".template";

impl crate::Core {
    /// Enable per-user homes: opens `host_path` as the shared homes root,
    /// creating it first if missing (unlike an admin-registered share, this
    /// directory is entirely managed by this process, not a pre-existing
    /// admin-chosen location). Idempotent-by-refusal, same contract as the
    /// other optional-attach points (`attach_acl_store`, ...) — called at
    /// most once, from `sc-server::app::App::build`, only when
    /// `homes.enabled`.
    pub fn attach_homes(&self, host_path: &Path) -> anyhow::Result<()> {
        if self.shares.contains_key(&HOME_SHARE_ID) {
            anyhow::bail!("homes already attached");
        }
        std::fs::create_dir_all(host_path)
            .map_err(|e| anyhow::anyhow!("creating homes root {}: {e}", host_path.display()))?;
        let def = ShareDef {
            id: HOME_SHARE_ID,
            name: "Home".to_string(),
            host_path: host_path.to_path_buf(),
            policy: sc_vfs::SharePolicy::default(),
            shared_externally: false,
        };
        self.register_share(def)
    }

    pub fn homes_enabled(&self) -> bool {
        self.shares.contains_key(&HOME_SHARE_ID)
    }

    /// Idempotent: create `user`'s home directory and grant if they don't
    /// have one yet, otherwise an immediate no-op (one cheap in-memory
    /// `roots()` scan, no filesystem or database access). Best-effort from
    /// every call site (`resolve_want`/`roots`) — a home hiccup (full disk,
    /// no grant store attached) must not break a user's access to their
    /// other, already-working shares, so callers log and continue rather
    /// than propagate this error.
    pub(crate) fn ensure_home(&self, user: UserId) -> Result<(), CoreError> {
        let Some(entry) = self.shares.get(&HOME_SHARE_ID) else {
            return Ok(()); // homes disabled
        };
        let root = entry.root.clone();
        drop(entry); // release the DashMap shard lock before any slower work

        if self.acl.roots(user).iter().any(|r| r.share == HOME_SHARE_ID) {
            return Ok(());
        }
        // Below here only runs once per user, ever. Serialized so two
        // concurrent first-accesses can't both lose the check above and
        // mint two grants, which `AclEngine::roots`'s label-dedup would
        // then render as "Home" and "Home (2)".
        let _guard = self.home_lock.lock();
        if self.acl.roots(user).iter().any(|r| r.share == HOME_SHARE_ID) {
            return Ok(());
        }

        let max_depth = root.policy().max_depth;
        let subpath = SafePath::parse(&user.get().to_string(), max_depth)?;
        let template_path = SafePath::parse(TEMPLATE_DIR_NAME, max_depth)?;
        let has_template = matches!(root.stat(&template_path), Ok(st) if st.kind == Kind::Dir);
        if has_template {
            // `copy_recursive` (`ops.rs`) creates `subpath` itself (only if
            // missing) and then copies the template tree into it -- same
            // no-clobber behavior as a plain `mkdir`, plus content.
            self.copy_recursive(&root, &template_path, &root, &subpath)?;
        } else {
            match root.mkdir(&subpath) {
                Ok(()) => {}
                // Lost a race with another process/thread creating the same
                // directory -- its existing is all that mattered (same
                // tolerance `trash.rs::ensure_dir_recursive` already applies).
                Err(VfsError::AlreadyExists) => {}
                Err(e) => return Err(e.into()),
            }
        }

        self.create_grant(&GrantSpec {
            principal: Principal::User(user),
            share: HOME_SHARE_ID,
            subpath: user.get().to_string(),
            allow: Perms::all(),
            deny: Perms::empty(),
            inherit: true,
            label: Some(HOME_LABEL.to_string()),
        })?;
        Ok(())
    }
}
