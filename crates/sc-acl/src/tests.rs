use std::collections::HashMap;

use sc_vfs::{GroupId, SafePath, ShareId, UserId};

use crate::{AclEngine, Decision, Grant, Perms, Principal};

fn sp(s: &str) -> SafePath {
    SafePath::parse(s, 64).unwrap()
}

fn grant(
    id: u32,
    principal: Principal,
    share: ShareId,
    subpath: &str,
    allow: Perms,
    deny: Perms,
    inherit: bool,
) -> Grant {
    Grant {
        id,
        principal,
        share,
        subpath: sp(subpath),
        allow,
        deny,
        inherit,
        label: None,
    }
}

fn assert_allowed(d: Decision, expect_by: u32) {
    match d {
        Decision::Allowed { by } => assert_eq!(by, expect_by),
        Decision::Denied { by } => panic!("expected Allowed{{by: {expect_by}}}, got Denied{{by: {by:?}}}"),
    }
}

fn assert_denied(d: Decision, expect_by: Option<u32>) {
    match d {
        Decision::Denied { by } => assert_eq!(by, expect_by),
        Decision::Allowed { by } => panic!("expected Denied{{by: {expect_by:?}}}, got Allowed{{by: {by}}}"),
    }
}

#[test]
fn default_deny_with_no_matching_grant() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);
    let d = engine.evaluate(user, share, &sp("a/b"), Perms::READ);
    assert_denied(d, None);
}

#[test]
fn deeper_allow_beats_shallower_deny() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![
        // Shallow: deny WRITE for the whole share.
        grant(1, Principal::User(user), share, "", Perms::empty(), Perms::WRITE, true),
        // Deep: explicitly allow WRITE under a/b.
        grant(2, Principal::User(user), share, "a/b", Perms::WRITE, Perms::empty(), true),
    ]);

    let d = engine.evaluate(user, share, &sp("a/b/c"), Perms::WRITE);
    assert_allowed(d, 2);

    // Outside the deep grant's subtree, the shallow deny still applies.
    let d2 = engine.evaluate(user, share, &sp("x/y"), Perms::WRITE);
    assert_denied(d2, Some(1));
}

#[test]
fn same_depth_deny_wins_over_allow() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let group = GroupId::new(10);
    let share = ShareId::new(1);

    engine.set_memberships(HashMap::from([(user, vec![group])]));
    engine.replace_grants(vec![
        grant(1, Principal::User(user), share, "a", Perms::READ | Perms::WRITE, Perms::empty(), true),
        grant(2, Principal::Group(group), share, "a", Perms::empty(), Perms::WRITE, true),
    ]);

    // Both grants are at depth 1 (subpath "a"). DENY wins regardless of
    // which one is the user grant and which is the group grant.
    let d = engine.evaluate(user, share, &sp("a/b"), Perms::WRITE);
    assert_denied(d, Some(2));

    // READ was never denied at this depth, so it's still allowed by grant 1.
    let d_read = engine.evaluate(user, share, &sp("a/b"), Perms::READ);
    assert_allowed(d_read, 1);
}

#[test]
fn group_vs_user_tie_deny_wins_no_matter_which_side_holds_it() {
    let share = ShareId::new(1);
    let user = UserId::new(1);
    let group = GroupId::new(10);

    // Case A: user allows, group denies (group's deny wins).
    let engine_a = AclEngine::new();
    engine_a.set_memberships(HashMap::from([(user, vec![group])]));
    engine_a.replace_grants(vec![
        grant(1, Principal::User(user), share, "a", Perms::DELETE, Perms::empty(), true),
        grant(2, Principal::Group(group), share, "a", Perms::empty(), Perms::DELETE, true),
    ]);
    assert_denied(engine_a.evaluate(user, share, &sp("a"), Perms::DELETE), Some(2));

    // Case B: group allows, user denies (user's deny wins) — symmetric,
    // proving there is no user > group priority, only "deny wins at this
    // depth" regardless of which principal kind holds the deny.
    let engine_b = AclEngine::new();
    engine_b.set_memberships(HashMap::from([(user, vec![group])]));
    engine_b.replace_grants(vec![
        grant(1, Principal::Group(group), share, "a", Perms::DELETE, Perms::empty(), true),
        grant(2, Principal::User(user), share, "a", Perms::empty(), Perms::DELETE, true),
    ]);
    assert_denied(engine_b.evaluate(user, share, &sp("a"), Perms::DELETE), Some(2));
}

#[test]
fn non_inheriting_grant_applies_only_to_its_exact_path() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![grant(
        1,
        Principal::User(user),
        share,
        "a/b",
        Perms::READ,
        Perms::empty(),
        false, // inherit = false
    )]);

    assert_allowed(engine.evaluate(user, share, &sp("a/b"), Perms::READ), 1);
    // Not a prefix match target since inherit is false: children are not covered.
    assert_denied(engine.evaluate(user, share, &sp("a/b/c"), Perms::READ), None);
}

