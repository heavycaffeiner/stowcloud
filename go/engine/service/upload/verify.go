//go:build linux

package upload

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"lukechampine.com/blake3"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// verifyBufBytes is the reused read buffer for whole-file verification. The
// digest is computed in a stream, so a fifty-gigabyte upload costs this much
// memory and not one byte more.
const verifyBufBytes = 256 << 10

// castagnoli names the polynomial in one place rather than at each call. The
// standard library memoizes its own table for the standard polynomials.
func castagnoli() *crc32.Table { return crc32.MakeTable(crc32.Castagnoli) }

// newHasher is the one place an algorithm becomes a hash.
func newHasher(a Algo) hash.Hash {
	if a == AlgoBLAKE3 {
		return blake3.New(digestLen(AlgoBLAKE3), nil)
	}
	return crc32.New(castagnoli())
}

// Sum computes a digest over data, which is the direction a caller holding
// bytes with no checksum attached needs.
func Sum(a Algo, data []byte) ([]byte, error) {
	h := newHasher(a)
	if _, err := h.Write(data); err != nil {
		return nil, fmt.Errorf("computing a %s digest: %w", a, err)
	}
	return h.Sum(nil), nil
}

// streamHasher is a running digest over a chunk that is never fully in
// memory: the engine streams a body into the file and feeds the same slices
// through here, so a per-chunk checksum costs a hasher rather than a copy.
//
// A write that fails poisons the digest rather than being discarded. A hash
// that silently missed a slice would compare against the wrong bytes and
// could pass.
type streamHasher struct {
	h   hash.Hash
	err error
}

func newStreamHasher(a Algo) *streamHasher { return &streamHasher{h: newHasher(a)} }

func (s *streamHasher) write(b []byte) {
	if s.err != nil {
		return
	}
	if _, err := s.h.Write(b); err != nil {
		s.err = err
	}
}

// sum is the digest, or nil when a write failed. A nil digest never compares
// equal, so a poisoned hash refuses the chunk.
func (s *streamHasher) sum() []byte {
	if s.err != nil {
		return nil
	}
	return s.h.Sum(nil)
}

// constantTimeEqual compares two digests without an early exit.
//
// Neither is a secret, but one comparison style for both algorithms is
// simpler than arguing about which one needs it.
func constantTimeEqual(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// VerifyWholeFile streams length bytes of f from zero and compares the digest
// against what the caller expected.
//
// It takes an algorithm and an expected digest, never one alone: the shape
// that shipped before carried only a selector, so verification computed a
// digest and logged it, and could never fail whatever arrived on disk.
//
// The read goes through the same descriptor that took the chunk writes, which
// is the one holder of the read-write intent and the reason that intent
// exists: a read-only reopen would fail the verification it was opened for.
func VerifyWholeFile(f *vfs.File, v Verify, length uint64) error {
	if err := checkDigestLen(v.Algo, len(v.Digest)); err != nil {
		return err
	}
	h := newHasher(v.Algo)
	buf := make([]byte, verifyBufBytes)

	var at uint64
	for at < length {
		want := uint64(verifyBufBytes)
		if remaining := length - at; remaining < want {
			want = remaining
		}
		off, oerr := num.Narrow[int64](at)
		if oerr != nil {
			return fmt.Errorf("verifying the upload: %w", oerr)
		}
		n, rerr := f.ReadAt(buf[:want], off)
		if n > 0 {
			if _, werr := h.Write(buf[:n]); werr != nil {
				return fmt.Errorf("verifying the upload: %w", werr)
			}
			read, nerr := num.Narrow[uint64](n)
			if nerr != nil {
				return fmt.Errorf("verifying the upload: %w", nerr)
			}
			at += read
		}
		if rerr != nil {
			// Short of the declared length is a real failure here, unlike in a
			// stream: the interval set already claimed these bytes, so a file
			// that cannot produce them is one that did not land.
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return fmt.Errorf("verifying the upload: %w", rerr)
		}
		if n == 0 {
			break
		}
	}
	if at != length {
		return fmt.Errorf("%w: the part file holds %d of the declared %d bytes",
			ErrVerify, at, length)
	}
	if !constantTimeEqual(h.Sum(nil), v.Digest) {
		return fmt.Errorf("%w: the finished file does not match the %s digest supplied",
			ErrVerify, v.Algo)
	}
	return nil
}
