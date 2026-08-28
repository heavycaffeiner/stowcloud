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

// Append-only record framing used by the delta and tombstone segments.
//
//	record := u32 payload_len ++ u32 fnv1a32(payload) ++ payload
//
// The length prefix together with the checksum provides crash safety. A torn
// tail, meaning a record whose header or body never reached disk, fails to
// parse, and the reader reports the offset of the last complete record so the
// opener can truncate back to it. Everything preceding the tear remains valid,
// because the file is only ever appended to.

// FrameHeader holds the length and checksum preceding a payload.
const FrameHeader = 8

// MaxRecord limits a single framed payload.
//
// The length prefix is read from disk ahead of the body, so absent a ceiling
// four corrupt bytes would request an allocation of whatever they happened to
// encode. Nothing written here comes close: a delta record holds a batch of
// paths.
const MaxRecord = 64 << 20

// FNV1a32 computes the record checksum.
//
// It is separate from the root package's Hash64, which feeds the distinct-count
// sketch. The two tolerate collisions differently and remain distinct
// primitives.
func FNV1a32(data []byte) uint32 {
	h := uint32(0x811c9dc5)
	for _, b := range data {
		h ^= uint32(b)
		h *= 0x01000193
	}
	return h
}

// Frame prefixes a payload with its header.
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

// AppendRecord writes one framed record.
//
// A single open, write and sync. This constitutes the constant-time write path
// the entire segment split exists to permit: an arriving name never touches the
// base.
func AppendRecord(path string, payload []byte) (int64, error) {
	framed, err := Frame(payload)
	if err != nil {
		return 0, err
	}
	//nolint:gosec // G304: this package constructed the segment path; no request did.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("index: appending to %s: %w", path, err)
	}
	// A short write during an append produces a torn record, which the reader is
	// designed to survive, so the close error still matters and is not
	// discarded.
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

// Recovered reports what a segment read found.
type Recovered struct {
	Records [][]byte
	// GoodLen gives the byte length of the intact prefix.
	GoodLen int64
	// Torn indicates a partial tail was found beyond GoodLen.
	Torn bool
}

// ReadRecords returns every intact record.
//
// A torn tail is never an error. It is the expected state following a crash
// rather than corruption, and treating it as a failure would disable the index
// every time the machine lost power mid-append.
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

// TruncateTo removes a torn tail, which the opener does whenever ReadRecords
// reports one.
func TruncateTo(path string, goodLen int64) error {
	//nolint:gosec // G304: same reasoning as above.
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

// Record bodies are varint-encoded and then compressed only when compression
// actually shrinks them, with a one-byte tag recording which form was used.
// Compressing unconditionally would enlarge the small records that dominate a
// delta segment.
const (
	codecRaw  = 0
	codecZstd = 1
)

// EncodePayload serializes one record containing a sequence number, a count and
// the entries.
//
// The sequence number is what orders a tombstone relative to an append of the
// same path, letting a name that was deleted and recreated end up live rather
// than hidden.
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

// DecodePayload parses a single record.
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
	// The count arrives as a length prefix from disk, so it sizes the allocation
	// only after being bounded by what the body could actually contain.
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
