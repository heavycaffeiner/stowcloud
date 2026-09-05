package vault

import (
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"

	"io"
)

// filesystem is the surface Root drives, whichever filesystem a container
// turns out to hold. VeraCrypt imposes no structure on the data area: it
// hands the decrypted bytes to whatever formatter made them, so a driver
// that reads only one filesystem reads only some containers.
type filesystem interface {
	Alive() error
	Space() (total, free uint64)
	Stat(p vfs.SafePath) (StatInfo, error)
	ReadDir(p vfs.SafePath) ([]Dirent, error)
	ReadFile(p vfs.SafePath, w io.Writer) error
	WriteFileStaged(dest vfs.SafePath, r io.Reader, noClobber bool, mtimeNs int64) (bool, error)
	CreateFile(p vfs.SafePath) error
	Truncate(p vfs.SafePath, newSize uint64) error
	SetModTime(p vfs.SafePath, mtimeNs int64) error
	Mkdir(p vfs.SafePath) error
	Rmdir(p vfs.SafePath) error
	Remove(p vfs.SafePath) error
	Rename(from, to vfs.SafePath, noReplace bool) error
	Sync() error
}

// exfatOEMName is what an exFAT formatter writes where a FAT BPB carries
// its jump instruction and OEM name. It is the one field that tells the two
// families apart before either BPB is trusted: an exFAT boot sector has no
// FAT BPB at all, so parsing it as one reads garbage.
const exfatOEMName = "EXFAT   "

// mountFilesystem probes the data area's first sector and brings up the
// driver that matches.
func mountFilesystem(dev Device, sizeBytes uint64, clk clock.Clock) (filesystem, error) {
	sector := make([]byte, 512)
	if _, err := dev.ReadAt(sector, 0); err != nil {
		return nil, fmt.Errorf("vault: read boot sector: %w", err)
	}
	if string(sector[3:11]) == exfatOEMName {
		return MountExFat(dev, sizeBytes, clk)
	}
	return Mount(dev, sizeBytes, clk)
}
