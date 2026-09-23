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
	"strconv"
	"strings"
)

const moduleRoot = "github.com/heavycaffeiner/stowcloud/go/"

const (
	internalPrefix  = moduleRoot + "internal/"
	ginPrefix       = "github.com/gin-gonic/gin"
	hanamiPrefix    = "github.com/heavycaffeiner/hanami"
	fxPrefix        = "go.uber.org/fx"
	netHTTP         = "net/http"
	durablefsPrefix = "github.com/stowcloud/durablefs"
)

var outboundHTTP = map[string]bool{
	"feature/oidc":              true,
	"platform/storage/objstore": true,
}

var tierAllowed = map[string]map[string]bool{
	"kit":       {},
	"feature":   {"platform": true, "kit": true},
	"platform":  {"kit": true},
	"transport": {"feature": true, "platform": true, "kit": true},
	"runtime":   {"transport": true, "platform": true, "kit": true},
	"bootstrap": {"feature": true, "platform": true, "kit": true},
	"app":       {"feature": true, "platform": true, "transport": true, "runtime": true, "kit": true},
}

func say(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	return err
}

func main() {
	if len(os.Args) < 2 {
		if err := say(os.Stderr, "usage: layercheck <dir>...\n"); err != nil {
			os.Exit(2)
		}
		os.Exit(64)
	}
	found := 0
	for _, root := range os.Args[1:] {
		n, err := check(root, os.Stdout)
		if err != nil {
			if writeErr := say(os.Stderr, "layercheck: %v\n", err); writeErr != nil {
				os.Exit(2)
			}
			os.Exit(2)
		}
		found += n
	}
	if found > 0 {
		if err := say(os.Stderr, "\nlayercheck: %d import(s) crossing an internal boundary.\n", found); err != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func check(root string, out io.Writer) (int, error) {
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		tier, sub, ok := tierFromFilePath(path)
		if !ok {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range f.Imports {
			n, reportErr := reportOne(out, fset, spec, tier, sub)
			found += n
			if reportErr != nil {
				return reportErr
			}
		}
		return nil
	})
	return found, err
}

func reportOne(out io.Writer, fset *token.FileSet, spec *ast.ImportSpec, importerTier, importerSub string) (int, error) {
	importPath, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return 0, nil
	}
	reason, refused := evaluate(importerTier, importerSub, importPath)
	if !refused {
		return 0, nil
	}
	if writeErr := say(out, "%s: import %q refused: %s\n", fset.Position(spec.Path.Pos()), importPath, reason); writeErr != nil {
		return 0, writeErr
	}
	return 1, nil
}

func evaluate(importerTier, importerSub, importPath string) (string, bool) {
	switch {
	case importPath == netHTTP:
		if importerTier == "app" || importerTier == "transport" || importerTier == "runtime" || outboundHTTP[importerSub] {
			return "", false
		}
		return "net/http is limited to transport/http, app, runtime, feature/oidc, and platform/storage/objstore", true
	case strings.HasPrefix(importPath, ginPrefix):
		if importerTier == "app" || importerTier == "transport" || importerTier == "runtime" {
			return "", false
		}
		return "Gin is limited to transport/http, app, and runtime", true
	case strings.HasPrefix(importPath, hanamiPrefix):
		if compositionPackage(importerTier) {
			return "", false
		}
		return "Hanami is limited to cmd, bootstrap/sandbox, app composition, and runtime", true
	case strings.HasPrefix(importPath, fxPrefix):
		if compositionPackage(importerTier) {
			return "", false
		}
		return "Fx is limited to cmd, bootstrap/sandbox, app composition, and runtime", true
	case strings.HasPrefix(importPath, durablefsPrefix):
		return "", false
	case strings.HasPrefix(importPath, internalPrefix):
		return evaluateInternal(importerTier, importerSub, importPath)
	default:
		return "", false
	}
}

func compositionPackage(tier string) bool {
	return tier == "app" || tier == "runtime" || tier == "bootstrap"
}

func evaluateInternal(importerTier, importerSub, importPath string) (string, bool) {
	importedTier, importedSub, ok := tierAndSub(strings.TrimPrefix(importPath, internalPrefix))
	if !ok {
		return "internal import has no package tier", true
	}
	if importedTier == importerTier {
		if importerTier == "app" && importerSub == importedSub {
			return "", false
		}
		return "", false
	}
	if tierAllowed[importerTier][importedTier] {
		return "", false
	}
	if _, known := tierAllowed[importerTier]; !known {
		return fmt.Sprintf("unknown internal tier %q", importerTier), true
	}
	return fmt.Sprintf("tier %s may not import tier %s", importerTier, importedTier), true
}

func tierFromFilePath(path string) (tier, sub string, ok bool) {
	rest, ok := afterInternal(filepath.ToSlash(path))
	if !ok {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	tier = parts[0]
	if len(parts) == 2 {
		return tier, tier, true
	}
	sub = tier + "/" + parts[1]
	if tier == "platform" && len(parts) >= 3 && parts[1] == "storage" && parts[2] == "objstore" {
		sub += "/" + parts[2]
	}
	return tier, sub, true
}

func afterInternal(slash string) (string, bool) {
	const marker = "internal/"
	if idx := strings.Index(slash, "/"+marker); idx >= 0 {
		return slash[idx+len("/"+marker):], true
	}
	if strings.HasPrefix(slash, marker) {
		return slash[len(marker):], true
	}
	return "", false
}

func tierAndSub(rest string) (tier, sub string, ok bool) {
	if rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	tier = parts[0]
	sub = tier
	if len(parts) >= 2 {
		sub += "/" + parts[1]
	}
	if tier == "platform" && len(parts) >= 3 && parts[1] == "storage" && parts[2] == "objstore" {
		sub += "/" + parts[2]
	}
	return tier, sub, true
}
