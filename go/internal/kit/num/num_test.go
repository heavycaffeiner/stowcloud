package num

import (
	"errors"
	"math"
	"testing"
)

func TestNarrowIdentity(t *testing.T) {
	got, err := Narrow[int32](int32(42))
	if err != nil || got != 42 {
		t.Fatalf("Narrow[int32](42) = %v, %v, want 42, nil", got, err)
	}
}

func TestNarrowWideningFits(t *testing.T) {
	got, err := Narrow[int64](int8(-5))
	if err != nil || got != -5 {
		t.Fatalf("Narrow[int64](int8(-5)) = %v, %v, want -5, nil", got, err)
	}
}

func TestNarrowTable(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
	}{
		{"wide to narrow, value too big", func() error {
			_, err := Narrow[uint8](300)
			return err
		}},
		{"wide to narrow, boundary just over", func() error {
			_, err := Narrow[int8](128)
			return err
		}},
		{"negative into unsigned, small magnitude", func() error {
			_, err := Narrow[uint8](int8(-1))
			return err
		}},
		{"negative into unsigned, wide source", func() error {
			_, err := Narrow[uint32](int64(-1))
			return err
		}},
		{"negative into signed but narrower", func() error {
			_, err := Narrow[int8](int16(-200))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if !errors.Is(err, ErrNarrow) {
				t.Fatalf("err = %v, want ErrNarrow", err)
			}
			var re *RangeError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want *RangeError", err)
			}
			if re.Value == "" || re.From == "" || re.To == "" {
				t.Fatalf("RangeError incomplete: %+v", re)
			}
		})
	}
}

func TestNarrowFitsAtBoundary(t *testing.T) {
	got, err := Narrow[uint8](255)
	if err != nil || got != 255 {
		t.Fatalf("Narrow[uint8](255) = %v, %v, want 255, nil", got, err)
	}
	got2, err2 := Narrow[int8](-128)
	if err2 != nil || got2 != -128 {
		t.Fatalf("Narrow[int8](-128) = %v, %v, want -128, nil", got2, err2)
	}
}

func TestNarrowRangeErrorCarriesTheOffendingValue(t *testing.T) {
	_, err := Narrow[int16](int64(math.MaxInt64))
	var re *RangeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RangeError", err)
	}
	if re.Value != "9223372036854775807" {
		t.Fatalf("RangeError.Value = %q, want the failing input printed", re.Value)
	}
}

func TestNarrowUintptrRoundTrip(t *testing.T) {
	got, err := Narrow[uintptr](uint32(123))
	if err != nil || got != 123 {
		t.Fatalf("Narrow[uintptr](123) = %v, %v, want 123, nil", got, err)
	}
}
