package vfs

import "fmt"

// ShareID names one configured share. It is an opaque number and nothing in
// this package derives a path from it.
type ShareID uint32

// Kind is what a directory entry or a stat result turned out to be.
type Kind uint8

const (
	// KindOther is also what a directory read reports for an entry whose type
	// the filesystem did not supply, rather than paying a statx per entry.
	KindOther Kind = iota
	KindFile
	KindDir
	KindSymlink
)

// IsDir is "== KindDir", not "can be traversed": a symlink to a directory
// answers false, because under the default symlink policy it cannot be entered
// at all.
func (k Kind) IsDir() bool { return k == KindDir }

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDir:
		return "dir"
	case KindSymlink:
		return "symlink"
	}
	return "other"
}

// Stat is the projection of statx this tree uses.
type Stat struct {
	Dev uint64
	Ino uint64

	// BtimeNs is nil where the filesystem carries no birth time, which some
	// ext4 mount options and NFS do not. An absent btime and a zero one are
	// different facts and the compat layer needs the difference, so it is
	// reported rather than faked.
	BtimeNs *int64

	MtimeNs int64

	// CtimeNs moves on a rename, which mtime does not, so it is what reports
	// when an entry was moved into the trash: being deleted leaves the file's
	// own contents and therefore its mtime untouched.
	CtimeNs *int64

	Size  uint64
	Mode  uint32
	UID   uint32
	GID   uint32
	Nlink uint32
	Kind  Kind
}

// SymlinkPolicy decides the resolve flags for a share.
type SymlinkPolicy uint8

const (
	// SymlinkDeny is the default: symlinks are visible in a listing but cannot
	// be opened or traversed.
	SymlinkDeny SymlinkPolicy = iota
	// SymlinkWithinShare follows a symlink as long as the target stays inside
	// the share root.
	SymlinkWithinShare
	// SymlinkFollow follows a relative symlink under RESOLVE_BENEATH. An
	// absolute target still fails and nothing reaches outside the share:
	// SymlinkWithinShare is the mode that rebases an absolute target, through
	// RESOLVE_IN_ROOT.
	SymlinkFollow
)

// ParseSymlinkPolicy is the trust boundary for the configured value.
//
// A name this build does not implement is a refusal rather than a warning and
// a fallback: an operator who wrote "within_share" and silently got "deny"
// believes a share follows symlinks that it does not, and the difference is
// invisible until somebody's link fails to open.
func ParseSymlinkPolicy(s string) (SymlinkPolicy, error) {
	switch s {
	case "deny":
		return SymlinkDeny, nil
	case "within_share":
		return SymlinkWithinShare, nil
	case "follow":
		return SymlinkFollow, nil
	}
	return 0, fmt.Errorf(
		"symlink policy %q is not one this build implements; it is \"deny\", \"within_share\" or \"follow\"", s)
}

// String is the configured spelling, so a round trip through the config and
// the settings surface is the same word.
func (p SymlinkPolicy) String() string {
	switch p {
	case SymlinkWithinShare:
		return "within_share"
	case SymlinkFollow:
		return "follow"
	}
	return "deny"
}

// Owner is the uid and gid a share applies to what it creates.
type Owner struct {
	UID uint32
	GID uint32
}

// SharePolicy is the per-share half of every resolution and every create.
type SharePolicy struct {
	Symlink SymlinkPolicy

	// CrossMount allows traversal into a filesystem mounted inside the share,
	// which is ordinary: a RAID array under media/ or a second disk under
	// archive/ is a different device and users browse straight into it.
	CrossMount bool

	// ModeFile and ModeDir are applied verbatim to what this server creates,
	// not filtered through umask.
	ModeFile uint32
	ModeDir  uint32

	// Chown is nil to leave what we create at the process uid and gid.
	Chown *Owner
}

// DefaultSharePolicy is the restrictive one: no symlink is followed, and the
// modes are the group-writable pair a shared folder needs so the neighbours
// keep their access.
func DefaultSharePolicy() SharePolicy {
	return SharePolicy{
		Symlink:    SymlinkDeny,
		CrossMount: true,
		ModeFile:   0o664,
		ModeDir:    0o775,
	}
}

// ReservedPolicy decides whether a directory read shows this server's own
// control files. It is an argument rather than ambient state: a caller that
// does not say what it is asking for inherits whatever the last caller wanted,
// and what a directory read returns is a security-relevant answer.
type ReservedPolicy uint8

