//! Per-share trash: `<share>/.sctrash/{id}-{encoded_orig_path}`
//! `TrashMode::ShareLocal`. `TrashMode::Off` —
//! the default for every share — is handled directly in `ops::delete` as a
//! plain unlink; this module only runs when a share has trash turned on.
//! An admin toggles it per share (`Core::update_share`'s `trash_enabled`,
//! persisted in `shares.db` — see `share.rs`), not through `config.toml`.
//!
//! `.sctrash` is *flat* — one level, not a mirror of the share's directory
//! tree — so a trashed file's original parent directory has nowhere to live
//! except folded into its own entry name. Earlier this crate kept only
//! `r.path.name()` (the basename) there, which silently discarded the
//! parent: restoring a file trashed from `docs/2024/report.pdf` dropped it
//! at the share root as `report.pdf` with no error and no indication
//! anything had moved. Confirmed by checking the actual on-disk state after
//! a delete+restore round trip, not by reading the code. `encode_orig_path`/
//! `decode_orig_path` below fix that by carrying the *full* relative path
//! through the flat entry name instead of just the leaf.

use sc_acl::Perms;
use sc_vfs::{SafePath, ShareId, ShareRoot, Stat, UserId};

use crate::entry::TrashEntry;
use crate::error::CoreError;
use crate::ops::path_exists;
use crate::resolve::Resolved;

pub(crate) const TRASH_DIR: &str = ".sctrash";

/// Fold a share-relative path into a single filesystem-safe path component.
/// Base64url (no padding) is used rather than the path's own `/`-joined
/// form because `/` inside a *single* path component would be reinterpreted
/// as a separator by `SafePath::join`/the OS — exactly the character this
/// needs to survive intact. The alphabet (`[A-Za-z0-9_-]`) also can't
/// collide with any of `validate_component`'s other rejections (no NUL/
/// control bytes, no `:`, never ends in `.`/` `), so the encoded name always
/// passes the same component validation every other on-disk name does.
fn encode_orig_path(path: &SafePath) -> String {
    data_encoding::BASE64URL_NOPAD.encode(path.to_display_string().as_bytes())
}

/// Inverse of [`encode_orig_path`]. Returns `None` on anything that doesn't
/// decode to a valid `SafePath` — in particular, a pre-fix trash entry whose
/// suffix is a literal basename (not base64) rather than an encoded path.
/// Callers treat that as the legacy shape, not an error: an entry trashed
/// before this fix shipped has no recorded parent directory to recover
/// (it was never written down), so the only honest fallback is the old
/// behavior, restore-to-root, not a hard failure on an otherwise-valid trash
/// entry.
fn decode_orig_path(encoded: &str, max_depth: u16) -> Option<SafePath> {
    let bytes = data_encoding::BASE64URL_NOPAD.decode(encoded.as_bytes()).ok()?;
    let s = String::from_utf8(bytes).ok()?;
    SafePath::parse(&s, max_depth).ok()
}

impl crate::Core {
    pub(crate) fn trash_move(&self, r: &Resolved, _st: &Stat) -> Result<(), CoreError> {
        let max_depth = r.root.policy().max_depth;
        let trash_dir = SafePath::control(TRASH_DIR, max_depth)?;
        if !path_exists(&r.root, &trash_dir)? {
            r.root.mkdir(&trash_dir)?;
        }

        let id = uuid::Uuid::new_v4().simple().to_string();
        // Whole path, not just `r.path.name()` -- see the module doc comment
        // for why the basename alone lost the file's location on restore.
        let encoded = encode_orig_path(&r.path);
        let trash_name = format!("{id}-{encoded}");
        let trash_path = trash_dir.join(&trash_name, max_depth)?;
        r.root.rename(&r.path, &trash_path, true)?;
        Ok(())
    }

    fn split_trash_name(name: &str) -> Option<(&str, &str)> {
        // `id` is `Uuid::simple()` -- 32 lowercase hex chars, never contains
        // `-` -- so splitting on the *first* `-` always separates the id
        // from the (base64url-encoded, itself possibly `-`-containing)
        // remainder correctly, regardless of trash-entry format version.
        name.split_once('-')
    }

    /// Best-effort display name for a trash entry: the leaf of the decoded
    /// original path, or (legacy entries / anything undecodable) the raw
    /// suffix as-is, which pre-fix was already exactly the basename.
    fn trash_display_name(rest: &str, max_depth: u16) -> String {
        match decode_orig_path(rest, max_depth) {
            Some(p) => p.name().unwrap_or(rest).to_string(),
            None => rest.to_string(),
        }
    }

    /// Create every ancestor of `dir` that doesn't already exist, shallowest
    /// first -- `ShareRoot::mkdir` is one level at a time, there is no
    /// recursive `mkdir -p` in that API. Used by `trash_restore` to recreate
    /// a directory that no longer exists (deleted itself, independently of
    /// the file now being restored into it) so the file can go back to
    /// exactly the path it was trashed from.
    ///
    /// Recreating is the chosen behavior over the two alternatives the task
    /// considered: dropping the file at the share root (the original bug —
    /// silent, and exactly what this fix removes) or at the nearest
    /// surviving ancestor (also silent, and now the *file's* name is right
    /// but its parent directory is wrong in a way the response never
    /// mentions). An empty recreated directory is the one outcome a user
    /// restoring a file already expects — it is what "put it back" means —
    /// and it costs nothing extra if the directory turns out to already
    /// exist.
    fn ensure_dir_recursive(root: &ShareRoot, dir: &SafePath, max_depth: u16) -> Result<(), CoreError> {
        let mut built = SafePath::root();
        for comp in dir.components() {
            built = built.join(comp.as_str(), max_depth)?;
            if path_exists(root, &built)? {
                continue;
            }
            match root.mkdir(&built) {
                Ok(()) => {}
                // Lost a race with something else creating the same
                // ancestor (another restore into a sibling path, a fresh
                // upload, ...) -- the directory existing is the only thing
                // that mattered.
                Err(sc_vfs::VfsError::AlreadyExists) => {}
                Err(e) => return Err(e.into()),
            }
        }
        Ok(())
    }

