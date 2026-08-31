package vfs

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

func parseSafe(t *testing.T, s string) SafePath {
	t.Helper()
	p, err := ParseSafePath(s)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", s, err)
	}
	return p
}

// TestEscapeTableRefusesEverywhere covers point 20 of the spec's test list:
// the existing-name table is enforced identically by every entry point that
// reaches it.
func TestEscapeTableRefusesEverywhere(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty component", "a//b", ErrInvalidName},
		{"dot", ".", ErrInvalidName},
		{"dot dot", "..", ErrInvalidName},
		{"dot dot in the middle", "a/../b", ErrInvalidName},
		{"embedded slash cannot happen via split, tested via JoinExisting", "", nil},
		{"nul byte", "a\x00b", ErrInvalidName},
		{"over length", strings.Repeat("a", limits.NameBytes+1), limits.ErrTooLarge},
		{"reserved trash", ".sctrash", ErrReservedName},
		{"reserved part", ".scpart-x", ErrReservedName},
		{"reserved meta", ".scmeta", ErrReservedName},
		{"reserved index", ".scindex", ErrReservedName},
	}
	for _, c := range cases {
		if c.want == nil {
			continue
		}
		t.Run(c.name+"/ParseVpath", func(t *testing.T) {
			if _, err := ParseVpath(c.in); !errors.Is(err, c.want) {
				t.Fatalf("ParseVpath(%q) = %v, want %v", c.in, err, c.want)
			}
		})
		t.Run(c.name+"/ParseSharePath", func(t *testing.T) {
			if _, err := ParseSharePath(c.in); !errors.Is(err, c.want) {
				t.Fatalf("ParseSharePath(%q) = %v, want %v", c.in, err, c.want)
			}
		})
		t.Run(c.name+"/ParseSafePath", func(t *testing.T) {
			if _, err := ParseSafePath(c.in); !errors.Is(err, c.want) {
				t.Fatalf("ParseSafePath(%q) = %v, want %v", c.in, err, c.want)
			}
		})
		t.Run(c.name+"/JoinExisting", func(t *testing.T) {
			if _, err := RootPath().JoinExisting(c.in); !errors.Is(err, c.want) {
				t.Fatalf("JoinExisting(%q) = %v, want %v", c.in, err, c.want)
			}
		})
	}
}

func TestJoinExistingRefusesAnEmbeddedSlash(t *testing.T) {
	if _, err := RootPath().JoinExisting("a/b"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("JoinExisting(%q) = %v, want ErrInvalidName", "a/b", err)
	}
}

func TestParseSafePathRefusesAnAbsolutePath(t *testing.T) {
	if _, err := ParseSafePath("/etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("ParseSafePath(\"/etc/passwd\") = %v, want ErrInvalidName", err)
	}
}

// TestVpathAcceptsExactlyOneLeadingSlash covers the one repair ParseVpath is
// allowed to make.
func TestVpathAcceptsExactlyOneLeadingSlash(t *testing.T) {
	rooted, err := ParseVpath("/documents/notes.md")
	if err != nil {
		t.Fatalf("a rooted client path was refused: %v", err)
	}
	if rooted.String() != "documents/notes.md" {
		t.Fatalf("String() = %q, want the slash stripped", rooted.String())
	}
	bare, err := ParseVpath("documents/notes.md")
	if err != nil || bare.String() != rooted.String() {
		t.Fatalf("the two spellings should agree: %q vs %q (%v)", rooted, bare, err)
	}
	root, err := ParseVpath("/")
	if err != nil || !root.IsRoot() {
		t.Fatalf("ParseVpath(\"/\") = %v, %v, want the virtual root", root, err)
	}
}

// TestVpathDoesNotAdmitTraversalUnderTheSlash proves the leading-slash
// repair does not widen what the traversal table accepts.
func TestVpathDoesNotAdmitTraversalUnderTheSlash(t *testing.T) {
	for _, in := range []string{
		"//a", "/../etc", "/a/../b", "/a/", "/a\x00b", "/.sctrash", "/a/.scpart-x/b",
	} {
		if p, err := ParseVpath(in); err == nil {
			t.Fatalf("ParseVpath(%q) accepted it as %q", in, p)
		}
	}
}

// TestCreationTableRefusesWhatExistingAccepts covers point 21: names another
// program's tool already wrote must remain reachable even though this
// package would refuse minting them.
func TestCreationTableRefusesWhatExistingAccepts(t *testing.T) {
	names := []string{
		"CON", "con", "PRN", "AUX", "NUL", "COM1", "com9", "LPT1", "lpt9",
		"CON.txt", "report:final", "trailing.", "trailing ", "control\x01char",
	}
	root := RootPath()
	for _, name := range names {
		if strings.ContainsAny(name, "\x00") {
			continue
		}
		if _, err := root.JoinExisting(name); err != nil {
			t.Errorf("JoinExisting(%q) refused an already-existing name: %v", name, err)
		}
		if _, err := root.Join(name); err == nil {
			t.Errorf("Join(%q) minted a name no Windows or SMB client could open", name)
		}
	}
}

