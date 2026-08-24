package preview

// Preset is a thumbnail size.
//
// A small closed set rather than caller-chosen dimensions: an arbitrary size
// per request is an unbounded cache and a decode the caller sizes, and neither
// is something a stranger should choose.
type Preset uint8

const (
	// PresetSmall is the grid thumbnail.
	PresetSmall Preset = 1
	// PresetMedium is the list preview.
	PresetMedium Preset = 2
	// PresetLarge is the viewer's first paint, before the full file arrives.
	PresetLarge Preset = 3
)

func (p Preset) valid() bool {
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
