// Package fsatomic durably replaces one file, or a small set of files, that
// this server exclusively owns: a key ring, a credential sidecar, a
// rendered config, an index snapshot, a TLS certificate and its key. It
// never takes a share root, a validated path type, or anything routed
// through a symlink policy; every path here is a plain string this server
// chose for itself, never one a client supplied.
package fsatomic

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// controlDir is a directory this package holds open across a stage, a
// publish and a final sync, so the rename and the fsync that makes it
// durable both act through the same descriptor. Implemented once per
// platform, because the directory-fsync guarantee itself does not exist
// everywhere.
type controlDir interface {
	// stage creates name inside the directory with O_EXCL, refusing a
	// collision rather than clobbering it, and sets mode on the returned
	// file regardless of the process umask.
	stage(name string, mode uint32) (*os.File, error)
	// publish renames oldName onto newName inside the directory, replacing
	// whatever newName already names.
	publish(oldName, newName string) error
	// remove unlinks name inside the directory. A name that was never
	// created, or already removed, is reported through fs.ErrNotExist.
	remove(name string) error
	// sync makes every publish and remove call already issued durable. On
	// a platform with no directory-fsync guarantee this does nothing.
	sync() error
	close() error
}

// dirOpener opens the control directory a replace stages and publishes
// through. It is a parameter rather than a package variable, so a test in
// this package can substitute a counting opener without any ambient
// mutable state.
type dirOpener func(path string) (controlDir, error)

// ReplaceFileDurable rewrites path with the content write puts into the
// file it is handed, so the call either lands write's full output under
// path or leaves path exactly as it was.
//
// The sequence: open path's directory once, stage a new file inside it
// under a random name with O_EXCL, apply mode with Chmod so umask cannot
// narrow or widen it, run write, fsync the staged file, close it, rename
// it onto path's own name through the held directory, then fsync the
// directory so the rename itself is not lost to a crash. Any return before
// the rename unlinks the staged file; a staged file that was never created
// counts as already removed, not as a failure.
func ReplaceFileDurable(path string, mode uint32, write func(*os.File) error) error {
	return replaceFileDurable(path, mode, write, openControlDir)
}

func replaceFileDurable(path string, mode uint32, write func(*os.File) error, open dirOpener) (err error) {
	dirPath := filepath.Dir(path)
	d, err := open(dirPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := d.close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing directory %s: %w", dirPath, cerr))
		}
	}()

	name, err := stagingName()
	if err != nil {
		return err
	}

	published := false
	defer func() {
		if published {
			return
		}
		if rerr := d.remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}()

	f, err := d.stage(name, mode)
	if err != nil {
		return err
	}
	if werr := write(f); werr != nil {
		return errors.Join(werr, f.Close())
	}
	if serr := f.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("syncing staged file for %s: %w", path, serr), f.Close())
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("closing staged file for %s: %w", path, cerr)
	}

	if perr := d.publish(name, filepath.Base(path)); perr != nil {
		return perr
	}
	published = true

	// This fsync is what makes the rename itself durable, separately from
	// the file's own content sync above: without it a crash can leave the
	// content in place under the staged name with nothing pointing at it
	// under the real one.
	if serr := d.sync(); serr != nil {
		return fmt.Errorf("syncing %s after publishing %s: %w", dirPath, filepath.Base(path), serr)
	}
	return nil
}

// Unit is one destination written as part of a multi-file durable replace:
// a full path and the mode its content takes.
type Unit struct {
	Path string
	Mode uint32
}

// ReplaceFilesDurable stages every unit beside its own destination, fsyncs
// each as it finishes, renames every one onto its destination in the order
// given, then fsyncs every distinct directory involved exactly once.
//
// If any unit's write or fsync fails, every unit staged so far, this one
// included, is unlinked and nothing is renamed. Once staging succeeds for
// every unit, renaming begins; a rename failure partway through stops the
// sequence there. This function does not make the set of renames atomic as
// a group: there is no multi-file rename in POSIX, and a crash between
// renaming one unit and the next leaves one destination updated and the
// other at its prior content. A caller writing a logically connected set of
// files states its own rule for detecting that half-updated state at
// startup; this function guarantees only that each individual file is
// never torn and that every directory is synced once every rename it holds
// has been attempted.
func ReplaceFilesDurable(units []Unit, write func(i int, f *os.File) error) error {
	return replaceUnitsDurable(units, write, -1, openControlDir)
}

