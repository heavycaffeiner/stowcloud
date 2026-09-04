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
// WRITE: the ask for both is satisfied by the pair, and the decision names
// the rule that completed it.
//
// The reachable shape is a user grant beside a group grant on one folder.
// Treating those as alternatives rather than as a set denied every operation
// needing two bits, which is every write and every download.
func TestSameDepthAllowsCompose(t *testing.T) {
	e := loaded(t,
		userGrant(70, 7, "a", Read, 0),
		userGrant(71, 7, "a", Write, 0),
	)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read|Write), true, 71)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 70)
	wantDecision(t, e.Evaluate(7, at(1, "a"), Write), true, 71)

	mixed := NewEvaluator()
	if err := mixed.LoadFromState([]Grant{
		{ID: 72, User: 7, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true},
		{ID: 73, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Download, Inherit: true},
	}, []Membership{{User: 7, Group: 3}}); err != nil {
		t.Fatalf("load: %v", err)
	}
	wantDecision(t, mixed.Evaluate(7, at(1, "team/file.txt"), Read|Download), true, 73)

	// A caller outside the group holds only what names them directly.
	wantDecision(t, mixed.Evaluate(9, at(1, "team/file.txt"), Read|Download), false, 0)
}

// What a multi-bit ask answers and what Effective reports are the same
// question, so they cannot disagree: a surface that lists a file by asking
// for one bit and then downloads it by asking for two must not find the
// second ask refused.
func TestEvaluateAgreesWithEffective(t *testing.T) {
	e := loaded(t,
		userGrant(74, 7, "a", Read, 0),
		userGrant(75, 7, "a", Download, 0),
		userGrant(76, 7, "a/private", 0, Download),
	)
	for _, tc := range []struct {
		path string
		want Perms
	}{
		{"a", Read | Download},
		{"a/deep/file", Read | Download},
		{"a/private", Read | Download},
		{"a/private/file", Read},
	} {
		effective := e.Effective(7, at(1, tc.path))
		decided := e.Evaluate(7, at(1, tc.path), tc.want).Allowed
		if decided != effective.Has(tc.want) {
			t.Errorf("at %q: Evaluate(%s)=%v while Effective is %s",
				tc.path, tc.want, decided, effective)
		}
	}
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

// The composition truth table walks every grant shape systematically rather
// than one shape per test: an allow alone, a deny alone, both at one depth,
// both split across depths, an inheriting grant against a non-inheriting
// one, a group grant against a user grant, and a multi-bit ask that only two
// grants together satisfy.
//
// The last two rows are why the table exists. Under the composition fix,
// evaluateLocked keeps consulting a level's ALLOW grants after the first
// partial match; reverted to consulting only the first, those two rows
// evaluate to false with By == 0 instead of true with the completing
// grant's id, and the table fails.
func TestGrantCompositionTruthTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grants  []Grant
		members []Membership
		user    int64
		path    string
		want    Perms
		allowed bool
		by      int64
	}{
		{
			name:    "allow only",
			grants:  []Grant{{ID: 200, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}},
			user:    7,
			path:    "a",
			want:    Read,
			allowed: true,
			by:      200,
		},
		{
			name:    "deny only",
			grants:  []Grant{{ID: 201, User: 7, Share: 1, Subpath: ParsePath("a"), Deny: Read, Inherit: true}},
			user:    7,
			path:    "a",
			want:    Read,
			allowed: false,
			by:      201,
		},
		{
			// Resolving a name before deciding what to do with it. Any rule
			// that grants something answers it, and the composition loop
			// cannot, since nothing intersects an empty set.
			name:    "an ask naming no permission is answered by a covering allow",
			grants:  []Grant{{ID: 215, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}},
			user:    7,
			path:    "a/b",
			want:    0,
			allowed: true,
			by:      215,
		},
		{
			name:    "an ask naming no permission is refused with no covering rule",
			grants:  []Grant{{ID: 216, User: 7, Share: 1, Subpath: ParsePath("other"), Allow: Read, Inherit: true}},
			user:    7,
			path:    "a",
			want:    0,
			allowed: false,
			by:      0,
		},
		{
			// A rule that only withholds a bit does not say the path is
			// reachable, so it is not an answer to this ask either.
			name:    "a deny-only rule does not answer an ask naming no permission",
			grants:  []Grant{{ID: 217, User: 7, Share: 1, Subpath: ParsePath("a"), Deny: Write, Inherit: true}},
			user:    7,
			path:    "a",
			want:    0,
			allowed: false,
			by:      0,
		},
		{
			name: "allow and deny at the same depth",
			grants: []Grant{
				{ID: 202, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true},
				{ID: 203, User: 7, Share: 1, Subpath: ParsePath("a"), Deny: Read, Inherit: true},
			},
			user:    7,
			path:    "a",
			want:    Read,
			allowed: false,
			by:      203,
		},
		{
			name: "allow and deny at different depths, the deeper allow wins",
			grants: []Grant{
				{ID: 204, User: 7, Share: 1, Subpath: ParsePath("a"), Deny: Write, Inherit: true},
				{ID: 205, User: 7, Share: 1, Subpath: ParsePath("a/b"), Allow: Write, Inherit: true},
			},
			user:    7,
			path:    "a/b/c",
			want:    Write,
			allowed: true,
			by:      205,
		},
		{
			name:    "an inheriting grant covers a descendant",
			grants:  []Grant{{ID: 206, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}},
			user:    7,
			path:    "a/b/c",
			want:    Read,
			allowed: true,
			by:      206,
		},
		{
			name:    "a non-inheriting grant does not cover a descendant",
			grants:  []Grant{{ID: 207, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read}},
			user:    7,
			path:    "a/b",
			want:    Read,
			allowed: false,
			by:      0,
		},
		{
			name:    "a group grant applies to a member",
			grants:  []Grant{{ID: 208, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true}},
			members: []Membership{{User: 7, Group: 3}},
			user:    7,
			path:    "team",
			want:    Read,
			allowed: true,
			by:      208,
		},
		{
			name:    "the same group grant does not apply to a non-member",
			grants:  []Grant{{ID: 208, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true}},
			members: []Membership{{User: 7, Group: 3}},
			user:    9,
			path:    "team",
			want:    Read,
			allowed: false,
			by:      0,
		},
		{
			// Reachable as two rows because they differ in reach: the store
			// keeps one grant per subject, share, subpath and inherit flag.
			// At the exact path both match, so the ask needs them together.
			name: "a multi-bit ask is satisfied by a subtree grant and a path grant",
			grants: []Grant{
				{ID: 209, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true},
				{ID: 210, User: 7, Share: 1, Subpath: ParsePath("a"), Allow: Write},
			},
			user:    7,
			path:    "a",
			want:    Read | Write,
			allowed: true,
			by:      210,
		},
		{
			name: "a multi-bit ask spans two group grants",
			grants: []Grant{
				{ID: 213, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true},
				{ID: 214, Group: 4, Share: 1, Subpath: ParsePath("team"), Allow: Write, Inherit: true},
			},
			members: []Membership{{User: 7, Group: 3}, {User: 7, Group: 4}},
			user:    7,
			path:    "team/file.txt",
			want:    Read | Write,
			allowed: true,
			by:      214,
		},
		{
			name: "a multi-bit ask spans a user grant and a group grant",
			grants: []Grant{
				{ID: 211, User: 7, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true},
				{ID: 212, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Download, Inherit: true},
			},
			members: []Membership{{User: 7, Group: 3}},
			user:    7,
			path:    "team/file.txt",
			want:    Read | Download,
			allowed: true,
			by:      212,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvaluator()
			if err := e.LoadFromState(tc.grants, tc.members); err != nil {
				t.Fatalf("load: %v", err)
			}
			wantDecision(t, e.Evaluate(tc.user, at(1, tc.path), tc.want), tc.allowed, tc.by)
		})
	}
}
