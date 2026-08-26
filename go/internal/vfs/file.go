//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"golang.org/x/sys/unix"
)

// isEOF folds the two ways a short read reports itself. (*os.File).ReadAt
// returns io.EOF when it could not fill the slice, which is a short copy here
// rather than a failure.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// copyBufBytes bounds the userspace fallback copy. No chunk is ever fully in
// memory, whatever the caller asked to copy.
const copyBufBytes = 256 << 10

// File is an open regular file inside a share. It owns its descriptor and the
// caller must Close it: there is no Drop here, so the lifetime is the author's
// problem on every path including the error paths.
type File struct{ f *os.File }

func (f *File) Close() error { return f.f.Close() }

// Name is the spelling this handle was opened under, for diagnostics only.
func (f *File) Name() string { return f.f.Name() }

// ReadAt is pread: it never touches the descriptor's own cursor, so two callers
// on one handle do not move each other's position.
func (f *File) ReadAt(b []byte, off int64) (int, error) { return f.f.ReadAt(b, off) }

// WriteAt is pwrite, for the same reason.
func (f *File) WriteAt(b []byte, off int64) (int, error) { return f.f.WriteAt(b, off) }

func (f *File) Truncate(n int64) error { return f.f.Truncate(n) }

// Stat answers from the descriptor, so it describes the file this handle holds
// rather than whatever currently answers to its name. Stat.Dev is the device it
// sits on, which a caller holding two handles compares to tell whether they are
// on one filesystem.
func (f *File) Stat() (Stat, error) { return statOf(f.f) }

// Space reports the filesystem behind this handle, the same accounting
// ShareRoot.Space answers for a path. It is for the caller that already holds
// the file open and would otherwise re-resolve a name it has in hand.

func (f *File) Space() (FsSpace, error) { return spaceOf(f.f) }

// SyncData makes the contents durable. It is fdatasync rather than fsync: the
// name is made durable separately, by syncing the parent directory, and doing
// both here would pay for the metadata twice.
func (f *File) SyncData() error {
	if err := withFdErr(f.f, unix.Fdatasync); err != nil {
		return mapErrno("sync file data", err)
	}
	return nil
}

// SetMode applies a mode to the open handle.
//
// This is what a replacement needs before it is published: without the
// original's mode, the other services sharing the directory lose access to a
// file they could read a moment earlier.
func (f *File) SetMode(mode uint32) error {
	if err := withFdErr(f.f, func(fd int) error { return unix.Fchmod(fd, mode) }); err != nil {
		return mapErrno("apply mode", err)
	}
	return nil
}

// SetOwner applies a uid and gid to the open handle.
//
// EPERM here is the ordinary answer for an unprivileged process and is returned
// rather than swallowed, so the caller decides whether it matters.
func (f *File) SetOwner(o Owner) error { return chownFd(f.f, o) }

// CopyRange copies n bytes from src to dst with copy_file_range: a reflink on
// btrfs and XFS when aligned, an in-kernel copy otherwise, and no userspace
// round trip either way.
//
// It loops, because a short copy is documented even on success, and falls back
// to a bounded buffered loop for the remainder on the three errnos that mean
// "not here": EXDEV, which happens inside one share when a subdirectory is a
// separate mount, EOPNOTSUPP, and ENOSYS. Any other errno is real and is
// returned.
//
// Explicit offsets, never nil: this package reads and writes positionally
// throughout, so letting the syscall advance a descriptor's own cursor would
// move it under whichever caller reads it next.
func CopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	var copied uint64
	for copied < n {
		remaining := n - copied
		want, err := num.Narrow[int](remaining)
		if err != nil {
			want = int(^uint(0) >> 1)
		}
		in, err := offsetOf(srcOff + copied)
		if err != nil {
			return copied, err
		}
		out, err := offsetOf(dstOff + copied)
		if err != nil {
			return copied, err
		}

		got, cerr := withFd2(src.f, dst.f, func(s, d int) (int, error) {
			return unix.CopyFileRange(s, &in, d, &out, want, 0)
		})
		switch {
		case cerr == nil && got == 0:
			// The source ran out before n was reached, which mirrors the
			// syscall's own short-copy contract.
			return copied, nil
		case cerr == nil:
			n64, nerr := num.Narrow[uint64](got)
			if nerr != nil {
				return copied, fmt.Errorf("copy range: %w", nerr)
			}
			copied += n64
		case errors.Is(cerr, unix.EXDEV),
			errors.Is(cerr, unix.EOPNOTSUPP),
			errors.Is(cerr, unix.ENOSYS):
			rest, berr := bufferedCopyRange(src, srcOff+copied, dst, dstOff+copied, n-copied)
			return copied + rest, berr
		default:
			return copied, mapErrno("copy range", cerr)
		}
	}
	return copied, nil
}

func offsetOf(v uint64) (int64, error) {
	off, err := num.Narrow[int64](v)
	if err != nil {
		return 0, fmt.Errorf("copy range offset: %w", err)
	}
	return off, nil
}

// bufferedCopyRange is the one userspace copy loop in this package, so the
// bounded-memory argument is made in one place.
func bufferedCopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error) {
	buf := make([]byte, copyBufBytes)
	var copied uint64
	for copied < n {
		want := uint64(copyBufBytes)
		if remaining := n - copied; remaining < want {
			want = remaining
		}
		rOff, err := offsetOf(srcOff + copied)
		if err != nil {
			return copied, err
		}
		got, err := src.ReadAt(buf[:want], rOff)
		if got > 0 {
			wOff, oerr := offsetOf(dstOff + copied)
			if oerr != nil {
				return copied, oerr
			}
			if _, werr := dst.WriteAt(buf[:got], wOff); werr != nil {
				return copied, werr
			}
			n64, nerr := num.Narrow[uint64](got)
			if nerr != nil {
				return copied, fmt.Errorf("copy range: %w", nerr)
			}
			copied += n64
		}
		if err != nil {
			// EOF before n was reached is the same short copy the kernel
			// primitive reports, so callers that fell back mid-copy do not have
			// to branch on which path they took.
			if isEOF(err) {
				return copied, nil
			}
			return copied, err
		}
		if got == 0 {
			return copied, nil
		}
	}
	return copied, nil
}

// SendJob passes this file's descriptor and one more to another process over a
// unix socket, as an SCM_RIGHTS control message alongside msg.
//
// It lives here because a descriptor must not leave this package raw: the
// keepalive rule exists so a file cannot be collected while a syscall still
// holds its number, and a caller reaching for the raw number to build the
// control message would be a third site doing that by hand.
//
// This is what hands the preview worker its input. The worker is never told a
// path, so a descriptor the parent opened is the only way it reaches a file.
func (f *File) SendJob(sock int, msg []byte, out *os.File) error {
	_, err := withFd2(f.f, out, func(a, b int) (struct{}, error) {
		rights := unix.UnixRights(a, b)
		return struct{}{}, unix.Sendmsg(sock, msg, rights, nil, 0)
	})
	return err
}

// OSFile is the underlying file, for the two callers that must pass this
// descriptor to another process.
//
// It returns the *os.File rather than the number, so the descriptor stays
// owned by something the runtime can see and the keepalive rule still holds at
// the point it is used. The preview pool is the caller: its worker is never
// told a path, so a descriptor the parent opened is the only way it reaches a
// file.
func (f *File) OSFile() *os.File { return f.f }
