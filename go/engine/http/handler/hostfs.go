// Linux only, for the same reason as the rest of this package.
//go:build linux

// The host filesystem browse surface's projection.
//
// Two callers answer through the same shape: an administrator picking a
// share host, a container file or another configured path, and the first-run
// screen picking the first share's host before an administrator exists.
// Neither reads anything but what os.ReadDir and os.Stat already expose, so
// there is nothing here to redact the way the admin-only share listing
// redacts a host path from every other surface: both callers configuring a
// deployment are the one audience this projection has.
package handler

// HostEntryView is one directory entry on the host, as a browse dialog draws
// it.
type HostEntryView struct {
	Name string `json:"name"`
	// Path is absolute, so a client can hand it straight back as the next
	// listing's path or as the value a form field wants, without rebuilding
	// it from the parent it came from.
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// HostListingView is one directory's contents, or the fixed starting points
// when nothing has been chosen yet.
type HostListingView struct {
	// Path is absolute and cleaned. Empty means this is the root listing:
	// the fixed starting points rather than one directory's contents.
	Path string `json:"path"`
	// Parent is empty when there is nowhere above Path to go, which is what
	// tells the dialog to stop offering an "up" action rather than send it
	// looping on "/".
	Parent    string          `json:"parent"`
	Entries   []HostEntryView `json:"entries"`
	Truncated bool            `json:"truncated"`
}