#[test]
fn different_share_grants_do_not_leak() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share1 = ShareId::new(1);
    let share2 = ShareId::new(2);

    engine.replace_grants(vec![grant(
        1,
        Principal::User(user),
        share1,
        "",
        Perms::READ,
        Perms::empty(),
        true,
    )]);

    assert_allowed(engine.evaluate(user, share1, &sp("a"), Perms::READ), 1);
    assert_denied(engine.evaluate(user, share2, &sp("a"), Perms::READ), None);
}

#[test]
fn multi_bit_want_can_be_satisfied_by_composition_across_depths() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![
        // Shallow grant covers WRITE for the whole share.
        grant(1, Principal::User(user), share, "", Perms::WRITE, Perms::empty(), true),
        // Deeper grant covers only READ.
        grant(2, Principal::User(user), share, "a", Perms::READ, Perms::empty(), true),
    ]);

    // The deepest grant (2, at "a") only partially covers `want`: it grants
    // READ, chipping `want` down to just WRITE and moving on to shallower
    // depths rather than resolving there. Grant 1 (at the root) then fully
    // covers what's left via the ordinary "allow ⊇ want" branch, so it is
    // the one the decision is attributed to — composition across depths
    // still nets an overall Allowed, it's just not the deepest grant that
    // gets the credit when the *last* piece happens to be a full match.
    let d = engine.evaluate(user, share, &sp("a/b"), Perms::READ | Perms::WRITE);
    assert_allowed(d, 1);
}

#[test]
fn effective_aggregates_per_bit_decisions() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![
        grant(1, Principal::User(user), share, "a", Perms::READ | Perms::WRITE, Perms::empty(), true),
        grant(2, Principal::User(user), share, "a/b", Perms::empty(), Perms::WRITE, true),
    ]);

    let eff = engine.effective(user, share, &sp("a/b"));
    assert!(eff.contains(Perms::READ), "READ should still be allowed from the shallower grant");
    assert!(!eff.contains(Perms::WRITE), "WRITE is denied at the deeper, more specific level");
    assert!(!eff.contains(Perms::DELETE));
}

#[test]
fn generation_bump_invalidates_cached_decisions() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![grant(
        1,
        Principal::User(user),
        share,
        "",
        Perms::READ,
        Perms::empty(),
        true,
    )]);

    let gen0 = engine.generation();
    assert_allowed(engine.evaluate(user, share, &sp("a"), Perms::READ), 1);
    // Same call again: should hit the cache and still agree.
    assert_allowed(engine.evaluate(user, share, &sp("a"), Perms::READ), 1);

    // Revoke everything — generation must bump and the cached Allowed must
    // not leak through.
    engine.replace_grants(vec![]);
    assert!(engine.generation() > gen0);
    assert_denied(engine.evaluate(user, share, &sp("a"), Perms::READ), None);
}

#[test]
fn membership_change_bumps_generation_too() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let group = GroupId::new(5);
    let share = ShareId::new(1);

    engine.replace_grants(vec![grant(
        1,
        Principal::Group(group),
        share,
        "",
        Perms::READ,
        Perms::empty(),
        true,
    )]);

    // User isn't in the group yet.
    assert_denied(engine.evaluate(user, share, &sp("a"), Perms::READ), None);

    engine.set_memberships(HashMap::from([(user, vec![group])]));
    assert_allowed(engine.evaluate(user, share, &sp("a"), Perms::READ), 1);
}

#[test]
fn roots_projects_read_granted_paths_with_label_dedup() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    let mut g1 = grant(1, Principal::User(user), share, "photos", Perms::READ, Perms::empty(), true);
    g1.label = Some("Docs".into());
    let mut g2 = grant(2, Principal::User(user), share, "documents", Perms::READ, Perms::empty(), true);
    g2.label = Some("Docs".into());
    // No READ: must not appear in roots() at all.
    let g3 = grant(3, Principal::User(user), share, "private", Perms::WRITE, Perms::empty(), true);

    engine.replace_grants(vec![g1, g2, g3]);

    let roots = engine.roots(user);
    assert_eq!(roots.len(), 2);
    assert_eq!(roots[0].label, "Docs");
    assert_eq!(roots[1].label, "Docs (2)");
    assert!(roots.iter().all(|r| r.perms.contains(Perms::READ)));
}

#[test]
fn roots_falls_back_to_subpath_basename_when_unlabeled() {
    let engine = AclEngine::new();
    let user = UserId::new(1);
    let share = ShareId::new(1);

    engine.replace_grants(vec![grant(
        1,
        Principal::User(user),
        share,
        "a/vacation",
        Perms::READ,
        Perms::empty(),
        true,
    )]);

    let roots = engine.roots(user);
    assert_eq!(roots.len(), 1);
    assert_eq!(roots[0].label, "vacation");
}

