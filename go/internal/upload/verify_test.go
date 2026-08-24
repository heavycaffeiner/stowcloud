//go:build linux

package upload

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// The wire parsers are this package's trust boundary: every value here arrives
// in a header or a URL that a client wrote. They are proved against the shapes
// a hostile client sends, not just the one a working client does.

func TestParseChecksumTakesBothAdvertisedAlgorithms(t *testing.T) {
	body := []byte("the quick brown fox")
	for _, algo := range Algorithms() {
		digest := Sum(algo, body)
		header := algo.String() + " " + base64.StdEncoding.EncodeToString(digest)

		got, err := ParseChecksum(header)
		if err != nil {
			t.Fatalf("ParseChecksum(%q): %v", header, err)
		}
		if got.Algo != algo {
			t.Fatalf("parsed algorithm %v, want %v", got.Algo, algo)
		}
		if !constantTimeEqual(got.Digest, digest) {
			t.Fatalf("the parsed digest does not round-trip for %v", algo)
		}
	}
}

func TestParseChecksumRefusesWhatCannotBeOne(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(Sum(AlgoCRC32C, []byte("x")))
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"no separator", "crc32c" + good},
		{"an algorithm this server does not offer", "md5 " + good},
		{"a digest that is not base64", "crc32c not-base64!!"},
		{"empty", ""},
		// A short digest would otherwise be compared against a truncation of
		// the real one and pass, which is a checksum that cannot fail.
		{"a digest of the wrong length", "crc32c " + base64.StdEncoding.EncodeToString([]byte{1, 2})},
		{"a blake3 digest under a crc32c name", "crc32c " +
			base64.StdEncoding.EncodeToString(Sum(AlgoBLAKE3, []byte("x")))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseChecksum(tc.header); err == nil {
				t.Fatalf("ParseChecksum(%q) was accepted", tc.header)
			}
		})
	}
}

func TestParseAlgoIsCaseInsensitiveAndRefusesTheRest(t *testing.T) {
	for _, spelling := range []string{"crc32c", "CRC32C", " Crc32c "} {
		if got, err := ParseAlgo(spelling); err != nil || got != AlgoCRC32C {
			t.Fatalf("ParseAlgo(%q) = %v, %v", spelling, got, err)
		}
	}
	if _, err := ParseAlgo("sha256"); !errors.Is(err, ErrUnknownAlgo) {
		t.Fatalf("an algorithm this server does not offer returned %v, want ErrUnknownAlgo", err)
	}
}

// A session id is the whole of a TUS URL, so a wrong length is refused rather
// than padded into something that addresses a different session.
func TestSessionIDRoundTripsAndRefusesAWrongLength(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	back, perr := ParseSessionID(id.String())
	if perr != nil {
		t.Fatalf("ParseSessionID: %v", perr)
	}
	if back != id {
		t.Fatalf("the id did not round-trip: %s to %s", id, back)
	}

	for _, bad := range []string{"", "short", strings.Repeat("A", 64), "not base64url!!"} {
		if _, err := ParseSessionID(bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ParseSessionID(%q) returned %v, want ErrNotFound", bad, err)
		}
	}
}

// Two ids minted in a row must differ, or the handle is not the unguessable
// thing standing between a request and somebody else's upload.
func TestSessionIDsAreDistinct(t *testing.T) {
	seen := map[SessionID]bool{}
	for i := 0; i < 64; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if seen[id] {
			t.Fatalf("a session id repeated after %d draws", i)
		}
		seen[id] = true
	}
}

// The part file's name is exactly the reserved prefix and the id. An earlier
// revision of this design disguised it to get past component validation, which
// defeated the reserved-name filter and put part files in every listing.
func TestThePartNameCarriesTheReservedPrefixAndNothingElse(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	name := partName(id)
	if want := ".scpart-" + id.String(); name != want {
		t.Fatalf("the part name is %q, want %q", name, want)
	}
	for _, n := range []string{name, spoolDirName(id), chunkFileName(1)} {
		if !strings.HasPrefix(n, ".scpart-") {
			t.Fatalf("%q does not carry the control prefix, so a listing would show it", n)
		}
	}
}

// Chunk names sort the same way as numbers, which is what "assembled in the
// order of their names" depends on.
func TestChunkNamesSortInNumericOrder(t *testing.T) {
	prev := chunkFileName(0)
	for _, n := range []uint32{1, 2, 9, 10, 255, 256, 65535, 65536, 1 << 31} {
		got := chunkFileName(n)
		if got <= prev {
			t.Fatalf("chunk %d sorts to %q, which is not after %q", n, got, prev)
		}
		prev = got
	}
}
