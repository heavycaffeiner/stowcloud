// Command sigscan prints one line per exported func or method under a Go tree:
//
//	Name<TAB>paramCount<TAB>resultCount<TAB>path
//
// Used by the phase audit to compare a document's declared signature against
// the one the tree actually has. Counts rather than types, because a document
// writes types loosely (an elided struct body, a named import alias) while an
// arity change is unambiguous and is what silently breaks a caller.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fieldCount counts parameters or results, expanding grouped names: "a, b int"
// is two, not one.
func fieldCount(l *ast.FieldList) int {
	if l == nil {
		return 0
	}
	n := 0
	for _, f := range l.List {
		if len(f.Names) == 0 {
			n++
			continue
		}
		n += len(f.Names)
	}
	return n
}

func main() {
	root := os.Args[1]
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			fmt.Printf("%s\t%d\t%d\t%s\n",
				fn.Name.Name, fieldCount(fn.Type.Params), fieldCount(fn.Type.Results), path)
		}
		// An interface method is a declaration too, and a document that names
		// one is naming a real obligation.
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok || it.Methods == nil {
					continue
				}
				for _, m := range it.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok {
						continue
					}
					for _, n := range m.Names {
						if !n.IsExported() {
							continue
						}
						fmt.Printf("%s\t%d\t%d\t%s\n",
							n.Name, fieldCount(ft.Params), fieldCount(ft.Results), path)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
