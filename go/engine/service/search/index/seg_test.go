package index

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFrameGoldenLayout(t *testing.T) {
	payload := []byte("hello")
	framed, err := Frame(payload)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if len(framed) != FrameHeader+len(payload) {
		t.Fatalf("framed length is %d, want %d", len(framed), FrameHeader+len(payload))
	}
	if n := int(binary.LittleEndian.Uint32(framed[0:])); n != len(payload) {
		t.Errorf("length prefix is %d, want %d", n, len(payload))
	}
	if sum := binary.LittleEndian.Uint32(framed[4:]); sum != FNV1a32(payload) {
		t.Errorf("checksum is %d, want %d", sum, FNV1a32(payload))
	}
	if string(framed[FrameHeader:]) != "hello" {
		t.Errorf("body is %q", framed[FrameHeader:])
	}
}

// FNV1a32 is the corruption checksum and stays its own primitive, distinct
// from the sketch's Hash64. This pins its published constants.
func TestFNV1a32KnownVectors(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"", 0x811c9dc5},
		{"a", 0xe40c292c},
		{"foobar", 0xbf9cf968},
	}
	for _, c := range cases {
		if got := FNV1a32([]byte(c.in)); got != c.want {
			t.Errorf("FNV1a32(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

func TestAppendAndReadRecordsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delta.000.idx")

	want := [][]Entry{
		{{Share: 1, Path: "a.txt"}},
		{{Share: 1, Path: "b.txt"}, {Share: 2, Path: "c.txt"}},
	}
	for i, e := range want {
		payload, err := EncodePayload(uint64(i), e)
		if err != nil {
			t.Fatalf("EncodePayload: %v", err)
		}
		if _, err := AppendRecord(path, payload); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}

	rec, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if rec.Torn {
		t.Error("a file this test wrote reported a torn tail")
	}
	if len(rec.Records) != len(want) {
		t.Fatalf("read %d records, want %d", len(rec.Records), len(want))
	}
	for i, raw := range rec.Records {
		seq, got, derr := DecodePayload(raw)
		if derr != nil {
			t.Fatalf("DecodePayload: %v", derr)
		}
		if seq != uint64(i) {
			t.Errorf("record %d carries sequence %d", i, seq)
		}
		if !slices.Equal(got, want[i]) {
			t.Errorf("record %d decoded to %v, want %v", i, got, want[i])
		}
	}
}

// A missing segment is not an error: an index directory with no delta yet is
// the normal state before the first write.
func TestReadRecordsOnAMissingFile(t *testing.T) {
	rec, err := ReadRecords(filepath.Join(t.TempDir(), "absent.idx"))
	if err != nil {
		t.Fatalf("ReadRecords on a missing file: %v", err)
	}
	if len(rec.Records) != 0 || rec.Torn || rec.GoodLen != 0 {
		t.Errorf("a missing file reported %+v", rec)
	}
}

// A torn tail is the expected state after a crash, not corruption. The prefix
// survives, the tail is reported, and truncation cuts it back.
func TestATornTailIsTruncatedAndThePrefixSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delta.000.idx")
	payload, err := EncodePayload(1, []Entry{{Share: 1, Path: "kept.txt"}})
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	written, err := AppendRecord(path, payload)
	if err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}

	// A second record that only partly reached the disk.
	torn, err := Frame([]byte("this record never finished"))
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, werr := f.Write(torn[:len(torn)-5]); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	rec, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if !rec.Torn {
		t.Error("the torn tail was not reported")
	}
	if len(rec.Records) != 1 {
		t.Fatalf("read %d intact records, want 1", len(rec.Records))
	}
	if rec.GoodLen != written {
		t.Errorf("intact prefix is %d bytes, want %d", rec.GoodLen, written)
	}

	if terr := TruncateTo(path, rec.GoodLen); terr != nil {
		t.Fatalf("TruncateTo: %v", terr)
	}
	after, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords after truncation: %v", err)
	}
	if after.Torn || len(after.Records) != 1 {
		t.Errorf("after truncation: torn %v, %d records", after.Torn, len(after.Records))
	}
}

