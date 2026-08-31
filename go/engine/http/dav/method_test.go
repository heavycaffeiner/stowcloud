//go:build linux

package dav

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// A Destination naming another host is refused. Following it would make the
// server fetch or write against a host the caller chose.
func TestAForeignDestinationIsRefused(t *testing.T) {
	cases := []string{
		"http://evil.example/dav/x",
		"https://evil.example/dav/x",
		"http://dav.example.com.evil.example/x",
		"http://dav.example:8081/x",
	}

	for _, dest := range cases {
		t.Run(dest, func(t *testing.T) {
			if _, err := ParseDestination(dest, "dav.example:8080"); !errors.Is(err, ErrForeignDestination) {
				t.Errorf("%q: want a foreign-host refusal, got %v", dest, err)
			}
		})
	}
}

// The scheme is not compared. A reverse proxy terminating TLS makes the server
// see http where the client wrote https, so comparing schemes would refuse
// every COPY behind one.
func TestTheSchemeIsNotCompared(t *testing.T) {
	for _, dest := range []string{"https://dav.example/target", "http://dav.example/target"} {
		got, err := ParseDestination(dest, "dav.example")
		if err != nil {
			t.Errorf("%q was refused: %v", dest, err)
			continue
		}
		if len(got) != 1 || got[0] != "target" {
			t.Errorf("%q gave %q, want [target]", dest, got)
		}
	}
}

// A host comparison is case insensitive, since DNS is.
func TestTheHostComparisonIsCaseInsensitive(t *testing.T) {
	if _, err := ParseDestination("http://DAV.Example/x", "dav.example"); err != nil {
		t.Errorf("a differently cased host was refused: %v", err)
	}
}

// The destination path goes through the same decoder as the request path. A
// second decoder is a second set of rules and a place for them to disagree.
func TestTheDestinationUsesTheSameDecoder(t *testing.T) {
	for _, dest := range []string{"/a%2fb", "http://h/a%2fb", "/..", "/a%00"} {
		t.Run(dest, func(t *testing.T) {
			if _, err := ParseDestination(dest, "h"); err == nil {
				t.Errorf("%q was accepted; the request decoder refuses it", dest)
			}
		})
	}
}

// A missing or unusable Destination is refused rather than defaulted.
func TestAnUnusableDestinationIsRefused(t *testing.T) {
	for _, dest := range []string{"", "   ", "relative/path", "?query"} {
		t.Run(dest, func(t *testing.T) {
			if _, err := ParseDestination(dest, "h"); !errors.Is(err, ErrNoDestination) {
				t.Errorf("%q: want a missing-destination refusal, got %v", dest, err)
			}
		})
	}
}

// Only "F" means do not overwrite. Anything else, including a typo, means the
// header's default, which is to overwrite.
func TestOnlyFMeansNoOverwrite(t *testing.T) {
	for _, no := range []string{"F", "f", " F "} {
		if Overwrite(no) {
			t.Errorf("%q was read as overwrite", no)
		}
	}
	for _, yes := range []string{"", "T", "t", "false", "no", "0"} {
		if !Overwrite(yes) {
			t.Errorf("%q was read as no-overwrite", yes)
		}
	}
}

// MOVE differs from COPY by removing the source. That difference is exactly
// the Delete bit on the source endpoint, and nothing else.
func TestMoveDiffersFromCopyByTheSourceDelete(t *testing.T) {
	cp, ok := MethodRequirement("COPY")
	if !ok {
		t.Fatal("COPY is not served")
	}
	mv, ok := MethodRequirement("MOVE")
	if !ok {
		t.Fatal("MOVE is not served")
	}

	if cp.Source&acl.Delete != 0 {
		t.Error("COPY requires Delete on the source, but it removes nothing")
	}
	if mv.Source&acl.Delete == 0 {
		t.Error("MOVE does not require Delete on the source it removes")
	}
	if cp.Dest != mv.Dest {
		t.Errorf("the two methods write their destination differently: %v against %v", cp.Dest, mv.Dest)
	}
}

// A read method never demands a write bit, and a write method never runs on
// read alone. This is the whole table, checked as a property.
func TestReadMethodsNeverDemandWrite(t *testing.T) {
	readOnly := []string{"GET", "HEAD", "PROPFIND", "SEARCH", "REPORT"}
	writing := []string{"PUT", "MKCOL", "PROPPATCH", "LOCK", "UNLOCK", "DELETE", "MOVE"}

	const mutating = acl.Write | acl.Create | acl.Delete

	for _, m := range readOnly {
		req, ok := MethodRequirement(m)
		if !ok {
			t.Errorf("%s is not served", m)
			continue
		}
		if req.Source&mutating != 0 {
			t.Errorf("%s requires a mutating permission on its source", m)
		}
		if req.HasDest() {
			t.Errorf("%s declares a destination endpoint", m)
		}
	}

	for _, m := range writing {
		req, ok := MethodRequirement(m)
		if !ok {
			t.Errorf("%s is not served", m)
			continue
		}
		if req.Source&mutating == 0 {
			t.Errorf("%s mutates but requires no mutating permission", m)
		}
	}
}

// Only COPY and MOVE address a second endpoint.
func TestOnlyCopyAndMoveHaveADestination(t *testing.T) {
	for _, m := range Methods() {
		req, ok := MethodRequirement(m)
		if !ok {
			continue
		}
		wantDest := m == "COPY" || m == "MOVE"
		if req.HasDest() != wantDest {
			t.Errorf("%s: destination endpoint %v, want %v", m, req.HasDest(), wantDest)
		}
	}
}

