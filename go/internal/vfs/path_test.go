package vfs

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

func mustParse(t *testing.T, s string) SafePath {
	t.Helper()
	p, err := ParseSafePath(s)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", s, err)
	}
	return p
}

func TestParseSafePathRejectsTheEscapeTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"dot dot alone", "..", ErrInvalidName},
		{"dot dot in the middle", "a/../b", ErrInvalidName},
		{"dot dot at the end", "a/..", ErrInvalidName},
		{"dot alone", ".", ErrInvalidName},
		{"dot in the middle", "a/./b", ErrInvalidName},
		{"absolute", "/etc/passwd", ErrInvalidName},
		{"empty component", "a//b", ErrInvalidName},
		{"trailing slash", "a/", ErrInvalidName},
		{"nul byte", "a\x00b", ErrInvalidName},
		{"reserved trash", ".sctrash", ErrReservedName},
		{"reserved part", ".scpart-abc", ErrReservedName},
		{"reserved meta", ".scmeta", ErrReservedName},
		{"reserved index", ".scindex", ErrReservedName},
		{"reserved deeper", "a/.scpart-abc/b", ErrReservedName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseSafePath(c.in); !errors.Is(err, c.want) {
				t.Fatalf("ParseSafePath(%q) = %v, want %v", c.in, err, c.want)
			}
		})
	}
}

// A virtual path may carry one leading slash, and a share-relative one may not.
//
// The two are different boundaries and it matters which is which. A Vpath is
// the client's addressing form, where "/documents/a.txt" and "documents/a.txt"
// name the same thing because neither can mean anything but a share label and
// a path under it. A SafePath is what reaches the filesystem, and there a
// leading slash is the escape this whole table exists to refuse.
func TestALeadingSlashIsAVpathSpellingAndNotASafePathOne(t *testing.T) {
	v, err := ParseVpath("/documents/notes/todo.md")
	if err != nil {
		t.Fatalf("a rooted virtual path was refused: %v", err)
	}
	if v.String() != "documents/notes/todo.md" {
		t.Fatalf("the parsed path is %q, want the slash dropped", v)
	}
	// The same path without the slash is the same path.
	bare, err := ParseVpath("documents/notes/todo.md")
	if err != nil || bare.String() != v.String() {
		t.Fatalf("the two spellings disagree: %q and %q (%v)", v, bare, err)
	}

	// "/" is the virtual root, which is how the client spells the empty
	// string. A client landing on its own home screen sends it.
	root, err := ParseVpath("/")
	if err != nil {
		t.Fatalf("the client's spelling of the root was refused: %v", err)
	}
	if !root.IsRoot() {
		t.Fatalf("%q did not parse to the virtual root", root)
	}

	// The filesystem-facing parser is unchanged: there a leading slash is an
	// absolute path and is refused.
	if _, err := ParseSafePath("/etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("ParseSafePath accepted an absolute path: %v", err)
	}
}

// Only one slash, and only at the front. Everything the escape table refuses
// is still refused when a slash is put in front of it.
func TestALeadingSlashDoesNotAdmitTheEscapeTable(t *testing.T) {
	for _, in := range []string{
		"//documents",      // an empty first component
		"/../etc",          // traversal
		"/a/../b",          // traversal below the label
		"/a/",              // a trailing slash
		"/a\x00b",          // NUL
		"/.sctrash",        // a reserved name
		"/a/.scpart-abc/b", // a reserved name deeper down
	} {
		if p, err := ParseVpath(in); err == nil {
			t.Fatalf("ParseVpath(%q) accepted it as %q", in, p)
		}
	}
}

// Normalising is what creates the bypass, so the refusal has to be the answer
// even where the result would have been harmless.
func TestDotDotIsRejectedRatherThanResolved(t *testing.T) {
	for _, in := range []string{"a/../a", "a/b/../b", "./a"} {
		if p, err := ParseSafePath(in); err == nil {
			t.Fatalf("ParseSafePath(%q) resolved to %q instead of refusing", in, p)
		}
	}
}

