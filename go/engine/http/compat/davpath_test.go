//go:build linux && compat_nc

package compat

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
)

// Both prefixes reach the same layout. A deployment behind a rewriting proxy
// sees one spelling and a direct client the other.
func TestBothVendorPrefixesReachTheSameLayout(t *testing.T) {
	for _, prefix := range []string{"/remote.php", "/index.php/remote.php"} {
		got, err := ParseDavPath(prefix + "/dav/files/alice/docs/a.txt")
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		if got.Layout != LayoutFiles {
			t.Errorf("%s gave layout %s", prefix, got.Layout)
		}
		if got.User != "alice" {
			t.Errorf("%s gave user %q", prefix, got.User)
		}
		if strings.Join(got.Path, "/") != "docs/a.txt" {
			t.Errorf("%s gave path %q", prefix, got.Path)
		}
	}
}

// The layouts a client actually sends.
func TestTheVendorLayouts(t *testing.T) {
	cases := []struct {
		raw      string
		layout   Layout
		user     string
		path     string
		transfer string
		member   string
	}{
		{raw: "/remote.php/webdav/a/b.txt", layout: LayoutLegacy, path: "a/b.txt"},
		{raw: "/remote.php/webdav", layout: LayoutLegacy},
		{raw: "/remote.php/dav/files/bob", layout: LayoutFiles, user: "bob"},
		{raw: "/remote.php/dav/files/bob/x/y", layout: LayoutFiles, user: "bob", path: "x/y"},
		{raw: "/remote.php/dav/uploads/bob/t1", layout: LayoutUploads, user: "bob", transfer: "t1"},
		{raw: "/remote.php/dav/uploads/bob/t1/5", layout: LayoutUploads, user: "bob", transfer: "t1", member: "5"},
		{
			raw: "/remote.php/dav/uploads/bob/t1/.file", layout: LayoutUploads,
			user: "bob", transfer: "t1", member: ".file",
		},
		{raw: "/remote.php/dav/trashbin/bob/trash", layout: LayoutTrash, user: "bob"},
		{raw: "/remote.php/dav/trashbin/bob/trash/e1", layout: LayoutTrash, user: "bob", path: "e1"},
		{raw: "/remote.php/dav/principals", layout: LayoutPrincipals},
		{raw: "/remote.php/dav/principals/users/bob", layout: LayoutPrincipals, path: "users/bob"},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, err := ParseDavPath(c.raw)
			if err != nil {
				t.Fatalf("%s: %v", c.raw, err)
			}
			if got.Layout != c.layout {
				t.Errorf("layout %s, want %s", got.Layout, c.layout)
			}
			if got.User != c.user {
				t.Errorf("user %q, want %q", got.User, c.user)
			}
			if strings.Join(got.Path, "/") != c.path {
				t.Errorf("path %q, want %q", got.Path, c.path)
			}
			if got.Transfer != c.transfer {
				t.Errorf("transfer %q, want %q", got.Transfer, c.transfer)
			}
			if got.Member != c.member {
				t.Errorf("member %q, want %q", got.Member, c.member)
			}
		})
	}
}

// The vendor layouts use the shared decoder, so an encoded separator or a
// traversal is refused here exactly as on the native mount. A second parser is
// a second answer to the same security question.
func TestTheVendorPathsUseTheSharedDecoder(t *testing.T) {
	cases := []string{
		"/remote.php/dav/files/bob/a%2fb",
		"/remote.php/dav/files/bob/..",
		"/remote.php/dav/files/bob/%2e%2e/etc",
		"/remote.php/dav/files/bob/a%00b",
		"/remote.php/webdav/%2e%2e",
		"/remote.php/dav/uploads/bob/t%2f1",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseDavPath(raw); err == nil {
				t.Errorf("%s was accepted", raw)
			}
		})
	}
}

// A path outside the vendor prefixes is not this parser's, and a vendor prefix
// with a shape nothing serves is refused rather than guessed at.
func TestAnUnservedShapeIsRefused(t *testing.T) {
	notMine := []string{"/api/v1/files", "/dav/files/bob", "/", "/remote.phpx/dav"}
	for _, raw := range notMine {
		if _, err := ParseDavPath(raw); !errors.Is(err, ErrNotCompatPath) {
			t.Errorf("%s: want a not-mine refusal, got %v", raw, err)
		}
	}

	unknown := []string{
		"/remote.php",
		"/remote.php/dav",
		"/remote.php/dav/files",
		"/remote.php/dav/uploads",
		"/remote.php/dav/uploads/bob",
		"/remote.php/dav/trashbin/bob",
		"/remote.php/dav/trashbin/bob/other",
		"/remote.php/dav/comments/x",
		"/remote.php/other",
	}
	for _, raw := range unknown {
		if _, err := ParseDavPath(raw); !errors.Is(err, ErrUnknownLayout) {
			t.Errorf("%s: want an unknown-layout refusal, got %v", raw, err)
		}
	}
}

