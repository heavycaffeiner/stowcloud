//! `sc-acl` — Share/Grant definitions and permission evaluation.
//!
//! The evaluation algorithm is, implemented exactly:
//! depth-first from the deepest matching grant down to the share root,
//! same-depth `DENY` beats `ALLOW`, a deeper `ALLOW` beats a shallower
//! `DENY`, no principal-kind priority (user vs. group ties are broken by
//! depth only, never by which kind of principal it is), default deny.
//!
//! `evaluate()` resolves a single `want` set atomically (all-or-nothing:
//! partial coverage at one depth chips away at `want` and keeps searching
//! shallower depths for the remainder — see `engine::evaluate_locked` for
//! the one place this needed a decision beyond the literal pseudocode).
//! `effective()` is built on top of it by asking the single-bit question for
//! each of the eight permission bits and OR-ing together the ones that come
//! back `Allowed` — for a singleton `want` the partial-coverage branch never
//! fires, so this reduces exactly to "the deepest applicable rule for this
//! one bit wins" per bit, which is the well-defined case.

mod engine;

#[cfg(test)]
mod tests;

use std::collections::HashMap;
use std::num::NonZeroUsize;
use std::sync::atomic::{AtomicU64, Ordering};

use sc_vfs::{GroupId, SafePath, ShareId, UserId};
use parking_lot::{Mutex, RwLock};

bitflags::bitflags! {
    #[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
    pub struct Perms: u16 {
        const READ     = 1 << 0; // list + download
        const WRITE    = 1 << 1; // overwrite existing file content
        const CREATE   = 1 << 2; // new file/directory
        const DELETE   = 1 << 3;
        const RENAME   = 1 << 4; // rename within the same directory
        const MOVE     = 1 << 5; // move to a different directory
        const SHARE    = 1 << 6; // create a share link
        const DOWNLOAD = 1 << 7; // off = preview/stream only
    }
}

/// The eight bits `effective()` probes individually. Order doesn't matter —
/// it's just an OR reduction — but it's fixed so results are deterministic.
const ALL_PERM_BITS: [Perms; 8] = [
    Perms::READ,
    Perms::WRITE,
    Perms::CREATE,
    Perms::DELETE,
    Perms::RENAME,
    Perms::MOVE,
    Perms::SHARE,
    Perms::DOWNLOAD,
];

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub enum Principal {
    User(UserId),
    Group(GroupId),
}

#[derive(Clone, Debug)]
pub struct Grant {
    pub id: u32,
    pub principal: Principal,
    pub share: ShareId,
    pub subpath: SafePath,
    pub allow: Perms,
    pub deny: Perms,
    /// `false` means this grant applies to exactly `subpath`, not anything
    /// beneath it.
    pub inherit: bool,
    /// Virtual-root display label. Falls back to
    /// `subpath`'s basename, then to a share-derived placeholder.
    pub label: Option<String>,
}

#[derive(Clone, Debug)]
pub enum Decision {
    Allowed { by: u32 },
    Denied { by: Option<u32> },
}

impl Decision {
    /// Convenience predicate for call sites that only branch on the outcome.
    ///
    /// The `by` grant id is deliberately still carried by the variant even
    /// when unused here: every denial has to stay *explainable* in the API
    /// response and the audit log, so this must never
    /// become the only way the result is inspected.
    #[inline]
    pub fn is_allowed(&self) -> bool {
        matches!(self, Decision::Allowed { .. })
    }

    #[inline]
    pub fn is_denied(&self) -> bool {
        !self.is_allowed()
    }

    /// The grant that decided this outcome, if one did. `None` means the
    /// default-deny fallthrough — i.e. nothing matched at all.
    #[inline]
    pub fn by(&self) -> Option<u32> {
        match *self {
            Decision::Allowed { by } => Some(by),
            Decision::Denied { by } => by,
        }
    }
}

#[derive(Clone, Debug)]
pub struct RootEntry {
    pub label: String,
    pub share: ShareId,
    pub subpath: SafePath,
    pub perms: Perms,
    /// Mirrors `ShareDef::shared_externally`, resolved by the caller that owns
    /// the share table (`Core::roots`) — the ACL engine has no share registry
    /// of its own. Carried here because `GET /api/auth/session` used to
    /// hardcode `false`, which left the "shared with another service" badge
    /// (`FEATURES.md` #133) permanently invisible.
    pub shared_externally: bool,
    /// Whether this share keeps deleted items. Filled in by `Core::roots` for
    /// the same reason as `shared_externally`, and carried to the client so a
    /// delete confirmation can say whether the delete is undoable.
    pub trash_enabled: bool,
}

pub(crate) struct Inner {
    pub(crate) grants: Vec<Grant>,
    pub(crate) memberships: HashMap<UserId, Vec<GroupId>>,
}

#[derive(Clone, PartialEq, Eq, Hash)]
struct CacheKey {
    user: UserId,
    share: ShareId,
    path: String,
    want: u16,
}

struct CacheEntry {
    gen: u64,
    decision: Decision,
}

/// Grants + group memberships behind a generation counter, with an LRU
/// decision cache invalidated by that counter.
pub struct AclEngine {
    inner: RwLock<Inner>,
    generation: AtomicU64,
    cache: Mutex<lru::LruCache<CacheKey, CacheEntry>>,
}

impl Default for AclEngine {
    fn default() -> Self {
        Self::new()
    }
}

