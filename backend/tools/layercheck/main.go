// layercheck refuses the two internal imports that would tangle the tree:
// platform reaching into the product, and anything reaching into the wiring
// package that is only meant to be called from cmd.
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const internalPrefix = "github.com/heavycaffeiner/stowcloud/backend/internal/"

func main() {
	log.SetFlags(0)
	log.SetPrefix("layercheck: ")
	if len(os.Args) < 2 {
		log.Fatal("usage: layercheck <internal dir>...")
	}
	found := 0
	for _, root := range os.Args[1:] {
		n, err := check(root, os.Stdout)
		if err != nil {
			log.Fatal(err)
		}
		found += n
	}
	if found > 0 {
		log.Fatalf("%d import(s) crossing an internal boundary", found)
	}
}

// check walks root, which is an internal directory, and reports each refused
// import to out.
func check(root string, out io.Writer) (int, error) {
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		importer := filepath.ToSlash(filepath.Dir(rel))
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.HasPrefix(imported, internalPrefix) {
				continue
			}
			if reason := refuse(importer, strings.TrimPrefix(imported, internalPrefix)); reason != "" {
				if _, err := fmt.Fprintf(out, "%s: import %q refused: %s\n", fset.Position(spec.Path.Pos()), imported, reason); err != nil {
					return err
				}
				found++
			}
		}
		return nil
	})
	return found, err
}

// refuse names the rule an import from importer to imported breaks, both
// given relative to internal/, or returns "" when the import is allowed.
func refuse(importer, imported string) string {
	switch {
	case under(importer, "platform") && !under(imported, "platform"):
		return "platform imports nothing else under internal"
	case imported == "server" && importer != "server":
		return "only cmd imports the server package"
	}
	return ""
}

func under(pkg, dir string) bool {
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}
