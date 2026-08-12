// Command vetgo rejects a bare go statement anywhere but internal/task.
//
// The rule it enforces: a panic in a goroutine with no recover installed takes
// the process down and every request in flight with it. task.Go is the one
// spawn that installs one, so it is the one spawn there is.
//
// It parses rather than type-checks, because a go statement is syntax and
// needs nothing else. Give it directories; it walks them.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// spawnPackage is the one directory allowed to hold a go statement. Matched on
// the path so that a fixture under testdata exercises the same rule the tree
// is held to.
const spawnPackage = "internal/task"

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() {
	if len(os.Args) < 2 {
		say(os.Stderr, "usage: vetgo <dir>...\n")
		os.Exit(64)
	}
	found := 0
	for _, root := range os.Args[1:] {
		n, err := check(root, os.Stdout)
		if err != nil {
			say(os.Stderr, "vetgo: %v\n", err)
			os.Exit(2)
		}
		found += n
	}
	if found > 0 {
		say(os.Stderr, "\nvetgo: %d go statement(s) outside %s.\n"+
			"Use task.Go, which installs a recover; a bare go statement does not.\n",
			found, spawnPackage)
		os.Exit(1)
	}
}

func check(root string, out io.Writer) (int, error) {
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if allowed(path) {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			g, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			found++
			say(out, "%s: go statement outside %s\n",
				fset.Position(g.Go), spawnPackage)
			return true
		})
		return nil
	})
	return found, err
}

// allowed reports whether path sits in the one package that may spawn.
func allowed(path string) bool {
	dir := filepath.ToSlash(filepath.Dir(path))
	return strings.HasSuffix(dir, spawnPackage)
}
