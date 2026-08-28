// Package index implements the trigram index, comprising the immutable base
// segment, the delta and tombstone overlay, and the union that serves a query.
//
// The index defaults to off, exists as an escalation, and behaves as a cache:
// removing the directory leaves search working on the walk tier. Nothing durable
// resides here, and everything in it can be rebuilt from the shares.
//
// The on-disk format is fixed. Every structural byte this package emits is
// checked against a committed fixture, so altering anything here alters a format
// two implementations must agree on.
package index

import (
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Block compression.
//
// The zstd specification defines what a decoder accepts rather than what an
// encoder produces, so two independent encoders need not agree byte for byte.
// What must hold, and what the fixtures verify, is that each side decodes the
// other's frames into identical bytes of the recorded length.

// BaseLevel compresses base-segment blocks. Each is read once per candidate hit
// and written once per build, so compression ratio outweighs encode speed.
const BaseLevel = zstd.SpeedBetterCompression

// DeltaLevel compresses delta records. Every query scans them linearly, so
// decode cost dominates and the fastest level is chosen.
const DeltaLevel = zstd.SpeedFastest

// MaxDecompressed caps a single decompressed block, so a corrupt length prefix
// cannot drive an allocation that consumes the machine. A 32-name block of 4 KiB
// paths runs about 128 KiB, leaving four orders of magnitude of headroom.
const MaxDecompressed = 64 << 20

// ErrCorrupt reports a payload disagreeing with what the directory records.
var ErrCorrupt = errors.New("index: a corrupt segment")

// ErrIndexCorrupt reports a segment failing its header or checksum while being
// read. The caller disables the index and rebuilds it while search proceeds by
// walking, since a broken cache costs speed and never correctness.
var ErrIndexCorrupt = errors.New("index: the index is corrupt and has been disabled")

// The encoders and decoders hold no state between calls and tolerate concurrent
// use, while constructing one allocates window-sized buffers, so each is built
// exactly once.
//
//nolint:gochecknoglobals // zstd encoders and decoders are documented as concurrency-safe.
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

// Compress encodes a block for the base segment.
func Compress(data []byte) ([]byte, error) {
	e, err := baseEncoder()
	if err != nil {
		return nil, fmt.Errorf("index: building the encoder: %w", err)
	}
	return e.EncodeAll(data, nil), nil
}

// CompressFast encodes a record for a delta segment.
func CompressFast(data []byte) ([]byte, error) {
	e, err := deltaEncoder()
	if err != nil {
		return nil, fmt.Errorf("index: building the encoder: %w", err)
	}
	return e.EncodeAll(data, nil), nil
}

// Decompress decodes a payload of unknown uncompressed length.
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

// DecompressHint decodes a payload whose uncompressed length the block directory
// recorded.
//
// That length serves as both an allocation hint and a validation: a block not
// decompressing to exactly the recorded size is not the block that was written,
// and proceeding would mean parsing names out of something else entirely.
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
