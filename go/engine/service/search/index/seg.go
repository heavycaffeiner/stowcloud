package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// Append-only record framing for the delta and tombstone segments.
//
//	record := u32 payload_len | u32 fnv1a32(payload) | payload
//
// Crash safety is the length prefix plus the checksum. A torn tail, a record
// whose header or body did not reach the disk, fails to parse, and the reader
// reports the offset of the last complete record so the opener can cut the
// file back to it. Everything before the tear is still good, because the file
// is only ever appended to.

// FrameHeader is the length and checksum that precede a payload.
const FrameHeader = 8

// MaxRecord bounds one framed payload.
//
// The length prefix is read off disk before the body is, so without a ceiling
// a corrupt four bytes would ask for an allocation of whatever they happened
// to say. Nothing this writes approaches it: a delta record is a batch of
// paths.
const MaxRecord = 64 << 20

// FNV1a32 is the record checksum.
//
// Distinct from the root package's Hash64, which feeds the distinct-count
// sketch. The two have different collision tolerances and stay two primitives.
func FNV1a32(data []byte) uint32 {
	h := uint32(0x811c9dc5)
	for _, b := range data {
		h ^= uint32(b)
		h *= 0x01000193
	}
	return h
}

// Frame wraps a payload in its header.
func Frame(payload []byte) ([]byte, error) {
	length, err := num.Narrow[uint32](len(payload))
	if err != nil || len(payload) > MaxRecord {
		return nil, fmt.Errorf("index: a record of %d bytes cannot be framed", len(payload))
	}
	out := make([]byte, 0, FrameHeader+len(payload))
	out = binary.LittleEndian.AppendUint32(out, length)
	out = binary.LittleEndian.AppendUint32(out, FNV1a32(payload))
	return append(out, payload...), nil
}

// AppendRecord appends one framed record.
//
// One open, one write, one sync. This is the constant-time write path the whole
// segment split exists to enable: a name arriving does not touch the base.
func AppendRecord(path string, payload []byte) (int64, error) {
	framed, err := Frame(payload)
	if err != nil {
		return 0, err
	}
	//nolint:gosec // G304: a segment path this package built, never a request's.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("index: appending to %s: %w", path, err)
	}
	// A short write on an append is a torn record, which the reader is built
	// to survive, so the close error still matters and is not discarded.
	if _, werr := f.Write(framed); werr != nil {
		return 0, errors.Join(fmt.Errorf("index: appending to %s: %w", path, werr), f.Close())
	}
	if serr := f.Sync(); serr != nil {
		return 0, errors.Join(fmt.Errorf("index: syncing %s: %w", path, serr), f.Close())
	}
	if cerr := f.Close(); cerr != nil {
		return 0, fmt.Errorf("index: closing %s: %w", path, cerr)
	}
	return int64(len(framed)), nil
}

// Recovered is what a segment read found.
type Recovered struct {
	Records [][]byte
	// GoodLen is the byte length of the intact prefix.
	GoodLen int64
	// Torn reports that a partial tail was found past GoodLen.
	Torn bool
}

// ReadRecords reads every intact record.
//
// A torn tail is never an error: it is the expected state after a crash, not
// corruption, and treating it as a failure would disable the index every time
// the machine lost power mid-append.
func ReadRecords(path string) (Recovered, error) {
	buf, err := os.ReadFile(path) //nolint:gosec // G304: as above, a segment path this package built.
	if errors.Is(err, fs.ErrNotExist) {
		return Recovered{}, nil
	}
	if err != nil {
		return Recovered{}, fmt.Errorf("index: reading %s: %w", path, err)
	}

	var out Recovered
	pos := 0
	for pos+FrameHeader <= len(buf) {
		length := binary.LittleEndian.Uint32(buf[pos:])
		sum := binary.LittleEndian.Uint32(buf[pos+4:])
		if length > MaxRecord {
			break
		}
		body := pos + FrameHeader
		n, nerr := num.Narrow[int](length)
		if nerr != nil || n > len(buf)-body {
			break
		}
		end := body + n
		if FNV1a32(buf[body:end]) != sum {
			break
		}
		out.Records = append(out.Records, buf[body:end])
		pos = end
	}

	out.GoodLen = int64(pos)
	out.Torn = pos < len(buf)
	return out, nil
}

