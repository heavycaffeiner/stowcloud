//go:build !linux

package fsatomic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// otherDir is the development-host control directory. It stages and
// renames by name rather than through a held descriptor and its sync does
// nothing: there is no portable way to fsync a directory outside Linux.
// This makes no durability claim; it exists only so a caller and its tests
// build and run on a non-Linux machine. The Linux file beside this one is
// what ships.
type otherDir struct {
	path string
}

func openControlDir(path string) (controlDir, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("opening directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("opening directory %s: not a directory", path)
	}
	return &otherDir{path: path}, nil
}

func (d *otherDir) stage(name string, mode uint32) (*os.File, error) {
	full := filepath.Join(d.path, name)
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode))
	if err != nil {
		return nil, fmt.Errorf("staging %s: %w", name, err)
	}
	if cerr := f.Chmod(os.FileMode(mode)); cerr != nil {
		return nil, errors.Join(
			fmt.Errorf("setting the mode of %s: %w", name, cerr), f.Close())
	}
	return f, nil
}

func (d *otherDir) publish(oldName, newName string) error {
	if err := os.Rename(filepath.Join(d.path, oldName), filepath.Join(d.path, newName)); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", oldName, newName, err)
	}
	return nil
}

func (d *otherDir) remove(name string) error {
	if err := os.Remove(filepath.Join(d.path, name)); err != nil {
		return fmt.Errorf("removing staged file %s: %w", name, err)
	}
	return nil
}

func (d *otherDir) sync() error { return nil }

func (d *otherDir) close() error { return nil }
