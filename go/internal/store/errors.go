package store

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The errors a caller of this package matches on. They are declared where the
// file mechanism raises them and named again here, so that a caller holding a
// *Store does not have to reach past it to say what went wrong.
var (
	// ErrSchemaAhead is a database written by a newer binary. Startup refuses.
	ErrSchemaAhead = dbfile.ErrSchemaAhead

	// ErrMigrationFailed is a migration that rolled back. The old version and
	// the old shape both stand.
	ErrMigrationFailed = dbfile.ErrMigrationFailed

	// ErrWritesBlocked is the size guard, which is off by default. Reads
	// continue, writes that grow the file refuse, and health reports degraded.
	ErrWritesBlocked = dbfile.ErrWritesBlocked

	// ErrBusy is a busy_timeout that expired, surfaced rather than retried
	// forever.
	ErrBusy = dbfile.ErrBusy
)