func TestControlBytesAreOnlyRefusedOnCreate(t *testing.T) {
	name := "a\x01b"
	if _, err := RootPath().JoinExisting(name); err != nil {
		t.Fatalf("JoinExisting(%q) refused a control byte in an existing name: %v", name, err)
	}
	if _, err := RootPath().Join(name); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Join(%q) = %v, want ErrInvalidName", name, err)
	}
}

// TestJoinControlIsTheOnlyReservedNameProducer covers point 22.
func TestJoinControlIsTheOnlyReservedNameProducer(t *testing.T) {
	root := RootPath()
	if _, err := root.Join(".scpart-x"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("Join minted a reserved name: %v", err)
	}
	if _, err := root.JoinExisting(".scpart-x"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("JoinExisting walked into a reserved name: %v", err)
	}
	got, err := root.JoinControl(".scpart-x")
	if err != nil {
		t.Fatalf("JoinControl(%q): %v", ".scpart-x", err)
	}
	if got.String() != ".scpart-x" {
		t.Fatalf("JoinControl produced %q", got)
	}
}

func TestJoinControlStillAppliesEveryOtherRule(t *testing.T) {
	for _, name := range []string{"", ".", "..", ".scpart-a/b", ".scpart-a\x00", ".scpart-a:b", ".scpart-a "} {
		if _, err := RootPath().JoinControl(name); err == nil {
			t.Fatalf("JoinControl(%q) was accepted", name)
		}
	}
	overLong := ".scpart-" + strings.Repeat("a", limits.NameBytes)
	if _, err := RootPath().JoinControl(overLong); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("JoinControl(over-length) = %v, want ErrTooLarge", err)
	}
}

// TestBoundsRefuseRatherThanTruncate covers point 23.
func TestBoundsRefuseRatherThanTruncate(t *testing.T) {
	atName := strings.Repeat("a", limits.NameBytes)
	if _, err := ParseSafePath(atName); err != nil {
		t.Fatalf("a component of exactly %d bytes was refused: %v", limits.NameBytes, err)
	}
	overName := atName + "a"
	if _, err := ParseSafePath(overName); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("a component of %d bytes: %v, want ErrTooLarge", len(overName), err)
	}

	atDepth := strings.Repeat("a/", limits.PathComponents-1) + "a"
	if _, err := ParseSafePath(atDepth); err != nil {
		t.Fatalf("exactly %d components were refused: %v", limits.PathComponents, err)
	}
	overDepth := atDepth + "/a"
	if _, err := ParseSafePath(overDepth); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("%d components: %v, want ErrTooLarge", limits.PathComponents+1, err)
	}

	overBytes := strings.Repeat("ab/", limits.PathBytes/3+1) + "a"
	if _, err := ParseSafePath(overBytes); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("a %d byte path: %v, want ErrTooLarge", len(overBytes), err)
	}
}

func TestComponentCountBuiltIncrementallyIsBounded(t *testing.T) {
	p := RootPath()
	var err error
	for i := 0; i < limits.PathComponents; i++ {
		p, err = p.JoinExisting("a")
		if err != nil {
			t.Fatalf("component %d: %v", i, err)
		}
	}
	if _, err := p.JoinExisting("a"); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("one component past the bound: %v, want ErrTooLarge", err)
	}
}

// TestHasPrefixAndUnderAreComponentWise covers point 24.
func TestHasPrefixAndUnderAreComponentWise(t *testing.T) {
	a := parseSafe(t, "a")
	ab := parseSafe(t, "a/b")
	abc := parseSafe(t, "a/b/c")
	abSibling := parseSafe(t, "ab/d")

	if !abc.HasPrefix(ab) {
		t.Fatal("a/b/c should be beneath a/b")
	}
	if abSibling.HasPrefix(a) {
		t.Fatal("ab/d should not be beneath a")
	}
	if !ab.HasPrefix(RootPath()) {
		t.Fatal("everything should be beneath the root")
	}
	if !abc.Under(ab) {
		t.Fatal("a/b/c should be under a/b")
	}
	if abSibling.Under(a) {
		t.Fatal("ab/d should not be under a")
	}
	if !a.Under(a) {
		t.Fatal("a path should be under itself")
	}
}

// TestComponentsIsADefensiveCopy covers point 25.
func TestComponentsIsADefensiveCopy(t *testing.T) {
	p := parseSafe(t, "a/b")
	got := p.Components()
	got[0] = "mutated"
	if p.String() != "a/b" {
		t.Fatalf("mutating the returned slice changed the path: %q", p.String())
	}
}

func TestParentAndName(t *testing.T) {
	p := parseSafe(t, "a/b/c")
	if p.Name() != "c" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "c")
	}
	if p.Parent().String() != "a/b" {
		t.Fatalf("Parent() = %q, want %q", p.Parent().String(), "a/b")
	}
	if !RootPath().Parent().IsRoot() {
		t.Fatal("the parent of the root should be the root")
	}
	if RootPath().Name() != "" {
		t.Fatal("the root should have no name")
	}
}

