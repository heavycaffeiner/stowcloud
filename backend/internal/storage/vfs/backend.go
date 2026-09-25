//go:build linux

package vfs

// Root is the product-facing storage seam. The local implementation delegates
// Linux descriptor mechanics to github.com/stowcloud/storage/local.
type Root interface {
	ID() ShareID
	Stat(SafePath) (Stat, error)
	ReadDir(SafePath, ReservedPolicy) ([]DirEntry, error)
	ReadDirFunc(SafePath, ReservedPolicy, func(DirEntry) bool) error
	OpenRead(SafePath, AccessIntent) (*File, error)
	CreatePart(SafePath) (*File, error)
	WriteDurable(SafePath, DurableOpts, func(*File) error) (Durable, error)
	PublishPart(SafePath, SafePath, bool) (Durable, error)
	SetTimes(SafePath, int64) error
	Mkdir(SafePath) error
	Rmdir(SafePath) error
	Unlink(SafePath) error
	Rename(SafePath, SafePath, bool) error
	Space(SafePath) (FsSpace, error)
	DirDev(SafePath) (uint64, error)
	Policy() SharePolicy
	Dev() uint64
	FsType() FsType
	HasBtime() bool
	IsScratch() bool
	Alive() error
	Close() error
}

var _ Root = (*ShareRoot)(nil)
