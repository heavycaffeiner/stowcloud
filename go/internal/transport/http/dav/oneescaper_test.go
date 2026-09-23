//go:build linux

package dav

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// There is one path escaper in the tree, and one XML text escaper.
//
// Two would be two answers to the same question, and the answers only have to
// differ once for a path that is safe on one mount to carry markup on the
// other. A comment saying so is not a check, so this reads the source.
//
// The rule is narrow on purpose: it looks for the standard library escapers
// being called anywhere outside the two functions that own them.
func TestOnlyOneEscaperOfEachKind(t *testing.T) {
	root := engineHTTPRoot(t)

	// Where each library call is allowed to appear, by file and function.
	allowed := map[string]string{
		"url.PathEscape":           "",
		"url.QueryEscape":          "",
		"(&url.URL{}).EscapedPath": "",
		"xml.EscapeText":           "escape|xmlEscape",
	}

	var problems []string

	werr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this build does not compile, such as a tagged sibling,
			// still parses; a real syntax error is worth reporting.
			problems = append(problems, path+": "+perr.Error())
			return nil
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := escaperName(call)
				owners, watched := allowed[name]
				if !watched {
					return true
				}
				if owners == "" {
					problems = append(problems, path+": "+fn.Name.Name+" calls "+name)
					return true
				}
				for _, owner := range strings.Split(owners, "|") {
					if fn.Name.Name == owner {
						return true
					}
				}
				problems = append(problems, path+": "+fn.Name.Name+" calls "+name)
				return true
			})
		}
		return nil
	})
	if werr != nil {
		t.Fatalf("walking %s: %v", root, werr)
	}

	for _, p := range problems {
		t.Errorf("a second escaper: %s", p)
	}
}

// escaperName renders a call target as pkg.Func.
func escaperName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); ok {
		return pkg.Name + "." + sel.Sel.Name
	}
	return sel.Sel.Name
}

// engineHTTPRoot locates the presentation tree.
func engineHTTPRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}

	root := filepath.Join(dir, "engine", "http")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("the presentation tree is not at %s: %v", root, err)
	}
	return root
}

// The check has to find a second escaper when there is one. Run against a stub
// that calls the library directly outside an owning function.
func TestTheEscaperCheckNoticesASecondOne(t *testing.T) {
	const stub = `package x

func renderHref(s string) string {
	return url.PathEscape(s)
}`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stub.go", stub, 0)
	if err != nil {
		t.Fatalf("parsing the stub: %v", err)
	}

	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && escaperName(call) == "url.PathEscape" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Error("the recogniser missed a direct library escaper call")
	}
}
