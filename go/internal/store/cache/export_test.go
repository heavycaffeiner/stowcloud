package cache

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
)

// NewNarrow is New with the derivation folded into fewer bits, so that the
// collision path can be reached by a test with a handful of files in it rather
// than the three billion a 63-bit collision needs. It is compiled into this
// package's tests and nowhere else.
func NewNarrow(ctx context.Context, f *dbfile.DB, ov Overrides, bits uint) (*DB, error) {
	d, err := New(ctx, f, ov)
	if err != nil {
		return nil, err
	}
	d.bits = bits
	return d, nil
}

// DeriveNarrow is DeriveID at the same reduced width.
func DeriveNarrow(ident Ident, attempt uint32, bits uint) FileID {
	return derive(ident, attempt, bits)
}
