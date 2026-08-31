//go:build linux

package lifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The cookie is cleared only after the revoke has been decided.
//
// This cannot be established by driving the server: producing it needs a
// revoke that fails, and the auth service fails only when the database does.
// So the ordering is read out of the source instead, which is deterministic
// and catches the edit rather than a schedule.
//
// The property matters because the failure is silent from the outside. If the
// cookie is cleared first and the revoke then fails, the browser stops
// presenting a session that is still live: the person believes they signed
// out, and the token in anything that copied the cookie keeps working. Both
// orderings answer 204 to the client, so no response tells them apart.
func TestLogoutClearsTheCookieAfterTheRevoke(t *testing.T) {
	body := logoutBody(t)

	clearAt, revokeAt := -1, -1
	position := 0

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		position++
		switch calleeOf(call) {
		case "clearSessionCookie":
			// The last one, not the first: the early return for a cookie this
			// server never issued clears before any revoke is attempted, and
			// that path has no revoke to be ordered against.
			clearAt = position
		case "RevokeSession":
			if revokeAt < 0 {
				revokeAt = position
			}
		}
		return true
	})

	if revokeAt < 0 {
		t.Fatal("logout calls no RevokeSession, so it revokes nothing and only clears the cookie")
	}
	if clearAt < 0 {
		t.Fatal("logout clears no cookie, so a browser keeps presenting a revoked session")
	}
	if clearAt < revokeAt {
		t.Errorf("the cookie is cleared at call %d, before the revoke at %d: a failed revoke would leave a live session the browser has stopped presenting",
			clearAt, revokeAt)
	}
}

// The revoke's error is not discarded: covered behaviourally instead.
//
// A shape-matching version of this check lived here and was measured to be
// worthless. Rewriting the condition to `if false && errors.Is(err, ...)`
// swallows every failure and still presents an if whose condition mentions
// both the error and the sentinel, so the check passed on code that answered
// 204 for a session it had failed to revoke.
//
// What replaced it drives the real endpoint against closed databases, where
// the revoke returns "sql: database is closed", and requires the response not
// to be a success. That mutation is caught. The ordering check above stays
// because no response distinguishes the two orderings.

// logoutBody parses the handler out of the source next to this test.
func logoutBody(t *testing.T) *ast.BlockStmt {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing auth.go: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == "logout" && fn.Body != nil {
			return fn.Body
		}
	}
	t.Fatal("logout is not in auth.go; if it was renamed, this check has stopped watching anything")
	return nil
}

// calleeOf names what a call invokes, by its final identifier.
func calleeOf(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if fn.Sel != nil {
			return fn.Sel.Name
		}
	}
	return ""
}