// An unregistered vocabulary is not advertised. A client reading Allow would
// otherwise send a method the server refuses.
func TestAllowAdvertisesOnlyRegisteredVocabularies(t *testing.T) {
	base := AllowHeader(AllowSet{Locking: true})
	for _, m := range []string{"SEARCH", "REPORT"} {
		if strings.Contains(base, m) {
			t.Errorf("%s is advertised with nothing registered: %q", m, base)
		}
	}

	with := AllowHeader(AllowSet{Locking: true, Extra: []string{"SEARCH"}})
	if !strings.Contains(with, "SEARCH") {
		t.Errorf("SEARCH was registered but is not advertised: %q", with)
	}
	if strings.Contains(with, "REPORT") {
		t.Errorf("REPORT is advertised without being registered: %q", with)
	}
}

// A name Allow does not know is not smuggled into the header.
func TestAllowIgnoresAnUnknownMethod(t *testing.T) {
	got := AllowHeader(AllowSet{Extra: []string{"BREW", "SEARCH"}})
	if strings.Contains(got, "BREW") {
		t.Errorf("an unknown method reached the header: %q", got)
	}
}

// The base set always carries the methods every mount serves.
func TestAllowCarriesTheBaseMethods(t *testing.T) {
	got := AllowHeader(AllowSet{Locking: true})
	for _, m := range []string{"OPTIONS", "GET", "HEAD", "PROPFIND", "DELETE", "COPY", "MOVE", "LOCK", "UNLOCK"} {
		if !strings.Contains(got, m) {
			t.Errorf("%s is missing from %q", m, got)
		}
	}
}

// A collection takes MKCOL and a file takes PUT, and neither takes the other.
//
// A client reads Allow to decide what to send. Offering PUT on a collection
// invites a request that has to be refused, and offering MKCOL on a file
// invites one that would have to remove the file first.
func TestAllowSeparatesCollectionsFromFiles(t *testing.T) {
	dir := AllowHeader(AllowSet{Exists: true, IsDir: true})
	if strings.Contains(dir, "PUT") {
		t.Errorf("a collection advertises PUT: %q", dir)
	}
	if !strings.Contains(dir, "MKCOL") {
		t.Errorf("a collection does not advertise MKCOL: %q", dir)
	}

	file := AllowHeader(AllowSet{Exists: true})
	if !strings.Contains(file, "PUT") {
		t.Errorf("a file does not advertise PUT: %q", file)
	}
	if strings.Contains(file, "MKCOL") {
		t.Errorf("a file advertises MKCOL: %q", file)
	}
}

// A path nothing occupies advertises both, because either would create.
func TestAllowOffersBothCreateMethodsOnAnEmptyPath(t *testing.T) {
	got := AllowHeader(AllowSet{})
	for _, m := range []string{"PUT", "MKCOL"} {
		if !strings.Contains(got, m) {
			t.Errorf("%s is missing for a path nothing occupies: %q", m, got)
		}
	}
}

// A deployment with no lock table does not advertise locking. A client told it
// may LOCK takes one it believes is recorded and writes on the strength of it.
func TestAllowHidesLockingWithoutATable(t *testing.T) {
	got := AllowHeader(AllowSet{Exists: true})
	for _, m := range []string{"LOCK", "UNLOCK"} {
		if strings.Contains(got, m) {
			t.Errorf("%s is advertised with no lock table: %q", m, got)
		}
	}
}

// Canonical decimal: one number, one name. Every padded spelling is refused
// rather than accepted as an alias for the same chunk.
func TestOnlyOneSpellingOfAChunkNumber(t *testing.T) {
	r := ChunkRange{Min: 1, Max: 10000}

	for _, name := range []string{"00001", "01", "0001", "000"} {
		if _, err := ParseChunkName(name, r); !errors.Is(err, ErrChunkLeadingZero) {
			t.Errorf("%q: want a leading-zero refusal, got %v", name, err)
		}
	}

	for _, name := range []string{"", " 1", "1 ", "+1", "-1", "1.0", "0x1", "1e3", "\u0661"} {
		if _, err := ParseChunkName(name, r); !errors.Is(err, ErrChunkNotDecimal) {
			t.Errorf("%q: want a non-decimal refusal, got %v", name, err)
		}
	}

	for _, name := range []string{"0", "10001", "99999999999999999999"} {
		if _, err := ParseChunkName(name, r); !errors.Is(err, ErrChunkRange) {
			t.Errorf("%q: want a range refusal, got %v", name, err)
		}
	}

	for _, name := range []string{"1", "9", "10", "10000"} {
		if _, err := ParseChunkName(name, r); err != nil {
			t.Errorf("%q was refused: %v", name, err)
		}
	}
}

// Zero is spelled "0" where a collection admits it, and the leading-zero rule
// does not refuse the number zero itself.
func TestZeroIsSpelledZeroWhereItIsAllowed(t *testing.T) {
	r := ChunkRange{Min: 0, Max: 9}

	n, err := ParseChunkName("0", r)
	if err != nil {
		t.Fatalf("zero was refused by a collection that admits it: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

// The renderer writes the one spelling the parser takes back.
func TestChunkNamesRoundTrip(t *testing.T) {
	r := ChunkRange{Min: 0, Max: 10000}
	for _, n := range []int64{0, 1, 9, 10, 999, 10000} {
		got, err := ParseChunkName(ChunkName(n), r)
		if err != nil {
			t.Errorf("%d rendered to a name its own parser refuses: %v", n, err)
			continue
		}
		if got != n {
			t.Errorf("%d round tripped to %d", n, got)
		}
	}
}
