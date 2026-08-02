//! The depth-first evaluation algorithm itself,,
//! factored out so both the cached `AclEngine::evaluate` and the
//! lock-already-held `AclEngine::roots` can call it without re-acquiring
//! (and deadlocking on) the `RwLock`.

use sc_vfs::{GroupId, SafePath, ShareId, UserId};

use crate::{Decision, Grant, Inner, Perms, Principal, ALL_PERM_BITS};

/// `{User(user)} ∪ {Group(g) | g ∈ groups(user)}` — the set of principals a
/// grant can match against for this user.
pub(crate) fn principals_of(inner: &Inner, user: UserId) -> Vec<Principal> {
    let mut v = vec![Principal::User(user)];
    if let Some(groups) = inner.memberships.get(&user) {
        v.extend(groups.iter().map(|g: &GroupId| Principal::Group(*g)));
    }
    v
}

/// `subpath` denotes exactly `path` (used for non-inheriting grants).
/// `SafePath` derives `PartialEq`, but we go through `len()` + `is_prefix_of`
/// instead of relying on that so this only depends on methods explicitly
/// guaranteed by the contract this crate was written against.
fn subpath_equals(subpath: &SafePath, path: &SafePath) -> bool {
    subpath.len() == path.len() && subpath.is_prefix_of(path)
}

/// Depth-first evaluation of a (possibly multi-bit) `want` set against
/// `path`, per:
///
/// ```text
/// for depth in (path.len() .. 0).rev():
///     level = candidates where subpath.len() == depth
///     if level empty: continue
///     if any deny ∩ want ≠ ∅:     return Denied(that grant)
///     if any allow ⊇ want:        return Allowed(that grant)
///     if any allow ∩ want ≠ ∅:    want -= allow; continue
/// return Denied(default)
/// ```
///
/// One clarification beyond the literal pseudocode: if a partial match at
/// some depth reduces `want` all the way to empty, that's a full grant by
/// composition (e.g. a READ grant at `/a/b` plus a WRITE grant at `/a`
/// together satisfying `want = READ|WRITE`) and we return `Allowed`
/// immediately rather than continuing to search shallower depths for
/// nothing. Nothing here changes the outcome for a single-bit `want` (the
/// partial branch is a no-op there — `⊇` and `∩ ≠ ∅` coincide for a
/// singleton), which is what every exhaustiveness property in the test
/// suite and all of `effective()` actually exercise.
pub(crate) fn evaluate_locked(
    inner: &Inner,
    user: UserId,
    share: ShareId,
    path: &SafePath,
    mut want: Perms,
) -> Decision {
    let principals = principals_of(inner, user);

    let candidates: Vec<&Grant> = inner
        .grants
        .iter()
        .filter(|g| g.share == share && principals.contains(&g.principal))
        .filter(|g| {
            if g.inherit {
                g.subpath.is_prefix_of(path)
            } else {
                subpath_equals(&g.subpath, path)
            }
        })
        .collect();

    let max_depth = path.len();
    for depth in (0..=max_depth).rev() {
        let level = || candidates.iter().copied().filter(|g| g.subpath.len() == depth);

        if level().next().is_none() {
            continue;
        }

        if let Some(g) = level().find(|g| g.deny.intersects(want)) {
            return Decision::Denied { by: Some(g.id) };
        }
        if let Some(g) = level().find(|g| g.allow.contains(want)) {
            return Decision::Allowed { by: g.id };
        }
        if let Some(g) = level().find(|g| g.allow.intersects(want)) {
            want.remove(g.allow);
            if want.is_empty() {
                return Decision::Allowed { by: g.id };
            }
            continue;
        }
    }

    Decision::Denied { by: None }
}

/// `effective()` computed against an already-held read lock (used by
/// `roots()`, which holds the lock while iterating grants).
pub(crate) fn effective_locked(inner: &Inner, user: UserId, share: ShareId, path: &SafePath) -> Perms {
    ALL_PERM_BITS.iter().fold(Perms::empty(), |acc, &bit| {
        match evaluate_locked(inner, user, share, path, bit) {
            Decision::Allowed { .. } => acc | bit,
            Decision::Denied { .. } => acc,
        }
    })
}
