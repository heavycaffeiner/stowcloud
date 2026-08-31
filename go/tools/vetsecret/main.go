// Command vetsecret rejects a secret reaching a formatting verb.
//
// The type redacts itself under String, GoString and MarshalJSON, which covers
// most verbs. What it cannot cover is the two ways round it: a verb that
// renders a struct's fields directly rather than through a method, and the
// Reveal accessor, whose result is a plain byte slice the type system has
// stopped tracking. Both are what this reports.
//
// It type-checks rather than greps, because "is this expression a secret" is a
// question about types and a name-based guess is exactly the kind of rule that
// passes while doing nothing. Type information comes from export data that the
// go command produces, so the tool has no dependency outside the standard
// library.
package main

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// secretPackage is the package whose type must not reach a formatting verb.
const secretPackage = "github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"

// secretType and accessor are the two things this looks for.
const (
	secretType = "Secret"
	accessor   = "Reveal"
)

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() {
	if len(os.Args) < 2 {
		say(os.Stderr, "usage: vetsecret <package>...\n")
		os.Exit(64)
	}
	findings, err := check(os.Args[1:])
	if err != nil {
		say(os.Stderr, "vetsecret: %v\n", err)
		os.Exit(2)
	}
	for _, f := range findings {
		say(os.Stdout, "%s\n", f)
	}
	if len(findings) > 0 {
		say(os.Stderr, "\nvetsecret: %d secret(s) reaching a formatting verb.\n"+
			"Format the redaction (s.String()) or do not format it at all.\n", len(findings))
		os.Exit(1)
	}
}

// pkg is one package to inspect.
type pkg struct {
	importPath string
	dir        string
	files      []string
}

func check(patterns []string) ([]string, error) {
	exports, err := exportData(patterns)
	if err != nil {
		return nil, err
	}
	targets, err := targets(patterns)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok || f == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})

	var findings []string
	for _, p := range targets {
		f, err := inspect(fset, imp, p)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	sort.Strings(findings)
	return findings, nil
}

func inspect(fset *token.FileSet, imp types.Importer, p pkg) ([]string, error) {
	var files []*ast.File
	for _, name := range p.files {
		f, err := parser.ParseFile(fset, filepath.Join(p.dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(p.importPath, fset, files, info); err != nil {
		return nil, fmt.Errorf("type-checking %s: %w", p.importPath, err)
	}

	var findings []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !formats(info, call) {
				return true
			}
			for _, arg := range call.Args {
				if why := offends(info, arg); why != "" {
					findings = append(findings,
						fmt.Sprintf("%s: %s", fset.Position(arg.Pos()), why))
				}
			}
			return true
		})
	}
	return findings, nil
}

// offends reports why an argument may not be formatted, or "" if it may. It
// looks at the whole expression, because a secret buried in a concatenation
// reaches the verb just the same.
func offends(info *types.Info, arg ast.Expr) string {
	if t := info.Types[arg].Type; t != nil && carriesSecret(t, map[types.Type]bool{}) {
		return "a secret reaches a formatting verb; format s.String() instead"
	}
	var why string
	ast.Inspect(arg, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != accessor {
			return true
		}
		if s := info.Selections[sel]; s != nil && isSecret(s.Recv()) {
			why = "a secret's bytes reach a formatting verb through " + accessor
			return false
		}
		return true
	})
	return why
}

// carriesSecret reports whether a value of type t holds secret bytes anywhere
// a formatting verb could reach them.
func carriesSecret(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	if isSecret(t) {
		return true
	}
	switch u := t.(type) {
	case *types.Pointer:
		return carriesSecret(u.Elem(), seen)
	case *types.Slice:
		return carriesSecret(u.Elem(), seen)
	case *types.Array:
		return carriesSecret(u.Elem(), seen)
	case *types.Map:
		return carriesSecret(u.Key(), seen) || carriesSecret(u.Elem(), seen)
	case *types.Chan:
		return carriesSecret(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if carriesSecret(u.Field(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Named:
		return carriesSecret(u.Underlying(), seen)
	}
	return false
}

// isSecret reports whether t is the secret type itself, through any number of
// pointers.
func isSecret(t types.Type) bool {
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Name() == secretType &&
		obj.Pkg() != nil && obj.Pkg().Path() == secretPackage
}

// formatters are the packages whose functions render a value for a human. A
// call into any of them is a formatting verb for this rule's purposes,
// including slog, whose handler formats what it is handed.
func formatters() map[string]bool {
	return map[string]bool{"fmt": true, "log": true, "log/slog": true}
}

// formats reports whether call renders its arguments. It covers both package
// functions (fmt.Printf) and methods on a logger (log.Logger.Printf), plus the
// panic builtin, whose value reaches the crash output.
func formats(info *types.Info, call *ast.CallExpr) bool {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name == "panic" && info.Uses[id] == nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if s := info.Selections[sel]; s != nil {
		return formatters()[packagePath(s.Recv())]
	}
	// Not a selection, so the receiver is a package name.
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	name, ok := info.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	return formatters()[name.Imported().Path()]
}

func packagePath(t types.Type) string {
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj() == nil || n.Obj().Pkg() == nil {
		return ""
	}
	return n.Obj().Pkg().Path()
}

// exportData asks the go command to compile the patterns and every dependency,
// and reports where it put each one's export data.
func exportData(patterns []string) (map[string]string, error) {
	args := append([]string{"list", "-export", "-deps",
		"-f", "{{.ImportPath}}\t{{.Export}}"}, patterns...)
	out, err := run(args)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) == 2 && parts[1] != "" {
			m[parts[0]] = parts[1]
		}
	}
	return m, nil
}

// targets reports the packages to inspect, with the source files to parse.
func targets(patterns []string) ([]pkg, error) {
	args := append([]string{"list",
		"-f", `{{.ImportPath}}	{{.Dir}}	{{join .GoFiles " "}}`}, patterns...)
	out, err := run(args)
	if err != nil {
		return nil, err
	}
	var ps []pkg
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		ps = append(ps, pkg{
			importPath: parts[0],
			dir:        parts[1],
			files:      strings.Fields(parts[2]),
		})
	}
	return ps, nil
}

func run(args []string) (string, error) {
	cmd := exec.Command("go", args...)
	// The shipping target, whatever host this runs on. A file behind a linux
	// build constraint is where the secrets end up, and a host-GOOS analysis
	// would not compile a line of it.
	cmd.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if e, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(e.Stderr))
		}
		return "", fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, stderr)
	}
	return string(out), nil
}