    pub fn trash_list(&self, user: UserId, share: ShareId) -> Result<Vec<TrashEntry>, CoreError> {
        let root = self.share(share).ok_or(CoreError::NotFound)?;
        let decision = self.acl.evaluate(user, share, &SafePath::root(), Perms::READ);
        if !decision.is_allowed() {
            return Err(CoreError::Denied { by: decision.by() });
        }

        let max_depth = root.policy().max_depth;
        let trash_dir = SafePath::control(TRASH_DIR, max_depth)?;
        let entries = root.read_dir(&trash_dir).unwrap_or_default();
        let mut out = Vec::new();
        for e in entries {
            let Some((id, rest)) = Self::split_trash_name(&e.name) else { continue };
            let Ok(p) = trash_dir.join(&e.name, max_depth) else { continue };
            if let Ok(st) = root.stat(&p) {
                out.push(TrashEntry {
                    id: id.to_string(),
                    name: Self::trash_display_name(rest, max_depth),
                    size: st.size,
                    deleted_mtime_ns: st.mtime_ns,
                });
            }
        }
        Ok(out)
    }

    pub fn trash_restore(&self, user: UserId, share: ShareId, id: &str) -> Result<(), CoreError> {
        let root = self.share(share).ok_or(CoreError::NotFound)?;
        let decision = self.acl.evaluate(user, share, &SafePath::root(), Perms::CREATE);
        if !decision.is_allowed() {
            return Err(CoreError::Denied { by: decision.by() });
        }

        let max_depth = root.policy().max_depth;
        let trash_dir = SafePath::control(TRASH_DIR, max_depth)?;
        let entries = root.read_dir(&trash_dir)?;
        let prefix = format!("{id}-");
        let entry_name = entries
            .into_iter()
            .map(|e| e.name.to_string())
            .find(|n| n.starts_with(&prefix))
            .ok_or(CoreError::NotFound)?;
        let rest = Self::split_trash_name(&entry_name)
            .map(|(_, n)| n)
            .ok_or_else(|| CoreError::Internal("malformed trash entry".into()))?;

        // The path this entry was trashed from, recovered whole
        // (`decode_orig_path`) -- or, for a pre-fix entry that has no
        // recorded parent, the legacy fallback of treating `rest` as a bare
        // basename and restoring to the share root exactly as this used to
        // unconditionally do. That fallback is the *documented* old
        // behavior applied only where the new information doesn't exist,
        // not a silent downgrade of a fixed entry.
        let dest_path = match decode_orig_path(rest, max_depth) {
            Some(p) => p,
            None => SafePath::parse(rest, max_depth)?,
        };

        let trash_path = trash_dir.join(&entry_name, max_depth)?;

        // The original directory may itself have been deleted (independent
        // of the file being restored into it) since the file was trashed.
        // Recreate the whole ancestor chain rather than dropping the file
        // somewhere else silently -- see `ensure_dir_recursive`'s doc
        // comment for why recreation was chosen over the alternatives.
        Self::ensure_dir_recursive(&root, &dest_path.parent(), max_depth)?;

        if path_exists(&root, &dest_path)? {
            return Err(CoreError::Conflict);
        }
        root.rename(&trash_path, &dest_path, true)?;
        self.mark_dirty(share, &dest_path);
        Ok(())
    }

    pub fn trash_purge(&self, user: UserId, share: ShareId, id: Option<&str>) -> Result<(), CoreError> {
        let root = self.share(share).ok_or(CoreError::NotFound)?;
        let decision = self.acl.evaluate(user, share, &SafePath::root(), Perms::DELETE);
        if !decision.is_allowed() {
            return Err(CoreError::Denied { by: decision.by() });
        }

        let max_depth = root.policy().max_depth;
        let trash_dir = SafePath::control(TRASH_DIR, max_depth)?;
        let entries = root.read_dir(&trash_dir).unwrap_or_default();
        for e in entries {
            let matches = match id {
                Some(id) => e.name.starts_with(&format!("{id}-")),
                None => true,
            };
            if !matches {
                continue;
            }
            let p = trash_dir.join(&e.name, max_depth)?;
            // Quota charge-back: purge is where trashed
            // bytes are actually freed (`trash_move` only relocated them, no
            // charge there). Size must be read before the delete.
            let freed = if e.kind == sc_vfs::Kind::Dir { self.aggregate(share, &p)?.rsize } else { root.stat(&p).map(|st| st.size).unwrap_or(0) };
            if e.kind == sc_vfs::Kind::Dir {
                Self::delete_recursive(&root, &p)?;
            } else {
                root.unlink(&p)?;
            }
            self.charge_quota(user, -(freed as i64));
        }
        Ok(())
    }
}
