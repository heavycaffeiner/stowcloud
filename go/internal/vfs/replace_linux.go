//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReplaceFileDurable rewrites a trusted private control file: it stages beside
// the destination with the exact mode asked for, syncs, replaces the name and
// syncs the directory.
//
// It is not WriteDurable and it is not PublishNew. WriteDurable owns staged
// share content, with a mode and an owner to transplant from what it replaces;
// PublishNew owns an already-complete database and refuses to replace anything.
// This one replaces, and what it replaces is something only this server ever
// wrote: the master key ring is the caller. It never takes a share path, and
// nothing user-supplied reaches either name.
//
// The mode is applied with fchmod rather than left to the open, because O_CREAT
// filters the mode through umask and a key file at 0644 is the whole failure
// this exists to prevent.
func ReplaceFileDurable(path string, mode uint32, write func(*os.File) error) (err error) {
	dir := filepath.Dir(path)
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErrno("open "+dir, err)
	}
	d := os.NewFile(uintptr(fd), dir)
	defer closeAfter(d, dir)

	staged, err := stagingName()
	if err != nil {
		return err
	}

	sfd, err := withFd(d, func(dfd int) (int, error) {
		return unix.Openat(dfd, staged,
			unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	})
	if err != nil {
		return mapErrno("stage "+filepath.Base(path), err)
	}
	f := os.NewFile(uintptr(sfd), filepath.Join(dir, staged))

	// Go has no Drop, so the unlink of a staging file nothing published is an
	// explicit flag. Every return between here and the rename takes it.
	published := false
	defer func() {
		if published {
			return
		}
		if rerr := withFdErr(d, func(dfd int) error { return unix.Unlinkat(dfd, staged, 0) }); rerr != nil &&
			!errors.Is(rerr, unix.ENOENT) {
			err = errors.Join(err, fmt.Errorf("removing the staging file %s: %w", staged, rerr))
		}
	}()

	if err := f.Chmod(os.FileMode(mode)); err != nil {
		return errors.Join(fmt.Errorf("setting the mode of %s: %w", staged, err), f.Close())
	}
	if err := write(f); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := withFdErr(f, unix.Fsync); err != nil {
		return errors.Join(mapErrno("sync "+filepath.Base(path), err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the staging file %s: %w", staged, err)
	}

	if err := withFdErr(d, func(dfd int) error {
		return unix.Renameat(dfd, staged, dfd, filepath.Base(path))
	}); err != nil {
		return mapErrno("replacing "+filepath.Base(path), err)
	}
	published = true

	// Separate from the file's own sync, and this is the one that makes the
	// name durable: without it a power cut can leave the whole content under a
	// staging name nothing looks for.
	if err := withFdErr(d, unix.Fsync); err != nil {
		return fmt.Errorf("syncing %s after replacing %s: %w", dir, filepath.Base(path), err)
	}
	return nil
}
