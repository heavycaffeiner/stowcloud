//go:build !linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PublishNew renames staged onto final, refusing rather than replacing if
// final already exists.
//
// This is the development host's copy, and it is here for one reason: the
// one-shot import of an older data directory is ordinary file handling with
// one rename at the end, and its tests are worth running where they are
// written. It is not a portable filesystem backend and nothing else in this
// package has one; the shipping target is Linux and the file beside this one
// is what runs there.
//
// Two things the Linux side has that this cannot: the refusal is a check
// rather than the kernel's own RENAME_NOREPLACE, and the directory is not
// synced afterwards. Neither is load-bearing here, because nothing that is
// tested on this host is being made durable on it.
func PublishNew(final, staged string) error {
	dir := filepath.Dir(final)
	if filepath.Dir(staged) != dir {
		return fmt.Errorf("publishing %s: the staged file is in another directory",
			filepath.Base(final))
	}
	if _, err := os.Stat(final); err == nil {
		return fmt.Errorf("publishing %s: %w", filepath.Base(final), ErrExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("publishing %s: %w", filepath.Base(final), err)
	}
	if err := os.Rename(staged, final); err != nil {
		return fmt.Errorf("publishing %s: %w", filepath.Base(final), err)
	}
	return nil
}