/// A small decision table exercising grant combinations x path depths x
/// permission bits in one place, covering the properties `DESIGN-CORE.md`
/// §3.2 calls out explicitly.
#[test]
fn exhaustive_depth_and_bit_table() {
    let share = ShareId::new(1);
    let user = UserId::new(1);

    struct Case {
        grants: Vec<Grant>,
        path: &'static str,
        want: Perms,
        expect_allowed_by: Option<u32>, // None means expect Denied
    }

    let cases = vec![
        // No grants at all -> default deny.
        Case {
            grants: vec![],
            path: "a",
            want: Perms::READ,
            expect_allowed_by: None,
        },
        // Root-level allow covers a deep path via inheritance.
        Case {
            grants: vec![grant(1, Principal::User(user), share, "", Perms::READ, Perms::empty(), true)],
            path: "a/b/c/d",
            want: Perms::READ,
            expect_allowed_by: Some(1),
        },
        // Root allow, but requested bit isn't granted anywhere -> deny.
        Case {
            grants: vec![grant(1, Principal::User(user), share, "", Perms::READ, Perms::empty(), true)],
            path: "a",
            want: Perms::DELETE,
            expect_allowed_by: None,
        },
        // Deny at depth 2 shadows an allow at depth 0 for that specific bit.
        Case {
            grants: vec![
                grant(1, Principal::User(user), share, "", Perms::READ | Perms::DELETE, Perms::empty(), true),
                grant(2, Principal::User(user), share, "a/b", Perms::empty(), Perms::DELETE, true),
            ],
            path: "a/b/c",
            want: Perms::DELETE,
            expect_allowed_by: None,
        },
        // Same setup, but READ (not denied anywhere) still resolves from the root grant.
        Case {
            grants: vec![
                grant(1, Principal::User(user), share, "", Perms::READ | Perms::DELETE, Perms::empty(), true),
                grant(2, Principal::User(user), share, "a/b", Perms::empty(), Perms::DELETE, true),
            ],
            path: "a/b/c",
            want: Perms::READ,
            expect_allowed_by: Some(1),
        },
        // A grant at exactly the queried path (depth == path.len()).
        Case {
            grants: vec![grant(1, Principal::User(user), share, "a/b", Perms::WRITE, Perms::empty(), true)],
            path: "a/b",
            want: Perms::WRITE,
            expect_allowed_by: Some(1),
        },
    ];

    for (i, case) in cases.into_iter().enumerate() {
        let engine = AclEngine::new();
        engine.replace_grants(case.grants);
        let decision = engine.evaluate(user, share, &sp(case.path), case.want);
        match (decision, case.expect_allowed_by) {
            (Decision::Allowed { by }, Some(expect)) => {
                assert_eq!(by, expect, "case {i}: wrong grant id")
            }
            (Decision::Denied { .. }, None) => {}
            (other, expect) => panic!("case {i}: got {other:?}, expected allowed_by={expect:?}"),
        }
    }
}

/// `denies_below` reports the depth-varying decisions a flat share ACL cannot
/// carry — what SMB has to warn about instead of silently widening.
#[test]
fn denies_below_finds_denials_inside_a_granted_tree() {
    let e = AclEngine::new();
    let u = UserId::new(7);
    let share = ShareId::new(1);
    e.replace_grants(vec![
        grant(1, Principal::User(u), share, "", Perms::READ | Perms::WRITE, Perms::empty(), true),
        // Inside the granted tree: SMB exports the whole thing as one share.
        grant(2, Principal::User(u), share, "private", Perms::empty(), Perms::READ, true),
        // A narrower *allow* deeper down takes nothing away, so it is not one.
        grant(3, Principal::User(u), share, "photos", Perms::READ, Perms::empty(), true),
        // Another user's deny is not this user's problem.
        grant(4, Principal::User(UserId::new(8)), share, "other", Perms::empty(), Perms::READ, true),
        // A different share is a different export.
        grant(5, Principal::User(u), ShareId::new(2), "elsewhere", Perms::empty(), Perms::READ, true),
    ]);

    assert_eq!(e.denies_below(u, share, &sp("")), vec!["private".to_string()]);
    // The root of a deny is not "below" itself — that one Samba *can* express,
    // as its own share section.
    assert!(e.denies_below(u, share, &sp("private")).is_empty());
    assert!(e.denies_below(UserId::new(9), share, &sp("")).is_empty());
}

/// Group grants reach a member, so a deny carried by a group has to surface
/// for that member too.
#[test]
fn denies_below_follows_group_membership() {
    let e = AclEngine::new();
    let u = UserId::new(7);
    let g = GroupId::new(3);
    let share = ShareId::new(1);
    e.replace_grants(vec![
        grant(1, Principal::User(u), share, "", Perms::READ, Perms::empty(), true),
        grant(2, Principal::Group(g), share, "vault", Perms::empty(), Perms::READ, true),
    ]);

    assert!(e.denies_below(u, share, &sp("")).is_empty(), "not a member yet");

    e.set_memberships(HashMap::from([(u, vec![g])]));
    assert_eq!(e.denies_below(u, share, &sp("")), vec!["vault".to_string()]);
}
