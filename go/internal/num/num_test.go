package num

import (
	"errors"
	"math"
	"testing"
)

func TestNarrowFits(t *testing.T) {
	got, err := Narrow[uint8](200)
	if err != nil || got != 200 {
		t.Fatalf("Narrow[uint8](200) = %v, %v", got, err)
	}
}

func TestNarrowTruncationIsRefused(t *testing.T) {
	_, err := Narrow[uint8](256)
	if !errors.Is(err, ErrNarrow) {
		t.Fatalf("Narrow[uint8](256) err = %v, want ErrNarrow", err)
	}
}

// The round trip alone cannot see this one: int8(-1) converts to uint8(255),
// which converts back to -1 and compares equal.
func TestNarrowNegativeToUnsignedIsRefused(t *testing.T) {
	_, err := Narrow[uint8](int8(-1))
	if !errors.Is(err, ErrNarrow) {
		t.Fatalf("Narrow[uint8](int8(-1)) err = %v, want ErrNarrow", err)
	}
}

func TestNarrowCarriesTheValue(t *testing.T) {
	_, err := Narrow[int32](int64(math.MaxInt64))
	var re *RangeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RangeError", err)
	}
	if re.Value != "9223372036854775807" {
		t.Fatalf("RangeError.Value = %q, want the value that did not fit", re.Value)
	}
}
