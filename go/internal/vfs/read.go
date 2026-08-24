//go:build linux

package vfs

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"golang.org/x/sys/unix"
)

// dirBufBytes is the reused getdents buffer. One buffer per walk, whatever the
// directory holds.
const dirBufBytes = 32 << 10

// linux_dirent64 field offsets. getdents64 lays a record out the same way on
// every architecture, so these are the kernel ABI rather than a local
// assumption: d_ino at 0, d_off at 8, d_reclen at 16, d_type at 18, and a
// NUL-terminated d_name from 19 to the end of the record.
const (
	direntOffIno    = 0
	direntOffReclen = 16
	direntOffType   = 18
	direntOffName   = 19
)

// ReadDirFunc streams the entries of p, calling fn once per entry. It never
// materialises the directory: the product's premise is that other programs
// write these directories, so their size is not ours to assume, and a
// several-million-entry directory is bounded by available memory and nothing
// else the moment something collects it first.
//
// fn returning false stops the walk cleanly. The name handed to fn is a fresh
// string and outlives the call; the buffer behind it does not.
func (r *ShareRoot) ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error {
	d, err := r.openLeaf(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer closeAfter(d, "directory being read")

	buf := make([]byte, dirBufBytes)
	for {
		n, err := withFd(d, func(fd int) (int, error) { return unix.Getdents(fd, buf) })
		if err != nil {
			return mapErrno("read directory", err)
		}
		if n == 0 {
			return nil
		}
		for off := 0; off < n; {
			entry, reclen, err := parseDirent(buf[off:n])
			if err != nil {
				return err
			}
			off += reclen
			if entry.Name == "." || entry.Name == ".." {
				continue
			}
			if policy == HideReserved && IsReservedName(entry.Name) {
				continue
			}
			if !fn(entry) {
				return nil
			}
		}
	}
}

// ReadDir collects entries into a slice, refusing past the buffered-read bound.
// A caller that cannot tolerate a refusal streams with ReadDirFunc instead.
func (r *ShareRoot) ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error) {
	return collectBounded(limits.DirEntriesBuffered, func(fn func(DirEntry) bool) error {
		return r.ReadDirFunc(p, policy, fn)
	})
}

// collectBounded is the buffering half of ReadDir, separated from the syscalls
// so that a test can prove the bound is what refuses rather than that a large
// directory happens to fail.
func collectBounded(bound int, stream func(func(DirEntry) bool) error) ([]DirEntry, error) {
	out := make([]DirEntry, 0, 64)
	over := false
	err := stream(func(e DirEntry) bool {
		if len(out) >= bound {
			over = true
			return false
		}
		out = append(out, e)
		return true
	})
	if err != nil {
		return nil, err
	}
	if over {
		return nil, limits.Exceed("directory entries buffered", int64(bound), int64(bound)+1)
	}
	return out, nil
}

// parseDirent reads one record and returns its length so the caller can step to
// the next.
//
// The records are walked directly rather than through unix.ParseDirent, which
// returns names only: d_type is what keeps a directory read from paying a statx
// per entry, and d_ino is what lets a caller order a later stat batch by
// (dev, ino).
func parseDirent(rec []byte) (DirEntry, int, error) {
	if len(rec) < direntOffName {
		return DirEntry{}, 0, fmt.Errorf(
			"read directory: %d bytes cannot hold a dirent header", len(rec))
	}
	reclen := int(binary.NativeEndian.Uint16(rec[direntOffReclen:]))
	if reclen <= direntOffName || reclen > len(rec) {
		return DirEntry{}, 0, fmt.Errorf(
			"read directory: a record claims %d bytes of the %d returned", reclen, len(rec))
	}
	name := rec[direntOffName:reclen]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return DirEntry{
		Name: string(name),
		Kind: kindOfDirentType(rec[direntOffType]),
		Ino:  binary.NativeEndian.Uint64(rec[direntOffIno:]),
	}, reclen, nil
}

// kindOfDirentType stays conservative on DT_UNKNOWN, which some filesystems and
// most FUSE mounts return: KindOther rather than a statx per entry. A caller
// that needs certainty stats the specific entry.
func kindOfDirentType(t uint8) Kind {
	switch t {
	case unix.DT_DIR:
		return KindDir
	case unix.DT_REG:
		return KindFile
	case unix.DT_LNK:
		return KindSymlink
	}
	return KindOther
}
