// Command layercheck rejects an import that crosses a layer boundary inside
// engine/.
//
// A package's tier is the first path element under engine/, so the rule is a
// path prefix check rather than a lookup table: kit imports stdlib only,
// infra imports kit, store imports kit and infra/vfs, service imports infra,
// store and kit, http imports service and kit. Sideways imports inside a
// tier are refused unless they sit on the one explicit exception list this
// tool carries. engine never imports internal, and only engine/http may
// import net/http or fiber.
//
// It parses import declarations rather than resolving packages, because the
// rule is about which path a file names, not what that path resolves to; a
// build-tagged file is read the same as any other, since a layer rule holds
// regardless of GOOS.
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

// moduleRoot is the import path prefix every package in this module shares.
// An import without this prefix is a third-party module or the standard
// library, which deps.allow gates instead of this tool.
const moduleRoot = "github.com/heavycaffeiner/stowcloud/go/"

const (
	enginePrefix   = moduleRoot + "engine/"
	internalPrefix = moduleRoot + "internal/"
	// fiberPrefix matches go-fiber's own import path. Fiber is not a
	// dependency yet (it arrives in phase 3), so this rule has no real
	// importer to catch today; it exists so the rule is enforced and
	// tested before the day it matters.
	fiberPrefix = "github.com/gofiber/fiber"
	netHTTP     = "net/http"
	httpTier    = "http"
)

// outboundHTTP is the one package outside the presentation tier that may
// import net/http, and only because what it makes is an outbound client.
//
// The rule net/http is under is about serving: a package below the
// presentation tier that can write a response is a package that can answer a
// request from a layer that has no business knowing there is one. The single
// sign-on relying party never serves; it dials an identity provider through a
// guarded transport, and the alternative is a hand-rolled client speaking
// TLS and HTTP to an untrusted peer, which is a worse trade than this line.
const outboundHTTP = "service/oidc"

// tierAllowed lists, for each tier, the other tiers it may import. A tier
// missing from this map, or a target tier missing from its list, is refused.
// Same-tier imports are never looked up here: they go through sideways
// instead, since "downward in the same tier" is not the same question as
// "downward a tier".
var tierAllowed = map[string]map[string]bool{
	"kit":     {},
	"infra":   {"kit": true},
	"store":   {"kit": true},
	"service": {"infra": true, "store": true, "kit": true},
	"http":    {"service": true, "kit": true},
}

// sideways lists the one explicit exception for each tier: which package in
// the same tier it may import. Absence from this map, or from a listed
// package's set, refuses the import.
var sideways = map[string]map[string]bool{
	"service/core":   {"service/acl": true},
	"service/search": {"service/core": true},
	// The upload engine sits on the core: it resolves a destination, takes
	// the permission check from that resolution, and publishes through the
	// core's own publish path rather than renaming into a share itself.
	"service/upload": {"service/core": true, "service/acl": true},
	"store/state":    {"store/ident": true, "store/dbfile": true},
	"store/cache":    {"store/ident": true, "store/dbfile": true},
	"store/journal":  {"store/ident": true, "store/dbfile": true},
}

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() {
	if len(os.Args) < 2 {
		say(os.Stderr, "usage: layercheck <dir>...\n")
		os.Exit(64)
	}
	found := 0
	for _, root := range os.Args[1:] {
		n, err := check(root, os.Stdout)
		if err != nil {
			say(os.Stderr, "layercheck: %v\n", err)
			os.Exit(2)
		}
		found += n
	}
	if found > 0 {
		say(os.Stderr, "\nlayercheck: %d import(s) crossing a layer boundary.\n"+
			"Each line above names the importer and why the target is refused; move the logic, not the import.\n",
			found)
		os.Exit(1)
	}
}

// check walks root and reports every refused import found under it. A file
// outside engine/ is skipped rather than refused, since the gate has nothing
// to say about a tree it was not asked to cover.
func check(root string, out io.Writer) (int, error) {
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		importerTier, importerSub, ok := tierFromFilePath(path)
		if !ok {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			found += reportOne(out, fset, spec, importerTier, importerSub)
		}
		return nil
	})
	return found, err
}

