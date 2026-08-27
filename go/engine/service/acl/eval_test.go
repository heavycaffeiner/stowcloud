package acl

import "testing"

// at builds the vpath a test asks about.
func at(share int64, path string) Vpath {
	return Vpath{Share: share, Path: ParsePath(path)}
}

// loaded returns an evaluator holding the given grants and no memberships.
func loaded(t *testing.T, grants ...Grant) *Evaluator {
	t.Helper()
	e := NewEvaluator()
	if err := e.LoadFromState(grants, nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	return e
}

// userGrant is an inheriting grant for one user at one subpath.
func userGrant(id, user int64, subpath string, allow, deny Perms) Grant {
	return Grant{
		ID:      id,
		User:    user,
		Share:   1,
		Subpath: ParsePath(subpath),
		Allow:   allow,
		Deny:    deny,
		Inherit: true,
	}
}

func wantDecision(t *testing.T, got Decision, allowed bool, by int64) {
	t.Helper()
	if got.Allowed != allowed || got.By != by {
		t.Fatalf("decision %+v, want allowed=%v by=%d", got, allowed, by)
	}
}

// Test 1. A DENY and an ALLOW of the same bit at the same depth: DENY wins
// regardless of which comes first in the table.
func TestSameDepthDenyBeatsAllow(t *testing.T) {
	denyFirst := loaded(t,
		userGrant(10, 7, "a", 0, Read),
		userGrant(11, 7, "a", Read, 0),
	)
	wantDecision(t, denyFirst.Evaluate(7, at(1, "a"), Read), false, 10)

	allowFirst := loaded(t,
		userGrant(11, 7, "a", Read, 0),
		userGrant(10, 7, "a", 0, Read),
	)
	wantDecision(t, allowFirst.Evaluate(7, at(1, "a"), Read), false, 10)
}

// Test 2. An ALLOW at depth 2 and a DENY of the same bit at depth 1: the
// deeper ALLOW answers first and the shallower DENY is never reached.
func TestDeeperAllowBeatsShallowerDeny(t *testing.T) {
	e := loaded(t,
		userGrant(20, 7, "a", 0, Write),
		userGrant(21, 7, "a/b", Write, 0),
	)
	wantDecision(t, e.Evaluate(7, at(1, "a/b/c"), Write), true, 21)
	wantDecision(t, e.Evaluate(7, at(1, "a/x"), Write), false, 20)
}

// Test 3. A user grant and a group grant at the same depth, one DENY and one
// ALLOW: the outcome depends only on which is the DENY, never on which names
// a user.
func TestNoPrincipalKindPriority(t *testing.T) {
	groupDenies := []Grant{
		{ID: 30, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true},
		{ID: 31, Group: 3, Share: 1, Subpath: ParsePath("a"), Deny: Read, Inherit: true},
	}
	userDenies := []Grant{
		{ID: 30, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true},
		{ID: 31, User: 7, Share: 1, Subpath: ParsePath("a"), Deny: Read, Inherit: true},
	}
	members := []Membership{{User: 7, Group: 3}}

	for _, tc := range []struct {
		name   string
		grants []Grant
	}{
		{"the group grant denies", groupDenies},
		{"the user grant denies", userDenies},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvaluator()
			if err := e.LoadFromState(tc.grants, members); err != nil {
				t.Fatalf("load: %v", err)
			}
			wantDecision(t, e.Evaluate(7, at(1, "a"), Read), false, 31)
		})
	}
}

// Test 4. Two ALLOW grants at different depths, neither denying anything: the
// deeper one decides even though the shallower would also satisfy want.
func TestTiesBreakByDepthAlone(t *testing.T) {
	e := loaded(t,
		userGrant(40, 7, "", Read, 0),
		userGrant(41, 7, "a/b", Read, 0),
	)
	wantDecision(t, e.Evaluate(7, at(1, "a/b"), Read), true, 41)
}

// Test 5. A path with no matching grant is denied by the algorithm's own
// default, distinguished from a rule-driven denial by By == 0.
func TestDefaultDeny(t *testing.T) {
	e := loaded(t, userGrant(50, 7, "a", Read, 0))
	wantDecision(t, e.Evaluate(7, at(1, "other"), Read), false, 0)
	wantDecision(t, e.Evaluate(7, at(2, "a"), Read), false, 0)
	wantDecision(t, e.Evaluate(8, at(1, "a"), Read), false, 0)
}

