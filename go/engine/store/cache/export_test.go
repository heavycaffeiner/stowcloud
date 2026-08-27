package cache

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// NewNarrow is New with the derivation folded into fewer bits, so the
// collision path can be reached by a test with a handful of files in it
// rather than the corpus a 63-bit collision needs. It is compiled into this
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
func DeriveNarrow(id ident.Ident, attempt uint32, bits uint) ident.FileID {
	return derive(id, attempt, bits)
}

// SpecV1 stops at the first migration, so a test can build the shape that
// actually shipped and prove migration 2 finds and discards it. Nothing
// outside this package's tests may open a database behind the binary.
func SpecV1(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations()[:1], Rebuildable: true}
}
