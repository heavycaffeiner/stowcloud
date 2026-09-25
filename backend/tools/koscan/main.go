// Command koscan rejects Korean text in Go source.
//
// The server never decides what language a reader wants: a refusal travels as
// a catalogue key plus its placeholders and the browser renders it. Korean
// reaching a wire field, a comment or a log line is the shape this removes.
//
// It reads unicode.Hangul rather than a regular-expression escape, which is
// the whole reason it exists as a program: the gate it replaces used grep -P
// with a \x{...} class that the development host's grep does not interpret, so
// the pattern matched nothing and the gate reported PASS whatever the tree
// contained. A RangeTable behaves identically on every host.
package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() {
	if len(os.Args) < 2 {
		say(os.Stderr, "usage: koscan <dir>...\n")
		os.Exit(64)
	}
	found := 0
	for _, root := range os.Args[1:] {
		n, err := scan(root, os.Stdout)
		if err != nil {
			say(os.Stderr, "koscan: %v\n", err)
			os.Exit(2)
		}
		found += n
	}
	if found > 0 {
		say(os.Stderr, "\nkoscan: Korean text on %d line(s).\n"+
			"Send a catalogue key, not a sentence; keep comments in English.\n", found)
		os.Exit(1)
	}
}

func scan(root string, out io.Writer) (int, error) {
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// testdata is where CJK belongs: folding tables and trigram
		// fixtures are data, not copy, and the gate this replaces carried
		// the same exemption as a path allowlist.
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		n, err := scanFile(path, out)
		found += n
		return err
	})
	return found, err
}

func scanFile(path string, out io.Writer) (int, error) {
	f, err := os.Open(path) //nolint:gosec // the path comes from the walk this tool was pointed at.
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck // closing a file opened for reading cannot lose data.

	found, line := 0, 0
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for s.Scan() {
		line++
		for _, r := range s.Text() {
			if unicode.Is(unicode.Hangul, r) {
				say(out, "%s:%d: Korean text: %s\n", path, line, strings.TrimSpace(s.Text()))
				found++
				break
			}
		}
	}
	return found, s.Err()
}
