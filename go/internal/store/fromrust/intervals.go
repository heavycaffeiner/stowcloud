package fromrust

import "errors"

// maxRuns is the bound the old encoder held itself to. A blob claiming more
// than this is not a long interval set, it is a length field somebody wrote.
const maxRuns = 4096

// errCorrupt is a blob that is not an interval set.
var errCorrupt = errors.New("the stored interval set does not decode")

// decodeIntervals reads the run-length form the old upload engine wrote: a
// count, then for each run the gap since the previous run's end and the run's
// own length, every number a base-128 varint.
//
// The invariant is re-checked rather than assumed, because these are bytes
// read back from a file: runs ascend, none is empty, none touches the next (a
// touching pair would have been merged when it was written), and nothing
// trails the last one. A set that fails any of it is refused, and the caller
// treats that as a session with nothing recorded rather than as a failure of
// the import.
func decodeIntervals(b []byte) ([][2]uint64, error) {
	pos := 0
	count, ok := readVarint(b, &pos)
	if !ok || count > maxRuns {
		return nil, errCorrupt
	}
	runs := make([][2]uint64, 0, count)
	var prevEnd uint64
	for i := uint64(0); i < count; i++ {
		gap, okGap := readVarint(b, &pos)
		length, okLen := readVarint(b, &pos)
		if !okGap || !okLen || length == 0 || (i > 0 && gap == 0) {
			return nil, errCorrupt
		}
		start := prevEnd + gap
		if start < prevEnd {
			return nil, errCorrupt
		}
		end := start + length
		if end < start {
			return nil, errCorrupt
		}
		runs = append(runs, [2]uint64{start, end})
		prevEnd = end
	}
	if pos != len(b) {
		return nil, errCorrupt
	}
	return runs, nil
}

// readVarint reads one base-128 varint, refusing a shift past the width of the
// value it is filling so that a long run of continuation bytes cannot loop.
func readVarint(b []byte, pos *int) (uint64, bool) {
	var (
		result uint64
		shift  uint
	)
	for {
		if *pos >= len(b) || shift >= 64 {
			return 0, false
		}
		c := b[*pos]
		*pos++
		result |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return result, true
		}
		shift += 7
	}
}
