package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "internal")
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

func checkFixture(t *testing.T, files map[string]string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	n, err := check(newFixture(t, files), &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), n
}

const modRoot = "github.com/heavycaffeiner/stowcloud/backend/internal/"

func assertRefused(t *testing.T, files map[string]string, want string) {
	t.Helper()
	out, n := checkFixture(t, files)
	if n != 1 {
		t.Fatalf("got %d violations, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("output does not mention %q:\n%s", want, out)
	}
}

func assertAllowed(t *testing.T, files map[string]string) {
	t.Helper()
	out, n := checkFixture(t, files)
	if n != 0 {
		t.Fatalf("got %d violations, want 0:\n%s", n, out)
	}
}

func TestFeatureCannotImportUpwardOrTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"app", modRoot + "app", "tier feature may not import tier app"},
		{"runtime", modRoot + "runtime/listener", "tier feature may not import tier runtime"},
		{"bootstrap", modRoot + "bootstrap/sandbox", "tier feature may not import tier bootstrap"},
		{"http", modRoot + "http/api", "tier feature may not import tier http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, map[string]string{
				"feature/files/files.go": `package files

import "` + tc.path + `"

var _ = X{}
`,
			}, tc.want)
		})
	}
}

func TestFeatureCannotImportFrameworks(t *testing.T) {
	for _, imp := range []struct{ path, reason string }{
		{ginPrefix, "Gin is limited"},
		{hanamiPrefix + "/bootstrap", "Hanami is limited"},
		{fxPrefix, "Fx is limited"},
	} {
		assertRefused(t, map[string]string{
			"feature/files/files.go": `package files

import "` + imp.path + `"
`,
		}, imp.reason)
	}
}

func TestFeatureOutboundHTTPAllowancesAreNarrow(t *testing.T) {
	assertAllowed(t, map[string]string{
		"feature/oidc/client.go": `package oidc
import "net/http"
var _ = http.MethodGet
`,
		"storage/objstore/client.go": `package objstore
import "net/http"
var _ = http.MethodGet
`,
	})
	assertRefused(t, map[string]string{
		"feature/files/files.go": `package files
import "net/http"
`,
	}, "net/http is limited")
}

func TestTierDirection(t *testing.T) {
	assertAllowed(t, map[string]string{
		"feature/files/files.go": `package files
import (
	"` + modRoot + `platform/clock"
	"` + modRoot + `storage/vfs"
	"` + modRoot + `store/state"
)
`,
		"http/api/handler.go": `package handler
import (
	"` + modRoot + `feature/files"
	"` + modRoot + `storage/vfs"
	"` + modRoot + `store/state"
)
`,
		"bootstrap/preflight/preflight.go": `package preflight
import "` + modRoot + `feature/files"
`,
		"app/engine.go": `package app
import (
	"` + modRoot + `feature/files"
	"` + modRoot + `http/server"
)
`,
	})
	assertRefused(t, map[string]string{
		"platform/clock/clock.go": `package clock
import "` + modRoot + `feature/files"
`,
	}, "tier platform may not import tier feature")
	assertRefused(t, map[string]string{
		"storage/vfs/vfs.go": `package vfs
import "` + modRoot + `feature/files"
`,
	}, "tier storage may not import tier feature")
	assertRefused(t, map[string]string{
		"store/state/state.go": `package state
import "` + modRoot + `http/api"
`,
	}, "tier store may not import tier http")
}

func TestFrameworkAllowances(t *testing.T) {
	for _, sub := range []string{"http/api", "app", "runtime/listener", "bootstrap/sandbox"} {
		assertAllowed(t, map[string]string{
			sub + "/x.go": `package x
import (
	"net/http"
	"github.com/gin-gonic/gin"
)
var _ = http.MethodGet
var _ = gin.New
`,
		})
	}
	assertRefused(t, map[string]string{
		"feature/files/handler.go": `package files
import "github.com/gin-gonic/gin"
`,
	}, "Gin is limited")
}

func TestHanamiAndFxCompositionAllowances(t *testing.T) {
	for _, sub := range []string{"bootstrap/sandbox", "app", "runtime/listener"} {
		assertAllowed(t, map[string]string{
			sub + "/x.go": `package x
import (
	"github.com/heavycaffeiner/hanami/security/linux"
	"go.uber.org/fx"
)
var _ = linux.Policy{}
var _ fx.Option
`,
		})
	}
	assertRefused(t, map[string]string{
		"http/api/x.go": `package x
import "github.com/heavycaffeiner/hanami/security/linux"
`,
	}, "Hanami is limited")
}

func TestStdlibAndUnknownExternalImportsAllowed(t *testing.T) {
	assertAllowed(t, map[string]string{
		"platform/clock/clock.go": `package clock
import (
	"errors"
	"fmt"
)
var _ = errors.New
var _ = fmt.Sprintf
`,
	})
}

func TestUnknownInternalTiersAreRefused(t *testing.T) {
	assertRefused(t, map[string]string{
		"feature/files/files.go": `package files
import "` + modRoot + `experimental/thing"
`,
	}, `unknown internal tier "experimental"`)
}

func TestRealInternalTreeIsClean(t *testing.T) {
	var buf bytes.Buffer
	n, err := check(filepath.Join("..", "..", "internal"), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the real internal tree has %d layer violation(s):\n%s", n, buf.String())
	}
}
