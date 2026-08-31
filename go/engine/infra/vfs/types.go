package vfs

import "fmt"

// ShareID identifies a registered share. It carries no path information of
// its own; nothing in this package derives a filesystem location from it.
type ShareID uint32

// Kind classifies a directory entry or a stat result.
type Kind uint8

const (
	// KindOther also covers a directory entry the kernel gave no type for
	// during a raw dirent read, rather than paying a statx per entry to find
	// out.
	KindOther Kind = iota
	KindFile
	KindDir
	KindSymlink
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDir:
		return "dir"
	case KindSymlink:
		return "symlink"
	default:
		return "other"
	}
}

// IsDir is true only for KindDir, not for anything that merely leads to one.
// A symlink to a directory answers false: under the default symlink policy it
// cannot be traversed, so asking "is this a directory" means "can I walk into
// it," and an unopenable link cannot.
func (k Kind) IsDir() bool { return k == KindDir }

// Stat is this package's projection of a statx result.
type Stat struct {
	Dev uint64
	Ino uint64

	// BtimeNs is nil when the filesystem reported no birth time at all,
	// which some ext4 mount options and NFS both do. Nil and zero are
	// different facts here on purpose: a caller building an identity tuple
	// off Btime needs to tell "unknown" apart from "epoch."
	BtimeNs *int64

	MtimeNs int64

	// CtimeNs moves whenever an entry is renamed, unlike MtimeNs, so it is
	// what tells a caller a file was relocated (into a trash directory, say)
	// without its own content having changed. Nil when the read did not
	// report it.
	CtimeNs *int64

	Size  uint64
	Mode  uint32
	UID   uint32
	GID   uint32
	Nlink uint32
	Kind  Kind
}

// SymlinkPolicy governs how a share resolves a symlink it finds.
type SymlinkPolicy uint8

const (
	// SymlinkDeny is the conservative default: a symlink shows up in a
	// listing but neither opens nor traverses.
	SymlinkDeny SymlinkPolicy = iota
	// SymlinkWithinShare follows a symlink whose target, once rebased
	// against the share root, still lands inside the share.
	SymlinkWithinShare
	// SymlinkFollow follows a relative target that stays beneath the share
	// root. An absolute target is still refused outright; unlike
	// SymlinkWithinShare, nothing here rebases it.
	SymlinkFollow
)

// ParseSymlinkPolicy is the trust boundary for a configured policy string. An
// operator who typed a value this build has no case for gets a refusal, not a
// quiet fallback to SymlinkDeny: a silent substitution would leave them
// believing their share follows links it in fact blocks.
func ParseSymlinkPolicy(s string) (SymlinkPolicy, error) {
	switch s {
	case "deny":
		return SymlinkDeny, nil
	case "within_share":
		return SymlinkWithinShare, nil
	case "follow":
		return SymlinkFollow, nil
	default:
		return 0, fmt.Errorf("vfs: unknown symlink policy %q, want deny, within_share or follow", s)
	}
}

func (p SymlinkPolicy) String() string {
	switch p {
	case SymlinkWithinShare:
		return "within_share"
	case SymlinkFollow:
		return "follow"
	default:
		return "deny"
	}
}

// Owner is a uid/gid pair a share applies to what it creates.
type Owner struct {
	UID uint32
	GID uint32
}

// SharePolicy holds the per-share settings every resolution and every create
// call consults.
type SharePolicy struct {
	Symlink SymlinkPolicy

	// CrossMount allows a resolution to walk into a filesystem mounted below
	// the share root. This is the ordinary case: a second disk mounted under
	// a share subdirectory is meant to be browsable, not walled off.
	CrossMount bool

	// ModeFile and ModeDir are the exact bits this share applies to whatever
	// it creates. Never filtered through the process umask.
	ModeFile uint32
	ModeDir  uint32

	// Chown is nil when creation should leave ownership at the process uid
	// and gid.
	Chown *Owner
}

// DefaultSharePolicy is the conservative starting point: no symlink is
// followed, mounts inside the share are crossed, and the modes are the
// group-writable pair a shared folder needs to keep other members of the
// group able to reach what gets created.
func DefaultSharePolicy() SharePolicy {
	return SharePolicy{
		Symlink:    SymlinkDeny,
		CrossMount: true,
		ModeFile:   0o664,
		ModeDir:    0o775,
	}
}

// ReservedPolicy tells a directory read whether to show this package's own
// control names. It is always an explicit call argument, never a package
// default: what a listing returns is itself a security-relevant decision, and
// a caller silently inheriting the previous caller's intent is exactly how a
// part file in progress ends up visible where it should not be.
type ReservedPolicy uint8

const (
	// HideReserved is what every caller serving a user-facing listing passes.
	HideReserved ReservedPolicy = iota
	// IncludeReserved is for the two trusted maintenance sweeps that need to
	// find control names: the orphaned-upload collector and the trash
	// collector. Never used to answer a request.
	IncludeReserved
)

// AccessIntent states what a caller means to do with a descriptor, since a
// file's own mode bits are not a statement of the caller's intent.
type AccessIntent uint8

const (
	// IntentRead opens O_RDONLY. This covers every read except the one
	// exception below.
	IntentRead AccessIntent = iota
	// IntentReadWrite opens O_RDWR, reserved for the upload engine's
	// part-file handle: it both receives chunk writes and, once complete, is
	// read back to verify a whole-file digest.
	IntentReadWrite
)

// FsSpace reports byte accounting for whatever filesystem holds the path
// asked about, which is not necessarily the filesystem holding the share
// root: a share is a directory tree, and a device mounted under it reports
// its own numbers.
type FsSpace struct {
	Total uint64
	// Free is every block the device has left, the reserve for root
	// included.
	Free uint64
	// Available is statfs's f_bavail: what an unprivileged writer can
	// actually use. The gap between Free and Available belongs to the
	// filesystem, not to a caller of this package, so it is never reported
	// as room an uploader can spend.
	Available uint64
}

// Used follows df's own convention and counts the root reserve as used.
func (s FsSpace) Used() uint64 {
	if s.Free > s.Total {
		return 0
	}
	return s.Total - s.Free
}

// FsType is a statfs magic number, carried whole so a value this build has
// not classified is not lost, only refused.
type FsType uint64

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
	default:
		return "unknown"
	}
}

// DirEntry is one name read out of a directory.
type DirEntry struct {
	Name string
	Kind Kind

	// Ino comes back from the raw directory read at no extra cost, so a
	// caller batching a later stat pass can sort by (dev, ino): filesystems
	// tend to lay inodes out in ascending order, so stating in that order
	// turns the batch into a forward-only disk seek.
	Ino uint64
}
