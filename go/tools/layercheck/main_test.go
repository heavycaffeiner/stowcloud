package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFixture writes files under a synthetic engine/ tree and returns its
// path, so check() can read tier from the path the way it would in the real
// tree. files maps a path relative to engine/ to its source text.
func newFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "engine")
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// checkFixture runs check over a synthetic fixture and returns its output
// and violation count.
func checkFixture(t *testing.T, files map[string]string) (string, int) {
	t.Helper()
	root := newFixture(t, files)
	var buf bytes.Buffer
	n, err := check(root, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), n
}

const modRoot = "github.com/heavycaffeiner/stowcloud/go/engine/"

func assertRefused(t *testing.T, files map[string]string, wantSubstr string) {
	t.Helper()
	out, n := checkFixture(t, files)
	if n != 1 {
		t.Fatalf("got %d violations, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, wantSubstr) {
		t.Fatalf("output does not mention %q:\n%s", wantSubstr, out)
	}
}

func assertAllowed(t *testing.T, files map[string]string) {
	t.Helper()
	out, n := checkFixture(t, files)
	if n != 0 {
		t.Fatalf("got %d violations, want 0:\n%s", n, out)
	}
}

// Survey entry 1: core imports search. A service-layer sideways import that
// is not on the exception list (only search may import core, not the other
// way).
func TestSurveyCoreImportsSearchRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/core/scan.go": `package core

import "` + modRoot + `service/search"

var _ = search.Source{}
`,
	}, "not on the sideways exception list")
}

// Survey entry 2 (acl mixes evaluation and SQL) and entry 3 (auth mixes
// service, SQL and the smb sidecar write) are not import-graph facts: they
// describe what one package's source does internally, not which package it
// imports. Nothing here reproduces them; the fix is the phase 0.4 and auth
// phase package splits, not an import refusal.

// Survey entry 4: emergency imports httpapi/mw, settingscheck imports
// apierr. In the new tree both land as a service package importing the
// presentation tier.
func TestSurveyEmergencyImportsPresentationMiddlewareRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/settings/emergency.go": `package settings

import "` + modRoot + `http/middleware"

var _ = middleware.Chain{}
`,
	}, "tier service may not import tier http")
}

func TestSurveySettingscheckImportsApierrRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/settingscheck/check.go": `package settingscheck

import "` + modRoot + `http/apierr"

var _ = apierr.Status{}
`,
	}, "tier service may not import tier http")
}

// Survey entry 5: store/state imports store/cache. Called out explicitly in
// the survey and in the tier table's sideways list, which grants state only
// ident and dbfile.
func TestSurveyStoreStateImportsStoreCacheRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"store/state/upload.go": `package state

import "` + modRoot + `store/cache"

var _ = cache.Ident{}
`,
	}, "not on the sideways exception list")
}

// Survey entry 6 (vfs exports the control-file replace primitive) is not an
// import-graph fact either: it is about what one function in vfs does, not
// about an import. The fix is moving ReplaceFileDurable to store/fsatomic,
// which is a phase 0.2/0.3 package move, not something this gate can see.

func TestKitImportingAnythingInModuleRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"kit/num/num.go": `package num

import "` + modRoot + `infra/vfs"

var _ = vfs.ShareID(0)
`,
	}, "tier kit may not import tier infra")
}

func TestInfraImportingStoreRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"infra/jail/jail.go": `package jail

import "` + modRoot + `store/dbfile"

var _ = dbfile.Open
`,
	}, "tier infra may not import tier store")
}

func TestStoreImportingServiceRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"store/dbfile/dbfile.go": `package dbfile

import "` + modRoot + `service/acl"

var _ = acl.Evaluate
`,
	}, "tier store may not import tier service")
}

func TestStoreImportingInfraJailRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"store/cache/cache.go": `package cache

import "` + modRoot + `infra/jail"

var _ = jail.Sandbox{}
`,
	}, "store may import infra/vfs only")
}

func TestEngineImportingInternalRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"kit/num/num.go": `package num

import "github.com/heavycaffeiner/stowcloud/go/internal/task"

var _ = task.Go
`,
	}, "engine packages do not import internal")
}

func TestNetHTTPOutsideEngineHTTPRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/core/serve.go": `package core

import "net/http"

var _ = http.StatusOK
`,
	}, "only engine/http and the assembly may import net/http")
}

// The relying party is the one package below the presentation tier that may
// dial out, and the rule it is excepted from is about serving rather than
// about the import itself.
func TestNetHTTPInTheRelyingPartyAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"service/oidc/client.go": `package oidc

import "net/http"

var _ = http.MethodPost
`,
	})
}

// The exception is that one package and no other package in its tier.
func TestNetHTTPInAnotherServicePackageRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/upload/serve.go": `package upload

import "net/http"

var _ = http.StatusOK
`,
	}, "only engine/http and the assembly may import net/http")
}

func TestFiberOutsideEngineHTTPRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"service/core/serve.go": `package core

import "github.com/gofiber/fiber/v2"

var _ = fiber.New
`,
	}, "only engine/http and the assembly may import fiber")
}

func TestServiceCoreImportsServiceAclAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"service/core/resolve.go": `package core

import "` + modRoot + `service/acl"

var _ = acl.Evaluate
`,
	})
}

func TestServiceSearchImportsServiceCoreAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"service/search/walk.go": `package search

import "` + modRoot + `service/core"

var _ = core.Resolve
`,
	})
}

func TestStoreStateImportsIdentAndDbfileAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"store/state/state.go": `package state

import (
	"` + modRoot + `store/ident"
	"` + modRoot + `store/dbfile"
)

var (
	_ = ident.Tuple{}
	_ = dbfile.Open
)
`,
	})
}

func TestStoreImportsInfraVfsAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"store/cache/cache.go": `package cache

import "` + modRoot + `infra/vfs"

var _ = vfs.SharePath{}
`,
	})
}

func TestEveryTierImportsKitAllowed(t *testing.T) {
	for _, tier := range []string{"infra", "store", "service", "http"} {
		tier := tier
		t.Run(tier, func(t *testing.T) {
			assertAllowed(t, map[string]string{
				tier + "/pkg/pkg.go": `package pkg

import "` + modRoot + `kit/clock"

var _ = clock.Now
`,
			})
		})
	}
}

func TestStdlibAlwaysAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"kit/num/num.go": `package num

import (
	"errors"
	"fmt"
)

var _ = errors.New
var _ = fmt.Sprintf
`,
	})
}

func TestNetHTTPAndFiberInsideEngineHTTPAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"http/server/server.go": `package server

import (
	"net/http"
	"github.com/gofiber/fiber/v2"
)

var (
	_ = http.StatusOK
	_ = fiber.New
)
`,
	})
}

// TestRealEngineTreeIsClean points the gate at the tree it actually guards,
// the way vetgo's test asserts its real allowed package is clean. It is the
// one test in this file not built from a synthetic fixture.
func TestRealEngineTreeIsClean(t *testing.T) {
	var buf bytes.Buffer
	n, err := check(filepath.Join("..", "..", "engine"), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the real engine tree has %d layer violation(s):\n%s", n, buf.String())
	}
}
