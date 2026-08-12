package watch

import "github.com/heavycaffeiner/stowcloud/go/internal/vfs"

// InvalEvent says something under a share changed.
type InvalEvent struct {
	Share vfs.ShareID

	// Dir is the share-relative directory whose entries changed. Empty when All
	// is set.
	Dir string

	// All says the watcher lost events and the whole share has to be treated as
	// stale. The consumer bumps the share's generation, which is one row
	// whatever was missed: there is nothing to replay, because what was missed
	// is exactly what is unknown.
	All bool
}

// Stats is what the health endpoint reports about change detection.
type Stats struct {
	// Registered is how many directories carry a live kernel watch.
	Registered int

	// Degraded counts the registrations that were refused, each of which is a
	// subtree that falls back to lazy revalidation. A non-zero value is a named
	// degradation rather than a failure.
	Degraded int64

	// Shares is how many shares the watcher knows about.
	Shares int
}
