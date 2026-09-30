// Package limits defines bounds for archive previews and the preview worker wire.
package limits

const (
	ArchiveEntriesListed  = 10_000
	ArchiveEntriesParsed  = 50_000
	ArchiveDirectoryBytes = 32 << 20
	ArchivePackedEntries  = 200_000
	ArchivePackedBytes    = 32 << 30
	ConcurrentArchives    = 8
	WorkerWireMessage     = 8 << 10
)