func TestParseSafePathAccepts(t *testing.T) {
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
		{"é", []string{"é"}},
		{"é", []string{"é"}},
	}
	for _, c := range cases {
		p, err := ParseSafePath(c.in)
		if err != nil {
			t.Fatalf("ParseSafePath(%q): %v", c.in, err)
		}
		got := p.Components()
		if len(got) != len(c.comps) {
			t.Fatalf("ParseSafePath(%q) components = %v, want %v", c.in, got, c.comps)
		}
		for i := range got {
			if got[i] != c.comps[i] {
				t.Fatalf("ParseSafePath(%q) components = %v, want %v", c.in, got, c.comps)
			}
		}
		if p.String() != c.in {
			t.Fatalf("ParseSafePath(%q).String() = %q", c.in, p.String())
		}
	}
}

// The traversal table and the creation table differ on purpose, and getting it
// backwards makes every name somebody else's tool wrote unaddressable.
func TestTraversalAcceptsWhatCreationRefuses(t *testing.T) {
	root := RootPath()
	for _, name := range []string{"CON", "report:final", "trailing.", "trailing "} {
		if _, err := root.JoinExisting(name); err != nil {
			t.Fatalf("JoinExisting(%q) refused a name already on disk: %v", name, err)
		}
		if _, err := root.Join(name); err == nil {
			t.Fatalf("Join(%q) minted a name no Windows or SMB client can open", name)
		}
	}
}

func TestJoinControlIsTheOnlyWayToAReservedName(t *testing.T) {
	root := RootPath()
	if _, err := root.Join(".scpart-abc"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("Join minted a control name: %v", err)
	}
	if _, err := root.JoinExisting(".scpart-abc"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("JoinExisting walked into a control name: %v", err)
	}
	p, err := root.JoinControl(".scpart-abc")
	if err != nil {
		t.Fatalf("JoinControl: %v", err)
	}
	if p.String() != ".scpart-abc" {
		t.Fatalf("JoinControl = %q", p)
	}
}

// JoinControl lifts one rule and not the rest.
func TestJoinControlStillAppliesEveryOtherRule(t *testing.T) {
	root := RootPath()
	for _, name := range []string{"", ".", "..", ".scpart-a/b", ".scpart-a\x00", ".scpart-a:b"} {
		if _, err := root.JoinControl(name); err == nil {
			t.Fatalf("JoinControl(%q) was accepted", name)
		}
	}
}

func TestLimitsRefuseRatherThanLargeInputsHappeningToFail(t *testing.T) {
	long := strings.Repeat("a", limits.NameBytes+1)
	if _, err := ParseSafePath(long); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("a %d byte component: %v, want ErrTooLarge", len(long), err)
	}

	// Exactly at each bound is accepted, one past it is refused, so the test
	// proves the bound is what refuses.
	atName := strings.Repeat("a", limits.NameBytes)
	if _, err := ParseSafePath(atName); err != nil {
		t.Fatalf("a component of exactly %d bytes was refused: %v", limits.NameBytes, err)
	}

	deep := strings.Repeat("a/", limits.PathComponents-1) + "a"
	if _, err := ParseSafePath(deep); err != nil {
		t.Fatalf("exactly %d components were refused: %v", limits.PathComponents, err)
	}
	tooDeep := deep + "/a"
	if _, err := ParseSafePath(tooDeep); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("%d components: %v, want ErrTooLarge", limits.PathComponents+1, err)
	}

	wide := strings.Repeat("ab/", limits.PathBytes/3) + "a"
	if _, err := ParseSafePath(wide); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("a %d byte path: %v, want ErrTooLarge", len(wide), err)
	}
}

func TestPushRespectsTheSameBounds(t *testing.T) {
	p := RootPath()
	var err error
	for i := 0; i < limits.PathComponents; i++ {
		p, err = p.JoinExisting("a")
		if err != nil {
			t.Fatalf("component %d: %v", i, err)
		}
	}
	if _, err := p.JoinExisting("a"); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("one past the component bound: %v, want ErrTooLarge", err)
	}
}

