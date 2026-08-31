// Command freshscan rejects comment lines in the rebuilt engine that were
// carried over from the old tree.
//
// The rebuild reads go/internal as a description of behavior and writes
// everything new, comments included. That rule is easy to keep for code, where
// a different design forces different statements, and easy to break for prose,
// where the old explanation is already correct and moving it costs nothing to
// type. It had been broken 2596 times before this program existed.
//
// Provenance is the smaller half of why it matters. A carried comment describes
// the system it was written for, and the rebuild changes things: the preview
// decoder arrived in the engine still claiming a 512 MiB address-space limit
// that measurement had already replaced with 2 GiB. The sentence survived
// because it was moved rather than written, and it documented a system that no
// longer existed.
//
// Matching is exact on the comment text after the marker and surrounding space
// are removed. Lines shorter than the threshold are ignored, because short
// fragments recur between any two files by an author's habit rather than by
// copying.
package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// minLen is the shortest comment worth comparing. A line under it, such as
// "// The store." or "// Never nil.", is ordinary phrasing rather than carried
// prose, and flagging those would bury the real findings.
const minLen = 30

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() {
	if len(os.Args) != 3 {
		say(os.Stderr, "usage: freshscan <old-dir> <new-dir>\n")
		os.Exit(64)
	}
	oldDir, newDir := os.Args[1], os.Args[2]

	old, err := collect(oldDir)
	if err != nil {
		say(os.Stderr, "freshscan: %v\n", err)
		os.Exit(70)
	}
	if len(old) == 0 {
		// An empty reference set would make every check pass, which is the one
		// failure a gate must not have. A missing old tree is a broken
		// invocation rather than a clean result.
		say(os.Stderr, "freshscan: no comments found under %s; refusing to pass vacuously\n", oldDir)
		os.Exit(70)
	}

	found, err := scan(newDir, old, os.Stdout)
	if err != nil {
		say(os.Stderr, "freshscan: %v\n", err)
		os.Exit(70)
	}
	if found > 0 {
		say(os.Stderr, "\nfreshscan: %d comment line(s) carried over from %s.\n", found, oldDir)
		say(os.Stderr, "The rebuild writes its own prose. Restate the comment rather than moving it,\n")
		say(os.Stderr, "and check it still describes what the new code actually does.\n")
		os.Exit(1)
	}
}

// commentText is the comment body on a line, and false when the line does not
// carry one worth comparing.
//
// Only line comments are read. A block comment is rare in this tree and its
// interior lines do not start with a marker, so treating them would mean
// tracking state across lines for no finding the line form does not already
// produce.
func commentText(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "//") {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimPrefix(t, "//"))
	if len(body) < minLen {
		return "", false
	}
	return body, true
}

// eachComment calls fn for every comparable comment line in a Go file.
func eachComment(path string, fn func(lineNo int, body string)) error {
	f, err := os.Open(path) //nolint:gosec // G304: a path this program walked, never a request's.
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only; the close cannot lose a write.

	sc := bufio.NewScanner(f)
	// A generated file can carry one very long line, and the default limit
	// would stop the scan partway and report a clean file.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for n := 1; sc.Scan(); n++ {
		if body, ok := commentText(sc.Text()); ok {
			fn(n, body)
		}
	}
	return sc.Err()
}

// collect reads every comparable comment under root into a set.
func collect(root string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		return eachComment(path, func(_ int, body string) {
			out[body] = struct{}{}
		})
	})
	return out, err
}

// finding is one carried line, held so the report can be sorted rather than
// emitted in directory-walk order.
type finding struct {
	path string
	line int
	body string
}

// scan reports every comment under root that also appears in old.
func scan(root string, old map[string]struct{}, w io.Writer) (int, error) {
	var found []finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		return eachComment(path, func(n int, body string) {
			if _, carried := old[body]; carried {
				found = append(found, finding{path: path, line: n, body: body})
			}
		})
	})
	if err != nil {
		return 0, err
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].path != found[j].path {
			return found[i].path < found[j].path
		}
		return found[i].line < found[j].line
	})
	for _, f := range found {
		say(w, "%s:%d: carried comment: %s\n", f.path, f.line, f.body)
	}
	return len(found), nil
}