// Test 6. READ at a/b and WRITE at a compose: Effective at a/b/c holds both,
// and the multi-bit Evaluate succeeds through its own depth walk.
func TestCompositionAcrossDepths(t *testing.T) {
	e := loaded(t,
		userGrant(60, 7, "a", Write, 0),
		userGrant(61, 7, "a/b", Read, 0),
	)
	if got := e.Effective(7, at(1, "a/b/c")); !got.Has(Read | Write) {
		t.Fatalf("effective %s, want read and write", got)
	}
	if got := e.Evaluate(7, at(1, "a/b/c"), Read|Write); !got.Allowed {
		t.Fatalf("multi-bit evaluate %+v, want allowed", got)
	}
}

// Test 7. Two grants at the exact same depth, one allowing READ and the other
// WRITE: only the first reduces want and the other is never consulted, so the
// combined ask falls through to the default. Each bit alone is allowed.
func TestSameDepthPartialAllowDoesNotCombine(t *testing.T) {
	e := loaded(t,
		userGrant(70, 7, "a", Read, 0),
		userGrant(71, 7, "a", Write, 0),
	)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read|Write), false, 0)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 70)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Write), true, 71)
}

// Test 8. A non-inheriting grant covers exactly its subpath; an inheriting one
// covers everything under it.
func TestInheritTrueVersusFalse(t *testing.T) {
	exact := loaded(t, Grant{
		ID: 80, User: 7, Share: 1, Subpath: ParsePath("a/b"), Allow: Read,
	})
	wantDecision(t, exact.Evaluate(7, at(1, "a/b"), Read), true, 80)
	wantDecision(t, exact.Evaluate(7, at(1, "a/b/c"), Read), false, 0)

	inheriting := loaded(t, userGrant(81, 7, "a/b", Read, 0))
	wantDecision(t, inheriting.Evaluate(7, at(1, "a/b"), Read), true, 81)
	wantDecision(t, inheriting.Evaluate(7, at(1, "a/b/c"), Read), true, 81)
}

// Test 9. A group grant applies to a member and not to a non-member, and a
// membership change takes effect only after a reload.
func TestGroupMembership(t *testing.T) {
	grants := []Grant{{
		ID: 90, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true,
	}}
	e := NewEvaluator()
	if err := e.LoadFromState(grants, []Membership{{User: 7, Group: 3}}); err != nil {
		t.Fatalf("load: %v", err)
	}
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 90)
	wantDecision(t, e.Evaluate(8, at(1, "a"), Read), false, 0)

	// The returned slice is a copy: writing through it must not enroll anyone.
	if groups := e.MembershipOf(7); len(groups) == 1 {
		groups[0] = 4
		wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 90)
	} else {
		t.Fatalf("membership of the granted user is %v, want one group", groups)
	}

	if err := e.LoadFromState(grants, []Membership{{User: 8, Group: 3}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantDecision(t, e.Evaluate(8, at(1, "a"), Read), true, 90)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), false, 0)
}

// Test 10. A path outside every grant holds no bits at all, which is what the
// layer above turns into a 404 rather than a 403.
func TestExistenceRuleComposition(t *testing.T) {
	e := loaded(t, userGrant(100, 7, "a", Read|Write|Download, 0))
	outside := at(1, "elsewhere/deep")
	if got := e.Effective(7, outside); !got.IsEmpty() {
		t.Fatalf("effective %s outside every grant, want empty", got)
	}
	for _, bit := range orderedBits() {
		wantDecision(t, e.Evaluate(7, outside, bit), false, 0)
	}
}

// A grant naming neither a user nor a group matches nobody, so a malformed
// row is inert rather than wildly permissive.
func TestAGrantNamingNobodyMatchesNobody(t *testing.T) {
	e := NewEvaluator()
	if err := e.LoadFromState(
		[]Grant{{ID: 110, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}},
		[]Membership{{User: 7, Group: 3}},
	); err != nil {
		t.Fatalf("load: %v", err)
	}
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), false, 0)
	wantDecision(t, e.Evaluate(0, at(1, "a"), Read), false, 0)
}

// A DENY of a bit the caller did not ask for does not settle the level.
func TestAnUnrelatedDenyDoesNotDecide(t *testing.T) {
	e := loaded(t,
		userGrant(120, 7, "a", 0, Delete),
		userGrant(121, 7, "a", Read, 0),
	)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 121)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Delete), false, 120)
}
