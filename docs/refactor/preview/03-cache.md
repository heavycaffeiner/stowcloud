# Preview 03: the service and its cache

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/preview` (here `service.go`, `cache.go`, `preset.go`) is
> referenced as a behavioral specification only. The new implementation
> is written completely from scratch; nothing is copied.

## The service

```go
func NewService(o ServiceOptions) *Service // Core, Pool, Cache, Clock
func (s *Service) Get(ctx context.Context, r core.Resolved, preset Preset) (Thumb, error)
```

- **The permission is `Read|Download`, the same a download needs**: a
  thumbnail is a derivative of the bytes, and being able to see one is
  being able to see the file. The check runs before any cache lookup,
  so a cache hit is not a permission bypass.
- Order: validate the preset, require the permission, consult the
  negative cache, consult the thumbnail cache, generate through the
  pool, store, answer. A generation failure stores a negative.

## Presets

```go
const (
    PresetSmall  Preset = 1 // the grid thumbnail
    PresetMedium Preset = 2 // the list preview
    PresetLarge  Preset = 3 // the viewer's first paint
)
```

Presets are wire values (they travel in the worker request and in cache
keys); the numbering is fixed. An invalid preset refuses with
`ErrUnsupported`.

## The thumbnail cache

```go
func NewCache(dir string) (*Cache, error)
func (c *Cache) Open(k Key) (*os.File, bool)
func (c *Cache) Put(k Key, write func(*os.File) error) error
```

- Keyed by content identity and preset, so a changed file misses rather
  than serving yesterday's pixels.
- `Put` is a durable atomic replace (stage, sync, rename), repointed at
  `store/fsatomic`: a crash mid-write must never leave a half thumbnail
  a later `Open` serves.
- The cache is disposable: delete the directory and previews
  regenerate.

## The negative cache

```go
type Negatives struct{ ... } // in-memory, TTL'd, swept
func (n *Negatives) Get / Put / Sweep / Len
```

A file that failed to decode will fail again; without the negative
cache, a grid full of corrupt files re-runs the worker on every scroll.
Entries carry a reason and a TTL; the sweep is called from the
maintenance loop. In-memory only: a restart retries, which is the
desired behavior after an upgrade fixes a decoder.

## Deliberate changes

1. **`Put` repoints at `store/fsatomic`** (the survey's inventory).
2. Nothing else: the permission rule, the key shape, the negative
   reasons and the disposability are behavior-preserving.

## Tests

- The permission: a caller without Download gets no thumbnail, cached
  or not (plant a cache entry first).
- Cache: hit, miss, key changes with content identity; a crash between
  stage and rename leaves the old entry served (fault-injected write).
- Negatives: a failed decode is remembered with its reason; the TTL
  expires it; the sweep counts; a restart forgets.
- End to end: a real image through service, pool and worker produces a
  thumbnail; the second request is a cache hit (the pool sees one job).
