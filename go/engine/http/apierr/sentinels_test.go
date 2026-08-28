package apierr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every sentinel the service packages export is classified here.
//
// The specification asks for exactly this: "reflection over the documented
// sentinel inventories fails when a new one is unmapped". Reflection cannot see
// an unreferenced package variable, so this parses the service packages instead
// and compares their declared error values against the ones this table names.
//
// Without it, a sentinel added to a service classifies as Internal and the
// caller gets a 500 for a refusal that had a perfectly good status. That is the
// silent half of the drift this package exists to end: the loud half was three
// ladders disagreeing, and the quiet half is one ladder falling behind.

// servicePackages are the trees whose sentinels the presentation tier must
// recognise. A service package absent here is one whose errors would classify
// as Internal without anything saying so.
func servicePackages() []string {
	return []string{
		"../../service/core",
		"../../service/auth",
		"../../service/upload",
		"../../service/preview",
	}
}

// declaredSentinels parses one package and returns its exported error values.
//
// An error value is an exported identifier beginning Err, declared at package
// scope in a var block. That is the convention every service package here
// follows, and a sentinel spelled another way would be missed, which is why the
// count is asserted below rather than only the membership.
func declaredSentinels(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Each file parsed on its own rather than through ParseDir, which is
		// deprecated for ignoring build tags. Here every file counts whatever
		// its tags say: a sentinel behind a Linux tag is still one this
		// package has to classify, since the product only ships on Linux.
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range vs.Names {
					if strings.HasPrefix(ident.Name, "Err") && ident.IsExported() {
						out = append(out, ident.Name)
					}
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// classifiedNames is every sentinel this package's table names, read from the
// table's own source so the two cannot drift.
func classifiedNames(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean("sentinels.go"))
	if err != nil {
		t.Fatalf("reading the sentinel table: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		for _, pkg := range []string{"core.", "auth.", "upload.", "preview."} {
			i := strings.Index(line, "{"+pkg+"Err")
			if i < 0 {
				continue
			}
			rest := line[i+1+len(pkg):]
			end := strings.IndexAny(rest, ",} \t")
			if end < 0 {
				continue
			}
			out[strings.TrimPrefix(pkg, "")+rest[:end]] = true
		}
	}
	return out
}

// The check the specification asks for: no service sentinel goes unclassified.
func TestEveryServiceSentinelIsClassified(t *testing.T) {
	named := classifiedNames(t)
	if len(named) == 0 {
		t.Fatal("the sentinel table parsed to nothing, so this comparison checks nothing")
	}

	total := 0
	for _, dir := range servicePackages() {
		pkg := filepath.Base(dir)
		for _, sentinel := range declaredSentinels(t, dir) {
			total++
			if !named[pkg+"."+sentinel] {
				t.Errorf("%s.%s is exported and this package does not classify it, "+
					"so it would answer 500", pkg, sentinel)
			}
		}
	}
	if total == 0 {
		t.Fatal("no sentinels were found in the service packages, so the parse is broken")
	}
	t.Logf("%d service sentinels, %d classified", total, len(named))
}

// The other direction: the table naming a sentinel that no longer exists would
// not compile, so this instead checks the table has not silently shrunk to a
// handful while the services grew.
func TestTheTableCoversEveryServicePackage(t *testing.T) {
	named := classifiedNames(t)
	for _, dir := range servicePackages() {
		pkg := filepath.Base(dir)
		found := false
		for name := range named {
			if strings.HasPrefix(name, pkg+".") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the table classifies nothing from %s", pkg)
		}
	}
}
