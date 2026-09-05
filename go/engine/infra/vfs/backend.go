//go:build linux

package vfs

// The storage seam.
//
// Every share used to be a directory on this machine, so every caller held a
// *ShareRoot and the type was the contract. Two share kinds now are not
// directories: an S3-compatible bucket and a VeraCrypt container file. Both
// answer the same questions a directory does, so the questions became an
// interface and the directory became one implementation of it.
//
// The handle type stayed concrete on purpose. A *File is a real descriptor on
// a real filesystem, which is what lets a download use sendfile, a copy use
// copy_file_range, and a thumbnail hand its fd to a jailed decoder. A backend
// whose bytes live elsewhere materializes them into server-owned scratch
// space and hands back a descriptor onto that, so none of those paths need a
// second implementation and none of them can silently lose the guarantee.

// Root is one share's storage, whatever holds it.
//
// The method set is exactly what the domain above it calls, and no more: this
// is a seam extracted from one implementation, not a filesystem API somebody
// might want. Every path argument is a SafePath, so a backend receives
// validated components and never parses a path itself.
type Root interface {
	// ID is the share this root serves. Zero for scratch space, which is
	// what IsScratch reports instead of a reserved id.
	ID() ShareID

	// Stat answers for one entry.
	Stat(p SafePath) (Stat, error)

	// ReadDir lists a directory, bounded by the implementation.
	ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error)

	// ReadDirFunc streams a directory, stopping when fn returns false.
	ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error

	// OpenRead opens an existing file for the stated intent.
	OpenRead(p SafePath, intent AccessIntent) (*File, error)

	// CreatePart creates the upload engine's part file.
	CreatePart(p SafePath) (*File, error)

	// WriteDurable writes a file whole and publishes it atomically.
	WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error)

	// PublishPart moves a finished part file onto its destination.
	PublishPart(part, dest SafePath, replacing bool) (Durable, error)

	// SetTimes sets an entry's modification time.
	SetTimes(p SafePath, mtimeNs int64) error

	Mkdir(p SafePath) error
	Rmdir(p SafePath) error
	Unlink(p SafePath) error

	// Rename moves an entry within this root.
	Rename(from, to SafePath, noReplace bool) error

	// Space reports byte accounting for whatever holds p.
	Space(p SafePath) (FsSpace, error)

	// DirDev is the device number for a directory, which a caller pairs with
	// an inode to identify it.
	DirDev(p SafePath) (uint64, error)

	// Policy holds the symlink, mode and ownership decisions.
	Policy() SharePolicy

	// Dev is the device the root itself sits on.
	Dev() uint64

	// FsType classifies what holds this root.
	FsType() FsType

	// HasBtime reports whether entries carry a birth time. False means every
	// identity this root produces has an absent birth time, which callers
	// pairing identity across a restart have to tolerate.
	HasBtime() bool

	// IsScratch reports server-owned scratch space rather than a share.
	IsScratch() bool

	// Alive reports whether the backing store still answers, so a health
	// probe can move a share between live and broken. It is never a
	// security decision.
	Alive() error

	// Close releases whatever the root holds open.
	Close() error
}

// ShareRoot is the local-directory implementation, and the only one in this
// package. The assertion is here rather than at each method so a signature
// drifting from the interface fails the build in one place.
var _ Root = (*ShareRoot)(nil)
