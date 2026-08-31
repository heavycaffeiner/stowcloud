package search

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func TestVarintRoundTripGoldens(t *testing.T) {
	cases := []struct {
		v    uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}},
		{math.MaxUint64, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, c := range cases {
		got := PutVarint(nil, c.v)
		if !slices.Equal(got, c.want) {
			t.Errorf("PutVarint(%d) = %x, want %x", c.v, got, c.want)
		}
		back, pos, err := Varint(got, 0)
		if err != nil || back != c.v || pos != len(c.want) {
			t.Errorf("Varint(%x) = %d, %d, %v; want %d, %d, nil", got, back, pos, err, c.v, len(c.want))
		}
		if n := VarintLen(c.v); n != len(c.want) {
			t.Errorf("VarintLen(%d) = %d, want %d", c.v, n, len(c.want))
		}
	}
}

// Only the canonical encoding is accepted. Both refusals below are second
// spellings of a number the encoder never writes, and a decoder that took them
// could not re-encode what it just read, so a corrupt segment would turn into
// plausible hits instead of a detected fault.
func TestVarintRefusesNonCanonicalEncodings(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
	}{
		{"truncated: continuation with no successor", []byte{0x80}},
		{"empty buffer", nil},
		{"overlong: a final byte contributing no bits", []byte{0x80, 0x00}},
		{"past 64 bits", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}},
		{"an eleventh byte", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x81, 0x01}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := Varint(c.buf, 0); !errors.Is(err, ErrVarint) {
				t.Errorf("Varint(%x) = %v, want ErrVarint", c.buf, err)
			}
		})
	}
}

func TestVarintRefusesAnOutOfRangePosition(t *testing.T) {
	buf := PutVarint(nil, 42)
	for _, pos := range []int{-1, len(buf), len(buf) + 5} {
		if _, _, err := Varint(buf, pos); !errors.Is(err, ErrVarint) {
			t.Errorf("Varint at %d = %v, want ErrVarint", pos, err)
		}
	}
}

func FuzzVarintRoundTrips(f *testing.F) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 40, math.MaxUint64} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v uint64) {
		buf := PutVarint(nil, v)
		got, pos, err := Varint(buf, 0)
		if err != nil {
			t.Fatalf("a value this package encoded did not decode: %v", err)
		}
		if got != v || pos != len(buf) {
			t.Errorf("round trip of %d gave %d at %d of %d", v, got, pos, len(buf))
		}
	})
}

func FuzzVarintNeverPanicsOnArbitraryBytes(f *testing.F) {
	f.Add([]byte{0x80})
	f.Add([]byte{0xff, 0xff})
	f.Fuzz(func(t *testing.T, buf []byte) {
		// The contract is only that it returns rather than panics, and that a
		// success reports a position inside the buffer.
		if v, pos, err := Varint(buf, 0); err == nil {
			if pos < 0 || pos > len(buf) {
				t.Errorf("decoded %d but reported position %d of %d", v, pos, len(buf))
			}
		}
	})
}

// Posting lists are the first id absolute and the rest gaps, which is what
// makes a dense list mostly one-byte values.
func TestAscendingRoundTripsAndIsDeltaEncoded(t *testing.T) {
	ids := []uint32{3, 4, 5, 900, 901}
	buf := PutAscending(nil, ids)
	got, err := Ascending(buf)
	if err != nil {
		t.Fatalf("Ascending: %v", err)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("round trip gave %v, want %v", got, ids)
	}
	// Three consecutive ids after the first cost one byte each; a plain
	// encoding of 900 and 901 would not.
	if len(buf) >= len(ids)*2+2 {
		t.Errorf("delta encoding did not shrink the list: %d bytes for %v", len(buf), ids)
	}
}

func TestAscendingOnAnEmptyList(t *testing.T) {
	got, err := Ascending(nil)
	if err != nil || got != nil {
		t.Errorf("Ascending(nil) = %v, %v; want nil, nil", got, err)
	}
	if PutAscending(nil, nil) != nil {
		t.Error("encoding no ids should write no bytes")
	}
}

// A gap that would carry the running total past a u32 is a corrupt region, not
// a large block id: block ids are u32 by format.
func TestAscendingRefusesAnOverflowingGap(t *testing.T) {
	buf := PutVarint(nil, 1)
	buf = PutVarint(buf, math.MaxUint32)
	if _, err := Ascending(buf); !errors.Is(err, ErrVarint) {
		t.Errorf("expected ErrVarint for a gap past u32, got %v", err)
	}
}

func FuzzAscendingNeverPanics(f *testing.F) {
	f.Add([]byte{0x01, 0x01, 0x01})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, buf []byte) {
		ids, err := Ascending(buf)
		if err != nil {
			return
		}
		if !slices.IsSorted(ids) {
			t.Errorf("decoded a non-ascending list from %x: %v", buf, ids)
		}
	})
}