// A bit flip in a record body fails its checksum, and the scan stops there
// rather than serving what the corrupted bytes decode to.
func TestABitFlipInARecordStopsTheScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delta.000.idx")
	for i := range 3 {
		payload, err := EncodePayload(uint64(i), []Entry{{Share: 1, Path: "f.txt"}})
		if err != nil {
			t.Fatalf("EncodePayload: %v", err)
		}
		if _, err := AppendRecord(path, payload); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Corrupt the first record's body, past its header.
	buf[FrameHeader+1] ^= 0xff
	//nolint:gosec // G703: this test's own TempDir and a fixed name.
	if werr := os.WriteFile(path, buf, 0o600); werr != nil {
		t.Fatalf("write: %v", werr)
	}

	rec, rerr := ReadRecords(path)
	if rerr != nil {
		t.Fatalf("ReadRecords: %v", rerr)
	}
	if len(rec.Records) != 0 {
		t.Errorf("read %d records past a failed checksum, want 0", len(rec.Records))
	}
	if !rec.Torn {
		t.Error("a failed checksum should leave the rest of the file unread")
	}
}

// The length prefix is read off disk before the body, so a corrupt four bytes
// must not ask for an allocation of whatever they happened to say.
func TestReadRecordsRefusesAnOversizedLengthPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delta.000.idx")
	var buf []byte
	buf = binary.LittleEndian.AppendUint32(buf, MaxRecord+1)
	buf = binary.LittleEndian.AppendUint32(buf, 0)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(rec.Records) != 0 {
		t.Error("a length past the ceiling produced a record")
	}
}

// A record body is compressed only when that made it smaller, because
// compressing unconditionally would grow the small records that dominate a
// delta segment.
func TestPayloadTakesTheSmallerOfRawAndCompressed(t *testing.T) {
	small, err := EncodePayload(1, []Entry{{Share: 1, Path: "a"}})
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	if small[0] != codecRaw {
		t.Errorf("a tiny record was compressed (tag %d)", small[0])
	}

	var many []Entry
	for i := range 500 {
		many = append(many, Entry{Share: 1, Path: "a-very-repetitive-directory-name/file.txt" + string(rune(i))})
	}
	big, err := EncodePayload(2, many)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	if big[0] != codecZstd {
		t.Errorf("a highly repetitive record was left raw (tag %d)", big[0])
	}

	seq, got, err := DecodePayload(big)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if seq != 2 || len(got) != len(many) {
		t.Errorf("round trip gave sequence %d and %d entries", seq, len(got))
	}
}

func TestDecodePayloadRefusesMalformedRecords(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"unknown codec", []byte{9, 0, 0}},
		{"truncated sequence", []byte{codecRaw, 0x80}},
		{"a count past what the body holds", []byte{codecRaw, 0x01, 0xff, 0x7f}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := DecodePayload(c.in); err == nil {
				t.Error("a malformed record decoded")
			} else if !errors.Is(err, ErrCorrupt) {
				t.Errorf("got %v, want ErrCorrupt", err)
			}
		})
	}
}

func FuzzReadRecordsNeverPanics(f *testing.F) {
	payload, err := EncodePayload(1, []Entry{{Share: 1, Path: "a.txt"}})
	if err != nil {
		f.Fatalf("EncodePayload: %v", err)
	}
	framed, err := Frame(payload)
	if err != nil {
		f.Fatalf("Frame: %v", err)
	}
	f.Add(framed)
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, in []byte) {
		path := filepath.Join(t.TempDir(), "delta.000.idx")
		//nolint:gosec // G703: this test's own TempDir and a fixed name.
		if werr := os.WriteFile(path, in, 0o600); werr != nil {
			t.Skip("could not stage the fixture")
		}
		rec, err := ReadRecords(path)
		if err != nil {
			return
		}
		if rec.GoodLen < 0 || rec.GoodLen > int64(len(in)) {
			t.Errorf("intact prefix of %d reported for a %d byte file", rec.GoodLen, len(in))
		}
		for _, raw := range rec.Records {
			// A record whose checksum verified must decode, or the reader
			// accepted bytes the writer could not have produced.
			if _, _, derr := DecodePayload(raw); derr != nil {
				t.Errorf("a checksummed record did not decode: %v", derr)
			}
		}
	})
}

func FuzzDecodePayloadNeverPanics(f *testing.F) {
	f.Add([]byte{codecRaw, 0x01, 0x00})
	f.Add([]byte{codecZstd, 0x00})
	f.Fuzz(func(t *testing.T, in []byte) {
		if _, _, err := DecodePayload(in); err != nil && !errors.Is(err, ErrCorrupt) {
			t.Errorf("DecodePayload returned a non-corrupt error: %v", err)
		}
	})
}
