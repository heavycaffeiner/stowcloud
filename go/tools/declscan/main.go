// Command declscan prints every exported identifier declared under a Go tree,
// one per line. Used by the phase audit to compare the documents' declared
// signatures against what the engine actually has.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := os.Args[1]
	out := map[string]bool{}
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
			switch v := decl.(type) {
			case *ast.FuncDecl:
				if v.Name.IsExported() {
					out[v.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range v.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							out[s.Name.Name] = true
						}
						// Interface and struct members count as declared too:
						// a document naming a method declares it on a type,
						// and the type may satisfy it through an interface.
						if it, ok := s.Type.(*ast.InterfaceType); ok && it.Methods != nil {
							for _, m := range it.Methods.List {
								for _, n := range m.Names {
									if n.IsExported() {
										out[n.Name] = true
									}
								}
							}
						}
						if st, ok := s.Type.(*ast.StructType); ok && st.Fields != nil {
							for _, fld := range st.Fields.List {
								for _, n := range fld.Names {
									if n.IsExported() {
										out[n.Name] = true
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								out[n.Name] = true
							}
						}
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

	names := make([]string, 0, len(out))
	for n := range out {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
}
