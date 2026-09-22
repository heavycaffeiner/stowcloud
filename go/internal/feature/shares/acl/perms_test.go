package acl

import (
	"slices"
	"testing"
)

// Test 20. The set operations over representative bit combinations.
func TestPermsSetOperations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		have, want Perms
		has        bool
		intersects bool
		remove     Perms
	}{
		{"empty holds nothing", 0, Read, false, false, 0},
		{"empty wanted by everything", Read, 0, true, false, Read},
		{"exact match", Read, Read, true, true, 0},
		{"superset", Read | Write, Read, true, true, Write},
		{"subset", Read, Read | Write, false, true, 0},
		{"disjoint", Read, Write, false, false, Read},
		{"partial overlap", Read | Write, Write | Delete, false, true, Read},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.have.Has(tc.want); got != tc.has {
				t.Errorf("Has = %v, want %v", got, tc.has)
			}
			if got := tc.have.Intersects(tc.want); got != tc.intersects {
				t.Errorf("Intersects = %v, want %v", got, tc.intersects)
			}
			if got := tc.have.Remove(tc.want); got != tc.remove {
				t.Errorf("Remove = %s, want %s", got, tc.remove)
			}
		})
	}
	if !Perms(0).IsEmpty() {
		t.Error("zero is not empty")
	}
	if Read.IsEmpty() {
		t.Error("read reads as empty")
	}
}

// Test 21. The two splits that must not collapse: viewing is not downloading,
// and renaming in place is not carrying a file out of a subtree.
func TestTheTwoSplitsStaySeparate(t *testing.T) {
	if Read == Download {
		t.Fatal("read and download are the same bit")
	}
	if Rename == Move {
		t.Fatal("rename and move are the same bit")
	}
	if Read.Has(Download) {
		t.Fatal("a read-only grant also hands out the bytes")
	}
	if Rename.Has(Move) {
		t.Fatal("a rename grant also allows a move out of the subtree")
	}
}

// Every bit is distinct, so no two permissions alias each other.
func TestEveryBitIsDistinct(t *testing.T) {
	var seen Perms
	for _, bit := range orderedBits() {
		if seen.Intersects(bit) {
			t.Fatalf("bit %s overlaps an earlier one", bit)
		}
		seen |= bit
	}
}

func TestPermsString(t *testing.T) {
	for _, tc := range []struct {
		in   Perms
		want string
	}{
		{0, "-"},
		{Read, "read"},
		{Download, "download"},
		{Read | Write, "read/write"},
		{Download | Read, "read/download"},
		{Read | Write | Create | Delete | Rename | Move | Share | Download,
			"read/write/create/delete/rename/move/share/download"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

// Test 23. Every bit round-trips against its name, and an unknown name is
// refused rather than silently dropped.
func TestPermByNameRoundTrip(t *testing.T) {
	for _, named := range NamedPerms() {
		got, ok := PermByName(named.Name)
		if !ok {
			t.Fatalf("%q is not a known permission", named.Name)
		}
		if got != named.Perm {
			t.Fatalf("%q maps to %s, want %s", named.Name, got, named.Perm)
		}
	}
	if got := len(NamedPerms()); got != 8 {
		t.Fatalf("NamedPerms lists %d entries for the eight defined bits", got)
	}
	for _, name := range []string{"", "READ", "admin", "read/write", " read"} {
		if got, ok := PermByName(name); ok {
			t.Fatalf("%q was accepted as %s", name, got)
		}
	}
}

// NamedPerms renders in the one fixed order.
func TestNamedPermsOrder(t *testing.T) {
	want := []string{"read", "write", "create", "delete", "rename", "move", "share", "download"}
	got := make([]string, 0, len(want))
	for _, named := range NamedPerms() {
		got = append(got, named.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// Test 22. ParsePath and String round-trip, with the empty and single-slash
// spellings folding to the root.
func TestPathRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   string
		length int
		name   string
	}{
		{"", "/", 0, ""},
		{"/", "/", 0, ""},
		{"//", "/", 0, ""},
		{"a", "/a", 1, "a"},
		{"/a", "/a", 1, "a"},
		{"a/b", "/a/b", 2, "b"},
		{"/a/b/", "/a/b", 2, "b"},
		{"a/b/c", "/a/b/c", 3, "c"},
	} {
		p := ParsePath(tc.in)
		if got := p.String(); got != tc.want {
			t.Errorf("ParsePath(%q).String() = %q, want %q", tc.in, got, tc.want)
		}
		if got := p.Len(); got != tc.length {
			t.Errorf("ParsePath(%q).Len() = %d, want %d", tc.in, got, tc.length)
		}
		if got := p.Name(); got != tc.name {
			t.Errorf("ParsePath(%q).Name() = %q, want %q", tc.in, got, tc.name)
		}
		if got := ParsePath(p.String()).String(); got != tc.want {
			t.Errorf("second round trip of %q gave %q", tc.in, got)
		}
	}
}

func TestPathIsPrefixOf(t *testing.T) {
	for _, tc := range []struct {
		p, q string
		want bool
	}{
		{"", "", true},
		{"", "a/b", true},
		{"a", "a", true},
		{"a", "a/b", true},
		{"a/b", "a", false},
		{"a/b", "a/b/c", true},
		{"a/b", "a/c", false},
		{"ab", "a/b", false},
	} {
		if got := ParsePath(tc.p).IsPrefixOf(ParsePath(tc.q)); got != tc.want {
			t.Errorf("%q prefix of %q = %v, want %v", tc.p, tc.q, got, tc.want)
		}
	}
}

func TestSubpathEquals(t *testing.T) {
	for _, tc := range []struct {
		p, q string
		want bool
	}{
		{"", "", true},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"a/b/c", "a/b", false},
		{"a/b", "a/c", false},
	} {
		if got := subpathEquals(ParsePath(tc.p), ParsePath(tc.q)); got != tc.want {
			t.Errorf("subpathEquals(%q, %q) = %v, want %v", tc.p, tc.q, got, tc.want)
		}
	}
}

// NewPath and Components both copy, so a caller cannot reach into a grant's
// subpath through the slice it passed or the one it got back.
func TestPathCopiesItsComponents(t *testing.T) {
	comps := []string{"a", "b"}
	p := NewPath(comps...)
	comps[0] = "escaped"
	if got := p.String(); got != "/a/b" {
		t.Fatalf("path became %q after the caller mutated its input", got)
	}

	out := p.Components()
	out[0] = "escaped"
	if got := p.String(); got != "/a/b" {
		t.Fatalf("path became %q after the caller mutated what Components returned", got)
	}
}
