package watch

import "github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"

// InvalEvent reports that something beneath a share has changed.
type InvalEvent struct {
	Share vfs.ShareID

	// Dir names the share-relative directory whose entries changed. It is empty
	// whenever All is set.
	Dir string

	// All indicates the watcher dropped events, so the entire share must be
	// considered stale. Consumers respond by incrementing the share's
	// generation, a single row regardless of how much was missed. Replay is
	// impossible: the missed events are precisely what is unknown.
	All bool
}

// Stats holds the change-detection figures the health endpoint publishes.
type Stats struct {
	// Registered counts directories currently holding a live kernel watch.
	Registered int

	// Degraded tallies refused registrations, each leaving a subtree on lazy
	// revalidation. Any non-zero value denotes a named degradation, not a
	// failure.
	Degraded int64

	// Shares counts the shares known to the watcher.
	Shares int
}