impl AclEngine {
    pub fn new() -> Self {
        Self {
            inner: RwLock::new(Inner {
                grants: Vec::new(),
                memberships: HashMap::new(),
            }),
            generation: AtomicU64::new(0),
            cache: Mutex::new(lru::LruCache::new(NonZeroUsize::new(4096).unwrap())),
        }
    }

    /// Wholesale-replace the grant list (e.g. after an admin edits Shares).
    /// Bumps the generation, which lazily invalidates every cached decision
    /// — no explicit cache clear needed.
    pub fn replace_grants(&self, grants: Vec<Grant>) {
        {
            let mut inner = self.inner.write();
            inner.grants = grants;
        }
        self.generation.fetch_add(1, Ordering::SeqCst);
    }

    pub fn set_memberships(&self, m: HashMap<UserId, Vec<GroupId>>) {
        {
            let mut inner = self.inner.write();
            inner.memberships = m;
        }
        self.generation.fetch_add(1, Ordering::SeqCst);
    }

    pub fn generation(&self) -> u64 {
        self.generation.load(Ordering::SeqCst)
    }

    /// Evaluate whether `user` has *all* of `want` at `path`, using the
    /// depth-first algorithm in `engine::evaluate_locked`. Cached on
    /// `(user, share, path, want)`, invalidated by generation mismatch.
    pub fn evaluate(&self, user: UserId, share: ShareId, path: &SafePath, want: Perms) -> Decision {
        let gen = self.generation();
        let key = CacheKey {
            user,
            share,
            path: path.to_display_string(),
            want: want.bits(),
        };

        if let Some(entry) = self.cache.lock().get(&key) {
            if entry.gen == gen {
                return entry.decision.clone();
            }
        }

        let inner = self.inner.read();
        let decision = engine::evaluate_locked(&inner, user, share, path, want);
        drop(inner);

        self.cache.lock().put(
            key,
            CacheEntry {
                gen,
                decision: decision.clone(),
            },
        );
        decision
    }

    /// The maximal set of permission bits `user` holds at `path`: the OR of
    /// every bit for which the single-bit `evaluate()` call returns
    /// `Allowed`. Goes through the same cache as `evaluate()`.
    pub fn effective(&self, user: UserId, share: ShareId, path: &SafePath) -> Perms {
        ALL_PERM_BITS.iter().fold(Perms::empty(), |acc, &bit| {
            match self.evaluate(user, share, path, bit) {
                Decision::Allowed { .. } => acc | bit,
                Decision::Denied { .. } => acc,
            }
        })
    }

    /// Subpaths strictly below `root` where a grant denies `user` something
    /// the root itself allows.
    ///
    /// A depth-varying decision inside one tree, which is exactly what a flat
    /// share ACL cannot express: an exporter that publishes `root` as a
    /// single unit (SMB — `smb.conf` has no per-path ACL) hands out access
    /// this engine would refuse. That exporter needs to say so rather than
    /// quietly widen the grant, so this reports the paths to name.
    ///
    /// Only denies count. A *narrower allow* deeper in the tree takes nothing
    /// away — allows compose upward — so it cannot make the flattened view
    /// too permissive.
    pub fn denies_below(&self, user: UserId, share: ShareId, root: &SafePath) -> Vec<String> {
        let inner = self.inner.read();
        let principals = engine::principals_of(&inner, user);
        let mut out: Vec<String> = inner
            .grants
            .iter()
            .filter(|g| {
                g.share == share
                    && principals.contains(&g.principal)
                    && !g.deny.is_empty()
                    && root.is_prefix_of(&g.subpath)
                    && g.subpath.len() > root.len()
            })
            // `SafePath` has no `Display`; its components are already
            // validated, so joining them is the whole rendering.
            .map(|g| {
                g.subpath
                    .components()
                    .iter()
                    .map(|c| c.as_str())
                    .collect::<Vec<_>>()
                    .join("/")
            })
            .collect();
        out.sort();
        out.dedup();
        out
    }

    /// The virtual root projection for `user`: one
    /// entry per READ-granted rule the user (directly or via group
    /// membership) holds, labeled by `Grant::label`, falling back to the
    /// subpath's basename, falling back to a share-derived placeholder.
    /// Label collisions get a `" (2)"`, `" (3)"`, ... suffix in encounter
    /// order.
    pub fn roots(&self, user: UserId) -> Vec<RootEntry> {
        let inner = self.inner.read();
        let principals = engine::principals_of(&inner, user);

        let mut seen: HashMap<String, u32> = HashMap::new();
        let mut result = Vec::new();

        for g in inner
            .grants
            .iter()
            .filter(|g| principals.contains(&g.principal) && g.allow.contains(Perms::READ))
        {
            let base = g
                .label
                .clone()
                .or_else(|| g.subpath.name().map(|n| n.to_string()))
                .unwrap_or_else(|| format!("share-{}", g.share.get()));

            let count = seen.entry(base.clone()).or_insert(0);
            *count += 1;
            let label = if *count == 1 {
                base
            } else {
                format!("{base} ({count})")
            };

            let perms = engine::effective_locked(&inner, user, g.share, &g.subpath);
            result.push(RootEntry {
                label,
                share: g.share,
                subpath: g.subpath.clone(),
                perms,
                // The engine cannot know either; `Core::roots` fills them in.
                shared_externally: false,
                trash_enabled: false,
            });
        }

        result
    }
}
