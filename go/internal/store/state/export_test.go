package state

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// SpecV1 stops at the first migration, so a test can build the shape that
// shipped and prove migration 2 finds what it has to refuse. Nothing outside
// this package's tests may open a database at a version behind the binary.
func SpecV1(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations()[:1], Rebuildable: false}
}
