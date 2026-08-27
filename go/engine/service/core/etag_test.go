package core

import (
	"encoding/hex"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

func baseStat() vfs.Stat {
	ctime := int64(1_700_000_000_000_000_000)
	return vfs.Stat{
		Dev:     0x10,
		Ino:     0x2000,
		Size:    4096,
		MtimeNs: 1_600_000_000_000_000_000,
		CtimeNs: &ctime,
	}
}

func TestFileETagIsDeterministic(t *testing.T) {
	first, weak := FileETag(baseStat())
	second, _ := FileETag(baseStat())
	if first != second {
		t.Fatalf("two hashes of the same stat differ: %q and %q", first, second)
	}
	if !weak {
		t.Fatal("the token reported itself strong; a metadata-derived token is always weak")
	}
}

func TestFileETagIs32LowercaseHexCharacters(t *testing.T) {
	token, _ := FileETag(baseStat())
	if len(token) != 2*etagBytes {
		t.Fatalf("token %q is %d characters, want %d", token, len(token), 2*etagBytes)
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token %q is not hex: %v", token, err)
	}
	for _, r := range token {
		if r >= 'A' && r <= 'Z' {
			t.Fatalf("token %q is not lowercase", token)
		}
	}
}

func TestFileETagChangesWithEveryHashedField(t *testing.T) {
	otherCtime := int64(1_700_000_000_000_000_001)
	cases := []struct {
		name  string
		apply func(*vfs.Stat)
	}{
		{"dev", func(st *vfs.Stat) { st.Dev++ }},
		{"ino", func(st *vfs.Stat) { st.Ino++ }},
		{"size", func(st *vfs.Stat) { st.Size++ }},
		{"mtime", func(st *vfs.Stat) { st.MtimeNs++ }},
		{"ctime", func(st *vfs.Stat) { st.CtimeNs = &otherCtime }},
	}
	want, _ := FileETag(baseStat())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := baseStat()
			tc.apply(&st)
			got, _ := FileETag(st)
			if got == want {
				t.Fatalf("changing %s left the token at %q", tc.name, got)
			}
		})
	}
}

// A rename moves ctime and leaves mtime alone, which is the whole reason
// ctime is in the input. Without it a moved file and an untouched one carry
// the same token.
func TestFileETagSeesAMoveThatLeftMtimeAlone(t *testing.T) {
	before := baseStat()
	after := baseStat()
	moved := *before.CtimeNs + 1_000_000
	after.CtimeNs = &moved

	first, _ := FileETag(before)
	second, _ := FileETag(after)
	if first == second {
		t.Fatalf("a moved file kept the token %q", first)
	}
	if before.MtimeNs != after.MtimeNs {
		t.Fatal("the fixture changed mtime; the case it proves is a move that did not")
	}
}

func TestFileETagFoldsANilCtimeIntoZero(t *testing.T) {
	zero := int64(0)
	withNil := baseStat()
	withNil.CtimeNs = nil
	withZero := baseStat()
	withZero.CtimeNs = &zero

	a, _ := FileETag(withNil)
	b, _ := FileETag(withZero)
	if a != b {
		t.Fatalf("a nil ctime hashed to %q and a zero ctime to %q; the encoding folds them", a, b)
	}
}

func TestFileETagAcceptsATimestampBeforeTheEpoch(t *testing.T) {
	negative := int64(-1)
	st := baseStat()
	st.MtimeNs = -1_000
	st.CtimeNs = &negative

	token, weak := FileETag(st)
	if len(token) != 2*etagBytes || !weak {
		t.Fatalf("FileETag on a pre-epoch stat = %q, weak %v", token, weak)
	}
	if token == mustToken(t, baseStat()) {
		t.Fatal("a pre-epoch stat hashed to the same token as the base stat")
	}
}

// The identity fields alone decide the token when nothing else differs, so
// two different files never share one.
func TestFileETagSeparatesTwoFilesOnTheSameDevice(t *testing.T) {
	a := baseStat()
	b := baseStat()
	b.Ino = a.Ino + 1
	if mustToken(t, a) == mustToken(t, b) {
		t.Fatal("two inodes on one device hashed to the same token")
	}
}

// The encoders write eight bytes each and nothing overlaps: a value moved
// from one field to the next has to change the token.
func TestFileETagFieldsDoNotAlias(t *testing.T) {
	inDev := baseStat()
	inDev.Dev, inDev.Ino = 1, 0
	inIno := baseStat()
	inIno.Dev, inIno.Ino = 0, 1
	if mustToken(t, inDev) == mustToken(t, inIno) {
		t.Fatal("dev and ino swap produced one token; the layout aliases")
	}
}

func TestPutUint64WritesLittleEndian(t *testing.T) {
	var buf [8]byte
	putUint64(buf[:], 0x0102030405060708)
	want := [8]byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	if buf != want {
		t.Fatalf("putUint64 wrote % x, want % x", buf, want)
	}
}

func TestPutInt64WritesTheBitPattern(t *testing.T) {
	var buf [8]byte
	putInt64(buf[:], -1)
	want := [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if buf != want {
		t.Fatalf("putInt64(-1) wrote % x, want % x", buf, want)
	}
}

func mustToken(t *testing.T, st vfs.Stat) string {
	t.Helper()
	token, _ := FileETag(st)
	return token
}
