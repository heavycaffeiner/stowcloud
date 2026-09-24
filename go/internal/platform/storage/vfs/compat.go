//go:build linux

package vfs

import (
	"errors"

	local "github.com/stowcloud/storage/local"
)

var ErrSandboxDenied = errors.New("vfs: the sandbox does not grant this path")

func classifyUnreadable(err error) error {
	if errors.Is(err, ErrDenied) {
		return errors.Join(ErrSandboxDenied, err)
	}
	return err
}
func CopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error) {
	return local.CopyRange(local.WrapFile(src.OSFile()), local.WrapFile(dst.OSFile()), srcOff, dstOff, n)
}