// TruncateTo cuts a torn tail off, which the opener does when ReadRecords
// reports one.
func TruncateTo(path string, goodLen int64) error {
	//nolint:gosec // G304: as above.
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("index: truncating %s: %w", path, err)
	}
	if terr := f.Truncate(goodLen); terr != nil {
		return errors.Join(fmt.Errorf("index: truncating %s: %w", path, terr), f.Close())
	}
	if serr := f.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("index: syncing %s: %w", path, serr), f.Close())
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("index: closing %s: %w", path, cerr)
	}
	return nil
}

// A record body is varint-encoded and then compressed only if that made it
// smaller, with a one-byte tag saying which. Compressing unconditionally would
// grow the small records that dominate a delta segment.
const (
	codecRaw  = 0
	codecZstd = 1
)

// EncodePayload encodes one record: a sequence number, a count, and the
// entries.
//
// The sequence number is what orders a tombstone against an append of the same
// path, so a name deleted and recreated ends up live rather than hidden.
func EncodePayload(seq uint64, entries []Entry) ([]byte, error) {
	body := make([]byte, 0, 16+len(entries)*24)
	body = search.PutVarint(body, seq)
	body = search.PutVarint(body, uint64(len(entries)))
	for _, e := range entries {
		body = search.PutVarint(body, uint64(e.Share))
		body = search.PutVarint(body, uint64(len(e.Path)))
		body = append(body, e.Path...)
	}

	comp, err := CompressFast(body)
	if err != nil {
		return nil, err
	}
	if len(comp) < len(body) {
		return append([]byte{codecZstd}, comp...), nil
	}
	return append([]byte{codecRaw}, body...), nil
}

// DecodePayload parses one record.
func DecodePayload(payload []byte) (uint64, []Entry, error) {
	if len(payload) == 0 {
		return 0, nil, fmt.Errorf("%w: an empty segment record", ErrCorrupt)
	}
	var body []byte
	switch payload[0] {
	case codecRaw:
		body = payload[1:]
	case codecZstd:
		var err error
		if body, err = Decompress(payload[1:]); err != nil {
			return 0, nil, err
		}
	default:
		return 0, nil, fmt.Errorf("%w: an unknown record codec %d", ErrCorrupt, payload[0])
	}

	seq, pos, err := search.Varint(body, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: a truncated sequence number", ErrCorrupt)
	}
	count, pos, err := search.Varint(body, pos)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: a truncated entry count", ErrCorrupt)
	}
	// The count is a length prefix off disk, so it sizes the allocation only
	// after it is bounded by what the body could actually hold.
	if count > uint64(len(body)) {
		return 0, nil, fmt.Errorf("%w: a record claiming %d entries", ErrCorrupt, count)
	}

	out := make([]Entry, 0, count)
	for range count {
		share, next, serr := search.Varint(body, pos)
		if serr != nil {
			return 0, nil, fmt.Errorf("%w: a truncated share id in a record", ErrCorrupt)
		}
		length, next2, lerr := search.Varint(body, next)
		if lerr != nil {
			return 0, nil, fmt.Errorf("%w: a truncated name length in a record", ErrCorrupt)
		}
		pos = next2

		n, nerr := num.Narrow[int](length)
		id, ierr := num.Narrow[uint32](share)
		if nerr != nil || ierr != nil || n > len(body)-pos {
			return 0, nil, fmt.Errorf("%w: a name runs past the end of its record", ErrCorrupt)
		}
		out = append(out, Entry{Share: id, Path: string(body[pos : pos+n])})
		pos += n
	}
	return seq, out, nil
}
