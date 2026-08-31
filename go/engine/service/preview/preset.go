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
// A small fixed set instead of caller-supplied dimensions. Arbitrary per-request
// sizes would mean an unbounded cache and a decode whose cost the caller
// dictates, neither of which a stranger should control.
//
// The numbering is fixed: these are wire values that travel in the worker
// request and in cache keys, so renumbering them would make an old cache entry
// answer for a different size.
type Preset uint8

const (
	// PresetSmall serves the grid thumbnail.
	PresetSmall Preset = 1
	// PresetMedium serves the list preview.
	PresetMedium Preset = 2
	// PresetLarge serves the viewer's first paint, ahead of the full file.
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

// Bounds gives the box a thumbnail fits within, aspect ratio preserved.
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

// Presets lists every preset for a caller that iterates them.
func Presets() []Preset { return []Preset{PresetSmall, PresetMedium, PresetLarge} }