// Deeper nesting under a bounded layout is refused rather than truncated to
// the part that fits. Truncating serves a path the client did not ask for.
func TestDeeperNestingIsRefusedNotTruncated(t *testing.T) {
	cases := []string{
		"/remote.php/dav/uploads/bob/t1/5/extra",
		"/remote.php/dav/uploads/bob/t1/5/extra/more",
		"/remote.php/dav/trashbin/bob/trash/e1/extra",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseDavPath(raw); !errors.Is(err, ErrUnknownLayout) {
				t.Errorf("%s was accepted or misparsed: %v", raw, err)
			}
		})
	}
}

// The username segment is recorded and never selects a principal: whoever
// mounts this resolves the caller's own tree. The parser's job is to report
// what the URL said, so a test at this layer can only check it is carried and
// not consulted, which the type makes plain.
func TestTheUserSegmentIsCarriedVerbatim(t *testing.T) {
	got, err := ParseDavPath("/remote.php/dav/files/someone.else/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "someone.else" {
		t.Errorf("user %q", got.User)
	}
	if strings.Join(got.Path, "/") != "x" {
		t.Errorf("path %q", got.Path)
	}
}

// A transfer id comes from the client, so its alphabet is bounded and the dot
// spellings are excluded: those name a directory rather than a transfer.
func TestTheTransferIDAlphabet(t *testing.T) {
	good := []string{"a", "1", "abc-123_x.y", strings.Repeat("a", 128), "..a", "a.."}
	for _, id := range good {
		if err := ValidTransferID(id); err != nil {
			t.Errorf("%q was refused: %v", id, err)
		}
	}

	bad := []string{
		"",
		".",
		"..",
		strings.Repeat("a", 129),
		"a/b",
		"a b",
		"a\x00b",
		"a:b",
		"\u00e9",
		"a%2fb",
	}
	for _, id := range bad {
		if err := ValidTransferID(id); !errors.Is(err, ErrBadTransferID) {
			t.Errorf("%q was accepted", id)
		}
	}
}

// A bad transfer id refuses the whole path rather than being carried through.
func TestABadTransferIDRefusesThePath(t *testing.T) {
	if _, err := ParseDavPath("/remote.php/dav/uploads/bob/" + strings.Repeat("a", 200)); !errors.Is(err, ErrBadTransferID) {
		t.Error("an oversized transfer id was accepted")
	}
}

// The assembly member is one exact name, so a chunk literally called .file
// cannot be written and then assembled as itself.
func TestTheAssemblyMemberIsExact(t *testing.T) {
	if !IsAssembly(".file") {
		t.Error(".file is not recognised as the assembly member")
	}
	for _, name := range []string{"file", ".files", ".File", "..file", "1"} {
		if IsAssembly(name) {
			t.Errorf("%q was read as the assembly member", name)
		}
	}
}

