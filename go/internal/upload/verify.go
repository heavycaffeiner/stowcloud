//go:build linux

package upload

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"lukechampine.com/blake3"
)

// Both algorithms here are client-facing: each is a value a client puts in a
// TUS Upload-Checksum header, so neither can be swapped for whatever the
// standard library happens to offer. CRC32C is the standard library's
// Castagnoli table; BLAKE3 is the pure-Go module the directory ETag already
// uses, so CGO_ENABLED=0 has nothing to fall back from.

// verifyBufBytes is the reused read buffer for whole-file verification. The
// digest is computed in a stream, so a 50 GiB upload costs this much memory
// and not one byte more.
const verifyBufBytes = 256 << 10

// castagnoli is built once. crc32.MakeTable memoises the standard polynomials
// internally, and naming the table here keeps the choice of polynomial in one
// place rather than at each call.
func castagnoli() *crc32.Table { return crc32.MakeTable(crc32.Castagnoli) }

// newHasher is the one place an algorithm becomes a hash.
func newHasher(a Algo) hash.Hash {
	if a == AlgoBLAKE3 {
		return blake3.New(32, nil)
	}
	return crc32.New(castagnoli())
}

// Sum computes a digest over data, which is the direction a caller holding
// bytes with no checksum attached needs.
func Sum(a Algo, data []byte) []byte {
	h := newHasher(a)
	// hash.Hash.Write is documented never to return an error.
	_, _ = h.Write(data) //nolint:errcheck // see above: the interface forbids a failure here.
	return h.Sum(nil)
}

// MatchChunk reports whether data hashes to the checksum the client sent.
//
// Constant-time comparison, though neither digest is a secret: one comparison
// style for both algorithms is simpler than arguing that one of them does not
// need it, and the input is client-supplied either way.
func MatchChunk(sum Checksum, data []byte) bool {
	return subtle.ConstantTimeCompare(Sum(sum.Algo, data), sum.Digest) == 1
}

// hasherFunc is a running digest over a chunk that is never fully in memory.
// The engine streams a body into pwrite and feeds the same slices through
// here, so a per-chunk checksum costs a hasher rather than a copy of the
// chunk.
type hasherFunc struct{ h hash.Hash }

func newHasherFunc(a Algo) *hasherFunc { return &hasherFunc{h: newHasher(a)} }

func (f *hasherFunc) Write(b []byte) {
	_, _ = f.h.Write(b) //nolint:errcheck // hash.Hash.Write is documented never to fail.
}

func (f *hasherFunc) Sum() []byte { return f.h.Sum(nil) }

// constantTimeEqual compares two digests without an early exit. Neither is a
// secret, but one comparison style for both algorithms is simpler than
// arguing which one needs it.
func constantTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

// VerifyChunk is MatchChunk as a refusal, so a caller does not have to build
// the error at every call site.
func VerifyChunk(sum *Checksum, data []byte) error {
	if sum == nil {
		return nil
	}
	if err := checkDigestLen(sum.Algo, len(sum.Digest)); err != nil {
		return err
	}
	if !MatchChunk(*sum, data) {
		return fmt.Errorf("%w: the %s digest does not match the %d bytes received",
			ErrChecksum, sum.Algo, len(data))
	}
	return nil
}

// VerifyWholeFile streams length bytes of f from zero and compares the digest
// against what the caller expected.
//
// It takes both an algorithm and an expected digest, never one alone. The
// shape that shipped before carried only a selector, so verification computed
// a digest and logged it: it could never fail whatever arrived on disk.
//
// The read is through the same descriptor that took the chunk writes, which is
// the one place in the tree holding IntentReadWrite and the reason that intent
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
			// hash.Hash.Write never fails, so the only error worth handling
			// here is the read's.
			_, _ = h.Write(buf[:n]) //nolint:errcheck // as in Sum above.
			read, nerr := num.Narrow[uint64](n)
			if nerr != nil {
				return fmt.Errorf("verifying the upload: %w", nerr)
			}
			at += read
		}
		if rerr != nil {
			// Short of the declared length is a real failure here, unlike a
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
	if subtle.ConstantTimeCompare(h.Sum(nil), v.Digest) != 1 {
		return fmt.Errorf("%w: the finished file does not match the %s digest supplied",
			ErrVerify, v.Algo)
	}
	return nil
}
