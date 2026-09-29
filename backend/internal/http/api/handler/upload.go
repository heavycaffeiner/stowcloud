// Linux only, for the same reason as the rest of this package.
//go:build linux

// The upload family's projection.
package handler

// UploadSettingsView is the server-global chunk configuration an
// administrator reads and writes.
//
// The byte counts are numbers rather than decimal strings: both are bounded
// by the chunk ceiling, far inside what a JavaScript number holds exactly,
// and the screen does arithmetic on them to show megabytes.
type UploadSettingsView struct {
	ChunkMin     int64 `json:"chunk_min"`
	ChunkDefault int64 `json:"chunk_default"`

	// CacheEnabled is the switch; CacheAvailable is whether this deployment
	// has a spool for it to mean anything. A deployment with no data
	// directory has none, and the screen shows the switch disabled rather
	// than offering one that does nothing.
	CacheEnabled   bool `json:"cache_enabled"`
	CacheAvailable bool `json:"cache_available"`
}

// TerminalUploadState reports whether a state name means the session is
// finished.
//
// Over the name, for the tier that reads names rather than stored numbers. It
// is checked against the service's own list, so the two cannot drift.
func TerminalUploadState(state string) (terminal, known bool) {
	switch state {
	case "done", "aborted", "expired":
		return true, true
	case "receiving", "finalizing":
		return false, true
	default:
		// Unknown counts as finished, and says so. A fallback that could not
		// be told from a listed answer would make the list untestable, since
		// removing an entry would change nothing observable.
		return true, false
	}
}
