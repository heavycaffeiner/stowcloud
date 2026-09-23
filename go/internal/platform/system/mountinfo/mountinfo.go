//go:build linux

// Package mountinfo reads the process's own mount table.
package mountinfo

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// maxRows bounds how many lines Parse will read. /proc/self/mountinfo is
// normally a few dozen lines, but it is still kernel-controlled input to
// this process; a hostile or runaway mount namespace must not turn a read
// into unbounded work.
const maxRows = 10_000

// Mount is one row of the table.
type Mount struct {
	// Point is the path the filesystem is mounted at.
	Point string
	// FsType is the filesystem's name as the kernel spells it ("xfs").
	FsType string
}

// Parse reads mountinfo rows from r.
//
// A row that fails to parse is skipped, not fatal: one line mangled by a
// kernel quirk or a truncated read must not cost the caller every other
// mount. Parse only returns an error when the scanner itself fails.
func Parse(r io.Reader) ([]Mount, error) {
	var mounts []Mount
	scanner := bufio.NewScanner(r)
	for rows := 0; rows < maxRows && scanner.Scan(); rows++ {
		m, ok := parseLine(scanner.Text())
		if ok {
			mounts = append(mounts, m)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("mountinfo: read: %w", err)
	}
	return mounts, nil
}

// parseLine parses one row per Documentation/filesystems/proc.rst:
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
//
// Fields 1-6 are fixed, then zero or more optional "tag:value" fields, then
// a literal "-" separator, then fstype, source and super options. The
// optional fields vary in count per row, so the separator must be found by
// scanning rather than assumed at a fixed index.
func parseLine(line string) (Mount, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return Mount{}, false
	}
	point := fields[4]

	sepIdx := -1
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 || sepIdx+2 >= len(fields) {
		return Mount{}, false
	}

	decoded, ok := unescapeOctal(point)
	if !ok {
		return Mount{}, false
	}

	return Mount{Point: decoded, FsType: fields[sepIdx+1]}, true
}

// unescapeOctal decodes the \NNN octal escapes proc uses for space, tab,
// newline and backslash in path fields.
func unescapeOctal(s string) (string, bool) {
	if !strings.Contains(s, `\`) {
		return s, true
	}
	// A byte slice rather than strings.Builder: appending has no error to
	// discard, so the decode reads as what it is.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		if i+3 >= len(s) {
			return "", false
		}
		n, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", false
		}
		out = append(out, byte(n))
		i += 3
	}
	return string(out), true
}

// Self reads /proc/self/mountinfo.
func Self() ([]Mount, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("mountinfo: open: %w", err)
	}
	mounts, err := Parse(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return mounts, nil
}
