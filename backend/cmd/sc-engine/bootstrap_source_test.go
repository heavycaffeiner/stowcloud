//go:build linux

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestHanamiBootstrapOwnsSecurityOwnershipAndDeferredEngineConstruction(t *testing.T) {
	path := filepath.Join("main.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := findTopLevelFunction(file, "run")
	if run == nil {
		t.Fatal("run function is missing")
	}

	var securityPosition, ownershipPosition int
	position := 0
	ast.Inspect(run.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		position++
		switch selectorName(call.Fun) {
		case "securitylinux.WithPolicy":
			securityPosition = position
		case "ownership.WithRequirement":
			ownershipPosition = position
		}
		return true
	})
	if securityPosition == 0 || ownershipPosition == 0 {
		t.Fatalf("security or ownership integration is missing: security=%d ownership=%d", securityPosition, ownershipPosition)
	}
	if securityPosition >= ownershipPosition {
		t.Fatal("ownership runs before process security and would leak its descriptor into the handoff")
	}

	modules := findModulesLiteral(run.Body)
	if modules == nil {
		t.Fatal("Hanami Spec.Modules is missing")
	}
	if !containsCall(modules, "app.Module") {
		t.Fatal("app.Module is not deferred inside Spec.Modules")
	}
	if containsCallBeforeModules(run.Body, "app.Module") {
		t.Fatal("app.Module can run before selected pre-composition integrations")
	}
}

func findTopLevelFunction(file *ast.File, name string) *ast.FuncDecl {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == name {
			return function
		}
	}
	return nil
}

func selectorName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		if prefix, ok := value.X.(*ast.Ident); ok {
			return prefix.Name + "." + value.Sel.Name
		}
		return value.Sel.Name
	default:
		return ""
	}
}

func findModulesLiteral(body *ast.BlockStmt) *ast.FuncLit {
	var result *ast.FuncLit
	ast.Inspect(body, func(node ast.Node) bool {
		keyValue, ok := node.(*ast.KeyValueExpr)
		if !ok || selectorName(keyValue.Key) != "Modules" {
			return true
		}
		function, functionOK := keyValue.Value.(*ast.FuncLit)
		if functionOK {
			result = function
		}
		return false
	})
	return result
}

func containsCall(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && selectorName(call.Fun) == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func containsCallBeforeModules(body *ast.BlockStmt, name string) bool {
	for _, statement := range body.List {
		found := false
		ast.Inspect(statement, func(node ast.Node) bool {
			if function, ok := node.(*ast.FuncLit); ok && containsCall(function, name) {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if ok && selectorName(call.Fun) == name {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
