//go:build linux

package vault

import (
	"errors"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/stowcloud/veracrypt"
)

const (
	minContainerDataMiB = 16
	maxContainerDataMiB = 1 << 20
)

var (
	ErrContainerSize         = veracrypt.ErrContainerSize
	ErrUnknownHash           = veracrypt.ErrUnknownHash
	ErrWrongPassword         = veracrypt.ErrWrongPassword
	ErrHeaderCorrupt         = veracrypt.ErrHeaderCorrupt
	ErrUnsupportedVolume     = veracrypt.ErrUnsupportedVolume
	ErrHeaderFieldsInvalid   = veracrypt.ErrHeaderFieldsInvalid
	ErrUnsupportedFilesystem = veracrypt.ErrUnsupportedFilesystem
)

type StatInfo struct {
	IsDir   bool
	Size    uint64
	MtimeNs int64
	Ino     uint64
}

type Dirent struct {
	Name  string
	IsDir bool
	Ino   uint64
}

type filesystem interface {
	Alive() error
	Space() (uint64, uint64)
	Stat(vfs.SafePath) (StatInfo, error)
	ReadDir(vfs.SafePath) ([]Dirent, error)
	ReadFile(vfs.SafePath, io.Writer) error
	WriteFileStaged(vfs.SafePath, io.Reader, bool, int64) (bool, error)
	CreateFile(vfs.SafePath) error
	Truncate(vfs.SafePath, uint64) error
	SetModTime(vfs.SafePath, int64) error
	Mkdir(vfs.SafePath) error
	Rmdir(vfs.SafePath) error
	Remove(vfs.SafePath) error
	Rename(vfs.SafePath, vfs.SafePath, bool) error
	Sync() error
}

func validateHashToken(token string) error {
	switch token {
	case "", "sha512", "sha256", "blake2s", "whirlpool", "streebog", "argon2":
		return nil
	default:
		return errors.Join(ErrUnknownHash, errors.New(token))
	}
}
