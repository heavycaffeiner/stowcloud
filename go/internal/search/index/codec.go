// Package index implements the trigram index: the immutable base segment, the
// delta and tombstone overlay, and the union that answers a query.
//
// The on-disk format is fixed. Every structural byte this package writes is
// compared against a fixture the Rust implementation produced, so a change
// here is a change to a format two implementations have to agree on.
package index

import (
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Block compression.
//
// zstd's format specifies what a decoder accepts rather than what an encoder
// emits, so two independent encoders are not required to agree byte for byte.
// What is required, and what the fixtures check, is that each side decodes the
// other's frames to identical bytes of the recorded length.

// BaseLevel compresses base-segment blocks. They are read once per candidate
// hit and written once per build, so ratio matters more than encode speed.
const BaseLevel = zstd.SpeedBetterCompression

// DeltaLevel compresses delta records, which are linearly scanned on every
// query, so decode cost dominates and the level is the fastest one.
const DeltaLevel = zstd.SpeedFastest

// MaxDecompressed is the ceiling on one decompressed block, so a corrupt
// length prefix cannot make this allocate the machine. A 32-name block of 4 KiB
// paths is about 128 KiB, so this is four orders of magnitude of headroom.
const MaxDecompressed = 64 << 20

// ErrCorrupt is a payload that is not what the directory says it is.
var ErrCorrupt = errors.New("index: a corrupt segment")

// The encoders and decoders are stateless across calls and safe for concurrent
// use, and building one costs an allocation of window-sized buffers, so they
// are made once.
//
//nolint:gochecknoglobals // zstd encoders and decoders are documented safe for concurrent use.
var (
	baseEncoder  = sync.OnceValues(func() (*zstd.Encoder, error) { return newEncoder(BaseLevel) })
	deltaEncoder = sync.OnceValues(func() (*zstd.Encoder, error) { return newEncoder(DeltaLevel) })
	sharedDecode = sync.OnceValues(func() (*zstd.Decoder, error) {
		return zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(0),
			zstd.WithDecoderMaxMemory(MaxDecompressed))
	})
)

func newEncoder(level zstd.EncoderLevel) (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
}

// Compress encodes a base-segment block.
func Compress(data []byte) ([]byte, error) {
	e, err := baseEncoder()
	if err != nil {
		return nil, fmt.Errorf("index: building the encoder: %w", err)
	}
	return e.EncodeAll(data, nil), nil
}

// CompressFast encodes a delta record.
func CompressFast(data []byte) ([]byte, error) {
	e, err := deltaEncoder()
	if err != nil {
		return nil, fmt.Errorf("index: building the encoder: %w", err)
	}
	return e.EncodeAll(data, nil), nil
}

// Decompress decodes a payload whose uncompressed length is not known.
func Decompress(data []byte) ([]byte, error) {
	d, err := sharedDecode()
	if err != nil {
		return nil, fmt.Errorf("index: building the decoder: %w", err)
	}
	out, err := d.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: the stream is truncated or corrupt: %w", ErrCorrupt, err)
	}
	return out, nil
}

// DecompressHint decodes a payload whose uncompressed length the block
// directory recorded.
//
// The length is both an allocation hint and a check: a block that does not
// decompress to exactly the recorded size is not the block that was written,
// and continuing with it would mean parsing names out of something else.
func DecompressHint(data []byte, expect int) ([]byte, error) {
	if expect < 0 || expect > MaxDecompressed {
		return nil, fmt.Errorf("%w: a declared block size of %d", ErrCorrupt, expect)
	}
	d, err := sharedDecode()
	if err != nil {
		return nil, fmt.Errorf("index: building the decoder: %w", err)
	}
	var dst []byte
	if expect > 0 {
		dst = make([]byte, 0, expect)
	}
	out, err := d.DecodeAll(data, dst)
	if err != nil {
		return nil, fmt.Errorf("%w: the stream is truncated or corrupt: %w", ErrCorrupt, err)
	}
	if expect != 0 && len(out) != expect {
		return nil, fmt.Errorf("%w: the directory says %d bytes and the stream produced %d",
			ErrCorrupt, expect, len(out))
	}
	return out, nil
}