const (
	// HideReserved is what every user-facing read passes.
	HideReserved ReservedPolicy = iota
	// IncludeReserved is for the trusted maintenance that has to find control
	// files: the upload orphan sweep and the trash collector. Never a request
	// path.
	IncludeReserved
)

// AccessIntent decides the access mode of a descriptor. Privilege on a handle
// is what the caller means to do, never what the file's mode happens to allow.
type AccessIntent uint8

const (
	// IntentRead is O_RDONLY, and is everything except the one site below.
	IntentRead AccessIntent = iota
	// IntentReadWrite is O_RDWR, for the upload engine's part-file handle
	// alone: it takes chunk writes and is read back at finalize to verify the
	// whole-file digest. The gate greps for a second call site.
	IntentReadWrite
)

// FsSpace is byte accounting for the filesystem behind one path, in bytes.
//
// A share is one directory tree, not one filesystem, so these answer for the
// filesystem holding the path asked about rather than the one holding the share
// root.
type FsSpace struct {
	Total uint64
	// Free is what the device has left, including the blocks the filesystem
	// reserves for root.
	Free uint64
	// Available is f_bavail: what an unprivileged writer can actually consume.
	// The reserved blocks in the difference are not ours to write into, and
	// reporting them as free promises an uploader room that ENOSPC refuses.
	Available uint64
}

// Used counts the root reserve as used, the way df does.
func (s FsSpace) Used() uint64 {
	if s.Free > s.Total {
		return 0
	}
	return s.Total - s.Free
}

// FsType is the filesystem gate's vocabulary.
type FsType uint64

// The named filesystems are their own statfs magic, so an unknown one is
// carried whole rather than lost.
const (
	FsExt4     FsType = 0xEF53
	FsBtrfs    FsType = 0x9123683E
	FsXfs      FsType = 0x58465342
	FsZfs      FsType = 0x2FC12FC1
	FsF2fs     FsType = 0xF2F52010
	FsTmpfs    FsType = 0x01021994
	FsOverlay  FsType = 0x794C7630
	FsFuse     FsType = 0x65735546
	FsNfs      FsType = 0x6969
	FsCifs     FsType = 0xFF534D42
	FsSmb2     FsType = 0xFE534D42
	FsSquashfs FsType = 0x73717368
	FsNtfs     FsType = 0x5346544E
)

func (t FsType) String() string {
	switch t {
	case FsExt4:
		return "ext4"
	case FsBtrfs:
		return "btrfs"
	case FsXfs:
		return "xfs"
	case FsZfs:
		return "zfs"
	case FsF2fs:
		return "f2fs"
	case FsTmpfs:
		return "tmpfs"
	case FsOverlay:
		return "overlay"
	case FsFuse:
		return "fuse"
	case FsNfs:
		return "nfs"
	case FsCifs, FsSmb2:
		return "cifs"
	case FsSquashfs:
		return "squashfs"
	case FsNtfs:
		return "ntfs"
	}
	return "unknown"
}

// Supported reports a filesystem a share may be registered on. This package
// does not act on it: registration is the config layer's boundary, and this is
// the fact it decides from.
//
// It is a whitelist, and the default answer is no. There is one file identity
// scheme in this server, derived from (share, dev, ino, btime), and every
// durable property, lock, favourite and share link is keyed by it. A filesystem
// whose inode numbers do not survive a remount, or that reports none at all,
// silently detaches all of that; a new value here that nobody classified would
// otherwise be admitted by omission.
//
// tmpfs is admitted and is the one that loses everything on reboot. That is a
// deployment's own decision and the warning belongs where a share is
// registered, not here.
func (t FsType) Supported() bool {
	switch t {
	case FsExt4, FsBtrfs, FsXfs, FsZfs, FsF2fs, FsTmpfs:
		return true
	}
	return false
}

// DirEntry is one name from a directory read.
type DirEntry struct {
	Name string
	Kind Kind

	// Ino comes back from getdents64 for free, so a caller can order a later
	// stat batch by (dev, ino). Filesystems lay inodes out in increasing order,
	// so asking for them that way makes the disk seek forward only and raises
	// the chance several inodes of interest share a block.
	Ino uint64
}
