//go:build !linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReplaceFileDurable rewrites a trusted private control file by staging beside
// it and renaming over the name.
//
// This is the development host's copy, and it exists so control-file callers
// and their tests compile and run where they are written. It is not a
// durability claim: there is no directory sync here and the mode is whatever
// this platform makes of one. The file beside this one is what ships.
func ReplaceFileDurable(path string, mode uint32, write func(*os.File) error) (err error) {
	dir := filepath.Dir(path)
	name, err := stagingName()
	if err != nil {
		return err
	}
	staged := filepath.Join(dir, name)

	//nolint:gosec // the name is this function's own random hex beside the path it was handed.
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode))
	if err != nil {
		return fmt.Errorf("staging %s: %w", filepath.Base(path), err)
	}

	published := false
	defer func() {
		if published {
			return
		}
		if rerr := os.Remove(staged); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing the staging file %s: %w", name, rerr))
		}
	}()

	if err := f.Chmod(os.FileMode(mode)); err != nil {
		return errors.Join(fmt.Errorf("setting the mode of %s: %w", name, err), f.Close())
	}
	if err := write(f); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("syncing %s: %w", name, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the staging file %s: %w", name, err)
	}
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("replacing %s: %w", filepath.Base(path), err)
	}
	published = true
	return nil
}
