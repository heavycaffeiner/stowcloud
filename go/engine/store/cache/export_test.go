package cache

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// NewNarrow is New with the derivation reduced to fewer bits, letting a test
// reach the collision path with a handful of files instead of the corpus a
// 63-bit collision would demand. It compiles into this package's tests only.
func NewNarrow(ctx context.Context, f *dbfile.DB, ov Overrides, bits uint) (*DB, error) {
	d, err := New(ctx, f, ov)
	if err != nil {
		return nil, err
	}
	d.bits = bits
	return d, nil
}

// DeriveNarrow performs DeriveID at that same reduced width.
func DeriveNarrow(id ident.Ident, attempt uint32, bits uint) ident.FileID {
	return derive(id, attempt, bits)
}

// SpecV1 halts after the first migration so a test can construct the shape that
// actually shipped and demonstrate migration 2 detecting and discarding it.
// Opening a database behind the binary is forbidden outside this package's
// tests.
func SpecV1(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations()[:1], Rebuildable: true}
}
