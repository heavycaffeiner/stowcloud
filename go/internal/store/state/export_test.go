package state

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// SpecV1 stops at the first migration, so a test can build the shape that
// shipped and prove migration 2 finds what it has to refuse. Nothing outside
// this package's tests may open a database at a version behind the binary.
func SpecV1(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations()[:1], Rebuildable: false}
}

// MigrationNames is the shipped migration list, so a test asserting the
// version a fresh database lands at reads it from the list rather than from a
// number that has to be edited every time a phase adds a step.
func MigrationNames() []string {
	out := make([]string, 0, len(migrations()))
	for _, m := range migrations() {
		out = append(out, m.Name)
	}
	return out
}
