# Preview 04: archive listing

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/preview/archive.go` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## What it is

"What is in this zip" without extracting it: the viewer's archive panel.
Listing only; nothing here opens an entry's content.

```go
func ListArchive(ctx context.Context, r io.ReaderAt, size int64) (ArchiveListing, error)

type ArchiveListing struct {
    Entries   []ArchiveEntry // name, size, dir flag
    Truncated bool           // the cap was hit
    Skipped   int            // unsafe names, counted not hidden
}
```

## The rules

- **Central directory only**, through `io.ReaderAt`: the listing never
  streams the archive body, so a 100 GB zip costs a directory read.
- Entries are capped at `limits.ArchiveEntriesListed`, and hitting the
  cap is **reported** (`Truncated`), never a silent short list.
- `safeArchiveName` is a **display filter, not a path-traversal guard**,
  and the distinction is documented where it lives: archive names are
  never opened, so traversal is not the risk; control characters and
  invalid encodings in a rendered name are. A skipped name increments
  `Skipped` rather than vanishing, so ten thousand entries and three
  unsafe ones reads as exactly that.
- Cancellation is honored between directory entries; a cancelled
  listing stops.
- Fuzzed: arbitrary bytes never panic the parser.

## Deliberate changes

None. This file was verified sound and its behavior carries whole.

## Tests

- A real archive lists names, sizes and directory flags.
- The cap truncates and reports; the count is exact at the boundary.
- Unsafe names are counted, safe ones listed (fixture with both).
- A cancelled context stops the listing.
- Fuzz: no panic on arbitrary input; a non-zip refuses cleanly.
