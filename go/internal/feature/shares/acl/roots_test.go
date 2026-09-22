package acl

import "testing"

// Test 11. The label comes from the grant's own label, then the subpath's
// last component, then share-<id>.
func TestRootsLabelPriority(t *testing.T) {
	e := loaded(t,
		Grant{ID: 1, User: 7, Share: 1, Subpath: ParsePath("a/b"), Allow: Read, Label: "Chosen", Inherit: true},
		Grant{ID: 2, User: 7, Share: 2, Subpath: ParsePath("a/b"), Allow: Read, Inherit: true},
		Grant{ID: 3, User: 7, Share: 3, Subpath: ParsePath("/"), Allow: Read, Inherit: true},
	)
	got := e.Roots(7)
	want := []string{"Chosen", "b", "share-3"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Fatalf("entry %d labeled %q, want %q", i, got[i].Label, label)
		}
	}
}

// Test 12. Two grants resolving to the same base label are disambiguated in
// encounter order.
func TestRootsLabelCollision(t *testing.T) {
	e := loaded(t,
		Grant{ID: 1, User: 7, Share: 1, Subpath: ParsePath("docs"), Allow: Read, Inherit: true},
		Grant{ID: 2, User: 7, Share: 2, Subpath: ParsePath("docs"), Allow: Read, Inherit: true},
		Grant{ID: 3, User: 7, Share: 3, Subpath: ParsePath("docs"), Allow: Read, Inherit: true},
	)
	got := e.Roots(7)
	want := []string{"docs", "docs (2)", "docs (3)"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Fatalf("entry %d labeled %q, want %q", i, got[i].Label, label)
		}
	}
}

// Test 13. A grant with DENY only and no ALLOW earns no entry.
func TestRootsSkipsAGrantWithoutRead(t *testing.T) {
	e := loaded(t,
		userGrant(1, 7, "denied", 0, Read),
		userGrant(2, 7, "writeonly", Write, 0),
		userGrant(3, 7, "readable", Read, 0),
	)
	got := e.Roots(7)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Label != "readable" {
		t.Fatalf("entry labeled %q, want %q", got[0].Label, "readable")
	}
}

// Test 14. An entry's Perms can lack a bit its own triggering grant allowed,
// when a same-depth DENY cancels it, while the entry is still listed. Roots
// answers which subtrees have an explicit read-granting rule, not what the
// account can do there.
func TestRootsPermsCanLackTheTriggeringBit(t *testing.T) {
	e := loaded(t,
		userGrant(1, 7, "shared", Read|Write, 0),
		userGrant(2, 7, "shared", 0, Read),
	)
	got := e.Roots(7)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Perms.Has(Read) {
		t.Fatalf("entry perms %s still hold read despite the same-depth deny", got[0].Perms)
	}
	if !got[0].Perms.Has(Write) {
		t.Fatalf("entry perms %s lost write, which nothing denies", got[0].Perms)
	}
}

// Roots leaves the three registry-owned fields zero: the core fills them in.
func TestRootsLeavesTheRegistryFieldsToTheCore(t *testing.T) {
	e := loaded(t, userGrant(1, 7, "a", Read, 0))
	got := e.Roots(7)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].TrashEnabled || got[0].SharedExternally || got[0].BrokenReason != "" {
		t.Fatalf("entry %+v carries registry facts this package cannot know", got[0])
	}
}

// A group grant earns its member a root entry, the same as a user grant.
func TestRootsFollowsGroupMembership(t *testing.T) {
	e := NewEvaluator()
	if err := e.LoadFromState(
		[]Grant{{ID: 1, Group: 3, Share: 1, Subpath: ParsePath("team"), Allow: Read, Inherit: true}},
		[]Membership{{User: 7, Group: 3}},
	); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := e.Roots(7); len(got) != 1 || got[0].Label != "team" {
		t.Fatalf("member roots %+v, want one entry labeled team", got)
	}
	if got := e.Roots(8); len(got) != 0 {
		t.Fatalf("non-member roots %+v, want none", got)
	}
}
