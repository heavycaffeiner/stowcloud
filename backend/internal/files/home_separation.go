//go:build linux

package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// canonicalDirectory resolves existing ancestors even when the new Home
// directory has not been created yet. Symlink aliases cannot bypass separation.
func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = canonicalDirectory(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

func directoriesOverlap(a, b string) (bool, error) {
	a, err := canonicalDirectory(a)
	if err != nil {
		return false, err
	}
	b, err = canonicalDirectory(b)
	if err != nil {
		return false, err
	}
	within := func(child, parent string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a), nil
}

// ValidateHomesRoot prevents exposing private files through an ordinary share.
func (c *Core) ValidateHomesRoot(host string) error {
	for _, def := range c.Shares() {
		if IsHomeShare(def.ID) || (def.Backend != "" && def.Backend != BackendLocal) {
			continue
		}
		overlap, err := directoriesOverlap(host, def.Host)
		if err != nil {
			return err
		}
		if overlap {
			return ErrHomePathOverlap
		}
	}
	return nil
}

func (c *Core) validateShareOutsideHomes(host string) error {
	for _, def := range c.Shares() {
		if !IsHomeShare(def.ID) {
			continue
		}
		overlap, err := directoriesOverlap(host, def.Host)
		if err != nil {
			return err
		}
		if overlap {
			return ErrHomePathOverlap
		}
	}
	return nil
}