func TestParentDoesNotAliasTheChild(t *testing.T) {
	p := parseSafe(t, "a/b/c")
	sibling, err := p.Parent().JoinExisting("z")
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != "a/b/c" {
		t.Fatalf("joining through the parent rewrote the child: %q", p.String())
	}
	if sibling.String() != "a/b/z" {
		t.Fatalf("sibling = %q, want a/b/z", sibling.String())
	}
}

func TestVpathLabelAndRest(t *testing.T) {
	v, err := ParseVpath("media/movies/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if v.Label() != "media" {
		t.Fatalf("Label() = %q, want %q", v.Label(), "media")
	}
	if v.Rest().String() != "movies/a.mkv" {
		t.Fatalf("Rest() = %q, want %q", v.Rest().String(), "movies/a.mkv")
	}
	if v.Name() != "a.mkv" {
		t.Fatalf("Name() = %q, want %q", v.Name(), "a.mkv")
	}

	root, err := ParseVpath("")
	if err != nil {
		t.Fatal(err)
	}
	if !root.IsRoot() || root.Label() != "" {
		t.Fatal("the empty Vpath should be the virtual root")
	}

	bareLabel, err := ParseVpath("media")
	if err != nil {
		t.Fatal(err)
	}
	if bareLabel.Label() != "media" || !bareLabel.Rest().IsRoot() {
		t.Fatalf("a bare label: label %q rest %q", bareLabel.Label(), bareLabel.Rest())
	}
}

func TestVpathRoundTripsThroughSharePathAndSafePath(t *testing.T) {
	v, err := ParseVpath("media/movies/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	safe, err := v.Rest().Safe()
	if err != nil {
		t.Fatal(err)
	}
	back, err := NewVpath(v.Label(), safe.Share())
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != v.String() {
		t.Fatalf("round trip: %q -> %q", v, back)
	}
}

func TestSharePathIsRoot(t *testing.T) {
	root, err := ParseSharePath("")
	if err != nil {
		t.Fatal(err)
	}
	if !root.IsRoot() {
		t.Fatal("an empty SharePath should report IsRoot")
	}
	nonRoot, err := ParseSharePath("a")
	if err != nil {
		t.Fatal(err)
	}
	if nonRoot.IsRoot() {
		t.Fatal("a non-empty SharePath should not report IsRoot")
	}
}

func TestSafePathNameAndString(t *testing.T) {
	p := parseSafe(t, "a/b/c")
	if p.Name() != "c" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "c")
	}
	if p.String() != "a/b/c" {
		t.Fatalf("String() = %q, want %q", p.String(), "a/b/c")
	}
	if RootPath().String() != "" {
		t.Fatalf("the root's String() should be empty, got %q", RootPath().String())
	}
}

func TestIsReservedName(t *testing.T) {
	for _, name := range []string{".sctrash", ".sctrash/x", ".scpart-1", ".scmeta.db", ".scindex"} {
		if !IsReservedName(name) {
			t.Fatalf("%q should be reserved", name)
		}
	}
	for _, name := range []string{"sctrash", ".sc", "scpart-1", ".hidden"} {
		if IsReservedName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
	}
}

func TestIsStagingName(t *testing.T) {
	name, err := stagingName()
	if err != nil {
		t.Fatal(err)
	}
	if !IsStagingName(name) {
		t.Fatalf("stagingName() produced %q, which IsStagingName rejects", name)
	}
	if IsStagingName(".scpart-") {
		t.Fatal("the bare prefix with no suffix should not count as a staging name")
	}
	if IsStagingName("plain-name") {
		t.Fatal("an ordinary name should not count as a staging name")
	}
}

func TestEveryRefusedNameIsActuallyRefused(t *testing.T) {
	for _, name := range RefusedNames() {
		if _, err := RootPath().Join(name); err == nil {
			t.Errorf("%q is advertised as refused but was accepted", name)
		}
		lower := strings.ToLower(name)
		if _, err := RootPath().Join(lower); err == nil {
			t.Errorf("%q is advertised as refused but was accepted", lower)
		}
	}
}

func TestEveryRefusedCharacterIsActuallyRefused(t *testing.T) {
	for _, c := range RefusedNameCharacters() {
		name := "a" + c + "b"
		if _, err := RootPath().Join(name); err == nil {
			t.Errorf("%q contains the advertised-refused character %q but was accepted", name, c)
		}
	}
}

func TestParseSafePathAcceptsOrdinaryAndUnicodeNames(t *testing.T) {
	cases := []struct {
		in    string
		comps []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a/b/c", []string{"a", "b", "c"}},
		{"Mods ", []string{"Mods "}},
		{"CON", []string{"CON"}},
		{"report:final", []string{"report:final"}},
		{"café", []string{"café"}},
	}
	for _, c := range cases {
		p, err := ParseSafePath(c.in)
		if err != nil {
			t.Fatalf("ParseSafePath(%q): %v", c.in, err)
		}
		if got := p.Components(); !slicesEqual(got, c.comps) {
			t.Fatalf("ParseSafePath(%q).Components() = %v, want %v", c.in, got, c.comps)
		}
		if p.String() != c.in {
			t.Fatalf("ParseSafePath(%q).String() = %q", c.in, p.String())
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