// reportOne evaluates one import spec and writes a line if it is refused.
func reportOne(out io.Writer, fset *token.FileSet, spec *ast.ImportSpec, importerTier, importerSub string) int {
	importPath, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return 0
	}
	reason, refused := evaluate(importerTier, importerSub, importPath)
	if !refused {
		return 0
	}
	say(out, "%s: import %q refused: %s\n", fset.Position(spec.Path.Pos()), importPath, reason)
	return 1
}

// evaluate reports whether importPath is refused for a file whose tier and
// tier-qualified package are importerTier and importerSub, and why.
func evaluate(importerTier, importerSub, importPath string) (reason string, refused bool) {
	switch {
	case strings.HasPrefix(importPath, internalPrefix):
		return "engine packages do not import internal; the two trees cross only in cmd or a test file", true

	case importPath == netHTTP:
		if importerTier != httpTier && importerSub != outboundHTTP {
			return "only engine/http may import net/http", true
		}
		return "", false

	case strings.HasPrefix(importPath, fiberPrefix):
		if importerTier != httpTier {
			return "only engine/http may import fiber", true
		}
		return "", false

	case strings.HasPrefix(importPath, enginePrefix):
		return evaluateEngine(importerTier, importerSub, importPath)

	default:
		// Standard library or a third-party module: not a tier import,
		// and deps.allow is the gate for third-party modules.
		return "", false
	}
}

// evaluateEngine applies the tier table and the sideways exception list to an
// import that names another engine package.
func evaluateEngine(importerTier, importerSub, importPath string) (string, bool) {
	importedTier, importedSub, ok := tierAndSub(strings.TrimPrefix(importPath, enginePrefix))
	if !ok {
		return "", false
	}

	if importedTier == importerTier {
		if importerSub == importedSub {
			return "", false
		}
		if sideways[importerSub][importedSub] {
			return "", false
		}
		return fmt.Sprintf("%s may not import %s: not on the sideways exception list", importerSub, importedSub), true
	}

	// store may reach infra for its type vocabulary only, and only vfs:
	// the tier table's one narrow crossing that is not a plain tier grant.
	if importerTier == "store" && importedTier == "infra" {
		if importedSub == "infra/vfs" {
			return "", false
		}
		return fmt.Sprintf("store may import infra/vfs only, not %s", importedSub), true
	}

	if tierAllowed[importerTier][importedTier] {
		return "", false
	}
	return fmt.Sprintf("tier %s may not import tier %s", importerTier, importedTier), true
}

// tierFromFilePath reports the tier and tier-qualified package a source file
// belongs to, read from its position under an engine/ directory anywhere in
// its path. It reports ok false for a file outside engine/.
func tierFromFilePath(path string) (tier, sub string, ok bool) {
	slash := filepath.ToSlash(path)
	rest, ok := afterEngine(slash)
	if !ok {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		// Just the tier directory itself with no file segment left, or a
		// bare file directly under engine/: not a real package location.
		return "", "", false
	}
	tier = parts[0]
	if len(parts) >= 3 {
		sub = tier + "/" + parts[1]
	} else {
		sub = tier
	}
	return tier, sub, true
}

// afterEngine returns the path segment following the last engine/ directory
// component, whether it opens the path or sits in the middle of it.
func afterEngine(slash string) (string, bool) {
	const marker = "engine/"
	if idx := strings.Index(slash, "/"+marker); idx >= 0 {
		return slash[idx+len("/"+marker):], true
	}
	if strings.HasPrefix(slash, marker) {
		return slash[len(marker):], true
	}
	return "", false
}

// tierAndSub splits the part of an import path following engine/ into its
// tier and tier-qualified package.
func tierAndSub(rest string) (tier, sub string, ok bool) {
	if rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	tier = parts[0]
	if len(parts) >= 2 {
		sub = tier + "/" + parts[1]
	} else {
		sub = tier
	}
	return tier, sub, true
}
