// Package preview makes thumbnails and archive listings out of hostile input.
//
// Every image decoded here arrived from a user, so the whole subsystem is
// built around one assumption: the decoder will be exploited someday, and when
// it is, the process it runs in must have nothing worth taking. That process is
// the worker under worker/: separate, jailed before it reads its first message,
// holding descriptors instead of paths, and enforcing its own ceilings whatever
// the parent said.
package preview

// Preset is a thumbnail size.
//
// A small closed set rather than caller-chosen dimensions: an arbitrary size
// per request is an unbounded cache and a decode the caller sizes, and neither
// is something a stranger should choose.
//
// The numbering is fixed: these are wire values that travel in the worker
// request and in cache keys, so renumbering them would make an old cache entry
// answer for a different size.
type Preset uint8

const (
	// PresetSmall is the grid thumbnail.
	PresetSmall Preset = 1
	// PresetMedium is the list preview.
	PresetMedium Preset = 2
	// PresetLarge is the viewer's first paint, before the full file arrives.
	PresetLarge Preset = 3
)

// Valid reports whether p is one of the three. An invalid preset refuses with
// ErrUnsupported rather than falling back to a default: a caller asking for a
// size that does not exist has a bug, and answering anyway hides it.
func (p Preset) Valid() bool {
	return p == PresetSmall || p == PresetMedium || p == PresetLarge
}

func (p Preset) String() string {
	switch p {
	case PresetSmall:
		return "small"
	case PresetMedium:
		return "medium"
	case PresetLarge:
		return "large"
	}
	return "unknown"
}

// Bounds is the box a thumbnail fits inside, aspect ratio preserved.
func (p Preset) Bounds() (w, h int) {
	switch p {
	case PresetSmall:
		return 256, 256
	case PresetMedium:
		return 512, 512
	case PresetLarge:
		return 1024, 1024
	}
	return 0, 0
}

// Presets is every preset, for a caller iterating them.
func Presets() []Preset { return []Preset{PresetSmall, PresetMedium, PresetLarge} }
