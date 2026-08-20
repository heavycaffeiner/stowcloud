package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
)

// Signing in has to be reachable on the path the client asks for.
//
// It was not. Login was mounted on the change-password path, so the shipped
// interface could not sign in at all, and every test in this tree stayed green
// because each one tested a handler rather than the route that reaches it.
//
// These assertions are about the table itself, which is what was wrong. A test
// that drives the handler directly would have passed with the route missing,
// which is exactly what happened.

func TestSigningInIsMountedWhereTheClientAsks(t *testing.T) {
	table := routes(handler.Deps{}, nil)

	var found bool
	for _, rt := range table {
		if rt.Method == http.MethodPost && rt.Pattern == "/api/auth/login" {
			found = true
		}
	}
	if !found {
		t.Fatal("POST /api/auth/login is not in the route table, so nobody can sign in")
	}

	// And it is not on the change-password path, which is a different endpoint
	// the client also calls and which requires a credential.
	for _, rt := range table {
		if rt.Pattern == "/api/auth/password" && rt.Method == http.MethodPost {
			t.Error("login is mounted on the change-password path")
		}
	}
}

// Signing in needs no credential by definition, so the chain has to let it
// through. Changing a password does need one, so it must not be let through:
// the endpoint verifies the current password, and a public one would let
// anybody change anybody's.
func TestTheSignInPathIsPublicAndChangePasswordIsNot(t *testing.T) {
	for _, p := range []string{"/api/auth/login", "/api/auth/login/totp"} {
		if !mw.PublicPaths(http.MethodPost, p) {
			t.Errorf("%s needs a credential to reach, which is the credential it issues", p)
		}
	}
	if mw.PublicPaths(http.MethodPost, "/api/auth/password") {
		t.Error("changing a password is reachable with no credential")
	}
}

// Every route the table declares is one the client can actually name: no
// pattern carries a placeholder the client would send literally.
func TestNoRoutePatternIsMalformed(t *testing.T) {
	for _, rt := range routes(handler.Deps{}, nil) {
		if !strings.HasPrefix(rt.Pattern, "/") {
			t.Errorf("%s %s is not an absolute path", rt.Method, rt.Pattern)
		}
		if strings.Contains(rt.Pattern, "${") {
			t.Errorf("%s %s carries an uninterpolated hole", rt.Method, rt.Pattern)
		}
		if strings.Contains(rt.Pattern, "?") {
			t.Errorf("%s %s carries a query string, which is not part of a pattern", rt.Method, rt.Pattern)
		}
	}
}
