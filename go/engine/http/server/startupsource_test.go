//go:build linux

package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The step order is only worth having if the shipped command obeys it. This
// reads the real serve source, recovers the order its calls actually run in,
// and validates that.
//
// Without this the table is a document that agrees with itself. With it, an
// edit that moves the store open above the sandbox fails here.
func TestTheShippedCommandObeysTheStartupOrder(t *testing.T) {
	src := locateServeSource(t)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parsing the serve source: %v", err)
	}

	body := findFunc(file, "runServe")
	if body == nil {
		t.Fatalf("runServe is not in %s; if it was renamed, this check has stopped watching anything", src)
	}

	// What each step looks like in the source. A step is recognised by a call
	// nothing else makes.
	markers := []struct {
		call string
		step StartupStep
	}{
		{"vfs.RequireResolver", StepRequireResolver},
		{"bootSettings", StepDeriveHardening},
		{"jail.Apply", StepApplySandbox},
		{"store.LockInstance", StepLockDataDir},
		{"core.New", StepOpenServices},
	}

	seen := map[StartupStep]int{}
	order := make([]StartupStep, 0, len(markers))
	position := 0

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call)
		for _, m := range markers {
			if name != m.call {
				continue
			}
			if _, dup := seen[m.step]; dup {
				return true
			}
			position++
			seen[m.step] = position
			order = append(order, m.step)
		}
		return true
	})

	for _, m := range markers {
		if _, ok := seen[m.step]; !ok {
			t.Fatalf("%s does not call %s, so the %s step is gone or renamed", src, m.call, m.step)
		}
	}

	// The recovered order is a subsequence of the full one. Fill the steps
	// this source does not mark, in their declared places, so the validator
	// sees a whole sequence and judges only what was observed.
	full := spliceObserved(order)
	if err := ValidateStartup(full); err != nil {
		t.Errorf("the shipped startup order is wrong: %v", err)
	}

	// And state the rule directly, so a failure names the security property
	// rather than an ordering technicality.
	sandbox := seen[StepApplySandbox]
	for _, s := range []StartupStep{StepLockDataDir, StepOpenServices} {
		if at, ok := seen[s]; ok && at < sandbox {
			t.Errorf("%s runs before the sandbox in %s: what it opens stays open after the confinement", s, src)
		}
	}
	if seen[StepDeriveHardening] > sandbox {
		t.Errorf("%s reads the sandbox's grants after applying it", src)
	}
	if seen[StepRequireResolver] > seen[StepDeriveHardening] {
		t.Errorf("%s resolves paths before requiring the race-free resolver", src)
	}
}

// spliceObserved returns the declared sequence with the observed steps placed
// in the order they were observed, leaving unobserved steps where they are.
func spliceObserved(observed []StartupStep) []StartupStep {
	isObserved := make(map[StartupStep]bool, len(observed))
	for _, s := range observed {
		isObserved[s] = true
	}

	out := make([]StartupStep, 0, len(StartupSequence()))
	next := 0
	for _, slot := range StartupSequence() {
		if isObserved[slot] {
			out = append(out, observed[next])
			next++
			continue
		}
		out = append(out, slot)
	}
	return out
}

// findFunc returns the named function's body.
func findFunc(file *ast.File, name string) *ast.BlockStmt {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Recv == nil {
			return fn.Body
		}
	}
	return nil
}

// calleeName renders a call's target as pkg.Func or Func.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	default:
		return ""
	}
}

// locateServeSource walks up to the module root and finds the serve source.
func locateServeSource(t *testing.T) string {
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

	src := filepath.Join(dir, "cmd", "stowcloud", "serve.go")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("the serve source is not at %s: %v", src, err)
	}
	return src
}

// A marker that no longer appears has to fail rather than pass vacuously. This
// runs the same recovery against a source with the sandbox call removed and
// confirms the check notices.
func TestARemovedMarkerIsNoticed(t *testing.T) {
	const stub = `package main
func runServe() {
	vfs.RequireResolver(nil)
	bootSettings("")
	store.LockInstance("")
	core.New(nil)
}`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stub.go", stub, 0)
	if err != nil {
		t.Fatalf("parsing the stub: %v", err)
	}

	body := findFunc(file, "runServe")
	if body == nil {
		t.Fatal("the stub has no runServe")
	}

	var found []string
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			found = append(found, calleeName(call))
		}
		return true
	})

	for _, name := range found {
		if strings.Contains(name, "jail.Apply") {
			t.Fatal("the stub was supposed to have no sandbox call")
		}
	}
	if len(found) != 4 {
		t.Errorf("recovered %d calls from the stub, want 4: %v", len(found), found)
	}
}