// The letter order is fixed and clients read it positionally. A missing letter
// changes what a client offers: no W hides editing, no CK refuses upload into
// a directory, no D hides delete.
func TestThePermissionLetterOrder(t *testing.T) {
	all := CanRead | CanWrite | CanCreate | CanDelete | CanRename | CanMove | CanShare

	cases := []struct {
		name string
		p    Perms
		want string
	}{
		{"a fully permitted file", Perms{Allowed: all, Shareable: true}, "SRDNVW"},
		{"a fully permitted directory", Perms{Allowed: all, Shareable: true, Directory: true}, "SRDNVCK"},
		{"a received share root", Perms{Allowed: all, Shareable: true, Mounted: true}, "SGDNVW"},
		{"sharing off entirely", Perms{Allowed: all}, "DNVW"},
		{"read only", Perms{Allowed: CanRead, Shareable: true}, ""},
		{"read only directory", Perms{Allowed: CanRead, Shareable: true, Directory: true}, ""},
		{"writable file only", Perms{Allowed: CanRead | CanWrite}, "W"},
		{"creatable directory only", Perms{Allowed: CanRead | CanCreate, Directory: true}, "CK"},
		{"delete only", Perms{Allowed: CanDelete}, "D"},
		{"rename and move", Perms{Allowed: CanRename | CanMove}, "NV"},
		{"nothing at all", Perms{}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PermissionLetters(c.p); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// M is never emitted. It marks a resource mounted from elsewhere, and this
// server has no federation, so claiming it makes a client look for a remote it
// cannot reach.
func TestMIsNeverEmitted(t *testing.T) {
	all := CanRead | CanWrite | CanCreate | CanDelete | CanRename | CanMove | CanShare

	for _, shareable := range []bool{false, true} {
		for _, mounted := range []bool{false, true} {
			for _, dir := range []bool{false, true} {
				for allowed := Ability(0); allowed <= all; allowed++ {
					got := PermissionLetters(Perms{
						Allowed: allowed, Shareable: shareable,
						Mounted: mounted, Directory: dir,
					})
					if strings.Contains(got, "M") {
						t.Fatalf("M appeared in %q", got)
					}
				}
			}
		}
	}
}

// The letters only ever come from the fixed alphabet, in the fixed order, with
// no repeats. Checked over every input combination rather than sampled.
func TestTheLetterStringIsAlwaysWellFormed(t *testing.T) {
	const order = "SRGDNVWCK"
	all := CanRead | CanWrite | CanCreate | CanDelete | CanRename | CanMove | CanShare

	for _, shareable := range []bool{false, true} {
		for _, mounted := range []bool{false, true} {
			for _, dir := range []bool{false, true} {
				for allowed := Ability(0); allowed <= all; allowed++ {
					got := PermissionLetters(Perms{
						Allowed: allowed, Shareable: shareable,
						Mounted: mounted, Directory: dir,
					})

					last := -1
					seen := map[rune]bool{}
					for _, r := range got {
						at := strings.IndexRune(order, r)
						if at < 0 {
							t.Fatalf("%q holds a letter outside the alphabet", got)
						}
						if at <= last {
							t.Fatalf("%q is out of order", got)
						}
						if seen[r] {
							t.Fatalf("%q repeats a letter", got)
						}
						seen[r] = true
						last = at
					}
				}
			}
		}
	}
}

// A received share root is G rather than R: the reshare bit says a caller may
// pass on what it holds, and grant chains are not offered.
func TestAReceivedRootIsNotResharable(t *testing.T) {
	p := Perms{Allowed: CanRead | CanShare, Shareable: true, Mounted: true}

	got := PermissionLetters(p)
	if strings.Contains(got, "R") {
		t.Errorf("a received root reported reshare: %q", got)
	}
	if !strings.Contains(got, "G") {
		t.Errorf("a received root did not report G: %q", got)
	}
}

// Time truncates rather than rounds. A rounded modification time can land in
// the future, and a client comparing it against its clock re-uploads a file it
// already has.
func TestTimeTruncatesRatherThanRounds(t *testing.T) {
	cases := map[string]int64{
		"1700000000":     1700000000,
		"1700000000.0":   1700000000,
		"1700000000.4":   1700000000,
		"1700000000.5":   1700000000,
		"1700000000.999": 1700000000,
		"0":              0,
		"0.999":          0,
		"-5":             -5,
		"-5.9":           -5,
		"+7":             7,
		" 1700000000 ":   1700000000,
	}

	for raw, want := range cases {
		got, err := ParseSeconds(raw)
		if err != nil {
			t.Errorf("%q was refused: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q gave %d, want %d", raw, got, want)
		}
	}
}

// Exponent notation and anything that will not fit refuse. A time this server
// cannot represent is not a time it should store.
func TestAnUnrepresentableTimeRefuses(t *testing.T) {
	bad := []string{
		"",
		" ",
		"abc",
		"1e3",
		"1.5e3",
		"1.",
		".5",
		"1.2.3",
		"0x10",
		"1 2",
		"99999999999999999999",
		"-",
		"+",
		"1,5",
	}

	for _, raw := range bad {
		if _, err := ParseSeconds(raw); !errors.Is(err, ErrBadTime) {
			t.Errorf("%q was accepted", raw)
		}
	}
}

// A count too large for a signed wire field clamps rather than wrapping. A
// wrapped value reads as negative free space, and an Android client that sees
// that parks every upload it has queued.
func TestAnOversizedCountClampsRatherThanWrapping(t *testing.T) {
	cases := map[uint64]int64{
		0:                    0,
		1:                    1,
		math.MaxInt64:        math.MaxInt64,
		math.MaxInt64 + 1:    math.MaxInt64,
		math.MaxUint64:       math.MaxInt64,
		math.MaxUint64 - 100: math.MaxInt64,
	}

	for in, want := range cases {
		got := ClampToInt64(in)
		if got != want {
			t.Errorf("%d gave %d, want %d", in, got, want)
		}
		if got < 0 {
			t.Errorf("%d produced a negative %d", in, got)
		}
	}
}

// Whatever the input, the parser never panics and never returns a value it
// then cannot render back.
func FuzzParseSeconds(f *testing.F) {
	for _, seed := range []string{"1", "1.5", "-1", "1e3", "", "999999999999999999999", "0.0"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := ParseSeconds(raw)
		if err != nil {
			return
		}
		// An accepted value is a real second count, so re-parsing its own
		// decimal rendering gives the same number back.
		again, err := ParseSeconds(formatInt(got))
		if err != nil {
			t.Errorf("%q parsed to %d, which its own parser refuses: %v", raw, got, err)
		}
		if again != got {
			t.Errorf("%q parsed to %d and back to %d", raw, got, again)
		}
	})
}

// formatInt renders a value the way the parser reads it.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	var digits []byte
	for n != 0 {
		d := n % 10
		if d < 0 {
			d = -d
		}
		digits = append([]byte{byte('0' + d)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// The path parser never panics and never returns a layout whose fields do not
// match it.
func FuzzParseDavPath(f *testing.F) {
	for _, seed := range []string{
		"/remote.php/dav/files/bob/x",
		"/index.php/remote.php/webdav/y",
		"/remote.php/dav/uploads/bob/t1/5",
		"/remote.php/dav/trashbin/bob/trash",
		"/remote.php/dav/principals",
		"/remote.php/dav/files/bob/a%2fb",
		"/",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := ParseDavPath(raw)
		if err != nil {
			return
		}

		if got.Layout == LayoutUnset {
			t.Errorf("%q parsed to no layout", raw)
		}
		if got.Layout != LayoutUploads && (got.Transfer != "" || got.Member != "") {
			t.Errorf("%q gave layout %s carrying upload fields", raw, got.Layout)
		}
		if got.Transfer != "" {
			if err := ValidTransferID(got.Transfer); err != nil {
				t.Errorf("%q produced an unusable transfer id %q", raw, got.Transfer)
			}
		}
		for _, seg := range got.Path {
			switch {
			case seg == "", seg == ".", seg == "..":
				t.Errorf("%q produced segment %q", raw, seg)
			case strings.Contains(seg, "/"):
				t.Errorf("%q produced a segment with a separator", raw)
			}
		}
	})
}

// The two mounts agree about every chunk name, because there is one parser.
// Two parsers disagreeing about whether "00001" and "1" are the same member is
// how a client writes a chunk twice and reads one of them back.
func TestBothMountsAgreeOnEveryChunkName(t *testing.T) {
	names := []string{
		"1", "9", "10", "10000", "0", "10001",
		"00001", "01", "000", "", " 1", "1 ", "+1", "-1", "1.0", "0x1", "1e3",
		".file", "abc", "\u0661", "99999999999999999999",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			viaCompat, compatErr := ParseChunk(name)
			viaDav, davErr := dav.ParseChunkName(name, ChunkRange())

			if (compatErr == nil) != (davErr == nil) {
				t.Fatalf("%q: compat %v, dav %v", name, compatErr, davErr)
			}
			if compatErr == nil && viaCompat != viaDav {
				t.Errorf("%q: compat %d, dav %d", name, viaCompat, viaDav)
			}
		})
	}
}

// The reference client numbers chunks from one, so zero is outside the range
// even though the parser accepts it for a collection that admits it.
func TestTheVendorChunkRangeStartsAtOne(t *testing.T) {
	if _, err := ParseChunk("0"); !errors.Is(err, dav.ErrChunkRange) {
		t.Errorf("zero was accepted: %v", err)
	}
	if _, err := ParseChunk("1"); err != nil {
		t.Errorf("one was refused: %v", err)
	}
	if _, err := ParseChunk("10000"); err != nil {
		t.Errorf("ten thousand was refused: %v", err)
	}
	if _, err := ParseChunk("10001"); !errors.Is(err, dav.ErrChunkRange) {
		t.Errorf("past the range was accepted: %v", err)
	}
}

// A padded name is refused rather than accepted as an alias, on this mount as
// on the other.
func TestAPaddedVendorChunkNameIsRefused(t *testing.T) {
	for _, name := range []string{"00001", "01", "0001"} {
		if _, err := ParseChunk(name); !errors.Is(err, dav.ErrChunkLeadingZero) {
			t.Errorf("%q was accepted: %v", name, err)
		}
	}
}

// The assembly member is not a chunk number.
func TestTheAssemblyMemberIsNotAChunk(t *testing.T) {
	if _, err := ParseChunk(AssemblyMember); err == nil {
		t.Error(".file parsed as a chunk number")
	}
}
