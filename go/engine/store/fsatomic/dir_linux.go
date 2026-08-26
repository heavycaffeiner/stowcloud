//go:build linux

package fsatomic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// linuxDir is the durable control directory. Every operation goes through
// the one descriptor opened here, which is what lets the final sync make
// every rename issued through it durable.
type linuxDir struct {
	f    *os.File
	path string
}

func openControlDir(path string) (controlDir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening directory %s: %w", path, err)
	}
	return &linuxDir{f: os.NewFile(uintptr(fd), path), path: path}, nil
}

// withFd runs fn against the directory's raw descriptor and keeps the file
// reachable for the whole call. Fd takes the descriptor out of the
// runtime's view for that duration, so nothing else keeps the file alive;
// without KeepAlive a finalizer could close it while fn is still using it.
func (d *linuxDir) withFd(fn func(fd int) error) error {
	err := fn(int(d.f.Fd()))
	runtime.KeepAlive(d.f)
	return err
}

func (d *linuxDir) stage(name string, mode uint32) (*os.File, error) {
	var fd int
	err := d.withFd(func(dfd int) error {
		var oerr error
		fd, oerr = unix.Openat(dfd, name,
			unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		return oerr
	})
	if err != nil {
		return nil, fmt.Errorf("staging %s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.path, name))

	// The mode above is filtered through the process umask on creation, so
	// it is set again here, explicitly, on the already-open file: that is
	// the one call in this sequence umask cannot touch.
	if cerr := f.Chmod(os.FileMode(mode)); cerr != nil {
		return nil, errors.Join(
			fmt.Errorf("setting the mode of %s: %w", name, cerr), f.Close())
	}
	return f, nil
}

func (d *linuxDir) publish(oldName, newName string) error {
	err := d.withFd(func(dfd int) error {
		return unix.Renameat(dfd, oldName, dfd, newName)
	})
	if err != nil {
		return fmt.Errorf("renaming %s to %s: %w", oldName, newName, err)
	}
	return nil
}

func (d *linuxDir) remove(name string) error {
	err := d.withFd(func(dfd int) error {
		return unix.Unlinkat(dfd, name, 0)
	})
	if err != nil {
		return fmt.Errorf("removing staged file %s: %w", name, err)
	}
	return nil
}

func (d *linuxDir) sync() error {
	if err := d.withFd(unix.Fsync); err != nil {
		return fmt.Errorf("syncing directory %s: %w", d.path, err)
	}
	return nil
}

func (d *linuxDir) close() error {
	return d.f.Close()
}
