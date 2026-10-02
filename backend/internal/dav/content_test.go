//go:build linux

package dav

import (
	"errors"
	"testing"
)

func TestSuffixRangeOnEmptyFileIsUnsatisfiable(t *testing.T) {
	rng, err := parseByteRange("bytes=-1", 0)
	if !errors.Is(err, ErrBadRange) {
		t.Fatalf("parseByteRange returned %v, want ErrBadRange", err)
	}
	if rng != nil {
		t.Fatalf("parseByteRange returned %#v for an empty file", rng)
	}
}