func TestParentAndName(t *testing.T) {
	p, err := ParseSafePath("a/b/c")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "c" {
		t.Fatalf("Name = %q", p.Name())
	}
	if p.Parent().String() != "a/b" {
		t.Fatalf("Parent = %q", p.Parent())
	}
	if !RootPath().Parent().IsRoot() {
		t.Fatal("the parent of the root must be the root")
	}
	if RootPath().Name() != "" {
		t.Fatal("the root has no name")
	}
}

// Parent shares the head of the backing array, so a later Join through the
// parent must not overwrite the child's own last component.
func TestParentDoesNotAliasTheChild(t *testing.T) {
	p, err := ParseSafePath("a/b/c")
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := p.Parent().JoinExisting("z")
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != "a/b/c" {
		t.Fatalf("joining through the parent rewrote the child: %q", p)
	}
	if sibling.String() != "a/b/z" {
		t.Fatalf("sibling = %q", sibling)
	}
}

func TestComponentsIsACopy(t *testing.T) {
	p, err := ParseSafePath("a/b")
	if err != nil {
		t.Fatal(err)
	}
	c := p.Components()
	c[0] = ".."
	if p.String() != "a/b" {
		t.Fatalf("editing the returned slice edited the path: %q", p)
	}
}

func TestHasPrefixIsComponentWise(t *testing.T) {
	ab := mustParse(t, "a/b")
	abc := mustParse(t, "a/b/c")
	abd := mustParse(t, "ab/d")
	if !abc.HasPrefix(ab) {
		t.Fatal("a/b/c is beneath a/b")
	}
	if abd.HasPrefix(ab) {
		t.Fatal("ab/d is not beneath a/b, and a string compare says it is")
	}
	if !ab.HasPrefix(RootPath()) {
		t.Fatal("everything is beneath the root")
	}
}

func TestVpathCarriesTheShareLabel(t *testing.T) {
	v, err := ParseVpath("media/movies/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if v.Label() != "media" {
		t.Fatalf("Label = %q", v.Label())
	}
	if v.Rest().String() != "movies/a.mkv" {
		t.Fatalf("Rest = %q", v.Rest())
	}

	root, err := ParseVpath("")
	if err != nil {
		t.Fatal(err)
	}
	if !root.IsRoot() || root.Label() != "" {
		t.Fatal("the empty Vpath is the virtual root")
	}

	one, err := ParseVpath("media")
	if err != nil {
		t.Fatal(err)
	}
	if one.Label() != "media" || !one.Rest().IsRoot() {
		t.Fatalf("a bare label: label %q rest %q", one.Label(), one.Rest())
	}
}

func TestVpathRoundTripsThroughTheCoreVocabulary(t *testing.T) {
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

func TestIsReservedName(t *testing.T) {
	for _, name := range []string{".sctrash", ".sctrash/x", ".scpart-1", ".scmeta.db", ".scindex"} {
		if !IsReservedName(name) {
			t.Fatalf("%q is reserved", name)
		}
	}
	for _, name := range []string{"sctrash", ".sc", "scpart-1", ".hidden"} {
		if IsReservedName(name) {
			t.Fatalf("%q is not reserved", name)
		}
	}
}

// The advertised rules have to be the enforced ones. A name advertised as
// legal and then refused makes a sync client retry the same file forever and
// the sync never converges, so these assert the lists against the validator
// rather than against a second copy of the table.
func TestEveryRefusedNameIsActuallyRefused(t *testing.T) {
	for _, name := range RefusedNames() {
		if _, err := RootPath().Join(name); err == nil {
			t.Errorf("%q is advertised as refused and is accepted", name)
		}
		// And in the lowercase spelling a client is as likely to send.
		lower := strings.ToLower(name)
		if _, err := RootPath().Join(lower); err == nil {
			t.Errorf("%q is advertised as refused and is accepted", lower)
		}
	}
}

func TestEveryRefusedCharacterIsActuallyRefused(t *testing.T) {
	for _, c := range RefusedNameCharacters() {
		name := "a" + c + "b"
		if _, err := RootPath().Join(name); err == nil {
			t.Errorf("%q contains %q, which is advertised as refused, and is accepted", name, c)
		}
	}
}