// ReplaceFilesDurableStopAfterRenameForTest behaves like ReplaceFilesDurable
// but returns immediately once the unit at index stopAfter has been
// renamed, without renaming any unit after it. It exists so a caller's own
// test can reproduce the one crash window ReplaceFilesDurable's contract
// leaves open, between two renames, without reimplementing this package's
// staging and renaming sequence by hand. Production code never calls this;
// stopAfter has no meaning outside a test that wants to inspect the
// half-updated state on disk afterward.
func ReplaceFilesDurableStopAfterRenameForTest(units []Unit, write func(i int, f *os.File) error, stopAfter int) error {
	return replaceUnitsDurable(units, write, stopAfter, openControlDir)
}

func replaceUnitsDurable(units []Unit, write func(i int, f *os.File) error, stopAfter int, open dirOpener) (err error) {
	if len(units) == 0 {
		return nil
	}

	dirs, dirOf, err := openUnitDirs(units, open)
	if err != nil {
		return err
	}
	defer func() {
		for _, d := range dirs {
			if cerr := d.close(); cerr != nil {
				err = errors.Join(err, cerr)
			}
		}
	}()

	names := make([]string, len(units))
	published := make([]bool, len(units))
	simulatedStop := false
	defer func() {
		if simulatedStop {
			return
		}
		for i := range units {
			if published[i] || names[i] == "" {
				continue
			}
			d := dirs[dirOf[i]]
			if rerr := d.remove(names[i]); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				err = errors.Join(err, rerr)
			}
		}
	}()

	for i, unit := range units {
		name, nerr := stagingName()
		if nerr != nil {
			return nerr
		}
		names[i] = name

		d := dirs[dirOf[i]]
		f, serr := d.stage(name, unit.Mode)
		if serr != nil {
			return serr
		}
		if werr := write(i, f); werr != nil {
			return errors.Join(werr, f.Close())
		}
		if serr := f.Sync(); serr != nil {
			return errors.Join(fmt.Errorf("syncing staged file for %s: %w", unit.Path, serr), f.Close())
		}
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("closing staged file for %s: %w", unit.Path, cerr)
		}
	}

	for i, unit := range units {
		d := dirs[dirOf[i]]
		if perr := d.publish(names[i], filepath.Base(unit.Path)); perr != nil {
			return perr
		}
		published[i] = true
		if stopAfter >= 0 && i == stopAfter {
			simulatedStop = true
			return nil
		}
	}

	for _, d := range dirs {
		if serr := d.sync(); serr != nil {
			return fmt.Errorf("syncing a directory after publishing every unit: %w", serr)
		}
	}
	return nil
}

// openUnitDirs opens each distinct directory named by units exactly once,
// in the order it is first seen, and reports which opened directory each
// unit belongs to. Two units under the same directory therefore share one
// descriptor and one final sync instead of two.
func openUnitDirs(units []Unit, open dirOpener) (dirs []controlDir, dirOf []int, err error) {
	seen := make(map[string]int, len(units))
	dirOf = make([]int, len(units))
	for i, unit := range units {
		dirPath := filepath.Dir(unit.Path)
		idx, ok := seen[dirPath]
		if !ok {
			d, oerr := open(dirPath)
			if oerr != nil {
				for _, opened := range dirs {
					oerr = errors.Join(oerr, opened.close())
				}
				return nil, nil, oerr
			}
			idx = len(dirs)
			dirs = append(dirs, d)
			seen[dirPath] = idx
		}
		dirOf[i] = idx
	}
	return dirs, dirOf, nil
}
