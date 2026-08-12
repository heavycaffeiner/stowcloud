//go:build linux

package vfs

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// PublishNew renames staged onto final and syncs the directory holding them,
// refusing rather than replacing if final already exists.
//
// It is for a file built beside the name it will take, by something that is
// not writing into a share: the one-shot import of an older data directory is
// the caller. Share content goes through WriteDurable, which has a mode, an
// owner and a staging name to look after and none of which apply here.
//
// Both names are taken relative to the same directory, which is what makes the
// rename atomic: a rename across filesystems is a copy, and a copy is not.
func PublishNew(final, staged string) error {
	dir := filepath.Dir(final)
	if filepath.Dir(staged) != dir {
		return fmt.Errorf("publishing %s: the staged file is in another directory",
			filepath.Base(final))
	}

	// O_RDONLY rather than O_PATH: the rename would take either, and the fsync
	// that has to follow it takes only a real descriptor.
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErrno("open "+dir, err)
	}
	d := os.NewFile(uintptr(fd), dir)
	defer closeAfter(d, dir)

	if err := withFdErr(d, func(fd int) error {
		// RENAME_NOREPLACE, so the refusal is the kernel's and not a check
		// something could win a race against.
		return unix.Renameat2(fd, filepath.Base(staged), fd, filepath.Base(final),
			unix.RENAME_NOREPLACE)
	}); err != nil {
		return mapErrno("publishing "+filepath.Base(final), err)
	}

	// Without this a power cut can leave the whole file under the staged name,
	// which nothing will look for.
	if err := withFdErr(d, unix.Fsync); err != nil {
		return fmt.Errorf("syncing %s after publishing %s: %w", dir, filepath.Base(final), err)
	}
	return nil
}
