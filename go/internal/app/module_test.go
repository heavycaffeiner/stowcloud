//go:build linux

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestModuleDefersEngineConstructionInsideFx(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var module *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "Module" {
			module = function
			break
		}
	}
	if module == nil {
		t.Fatal("Module function is missing")
	}
	var openInsideProvider bool
	ast.Inspect(module.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || selectorNameForModule(call.Fun) != "fx.Provide" {
			return true
		}
		for _, argument := range call.Args {
			ast.Inspect(argument, func(inner ast.Node) bool {
				innerCall, ok := inner.(*ast.CallExpr)
				if ok && selectorNameForModule(innerCall.Fun) == "Open" {
					openInsideProvider = true
				}
				return true
			})
		}
		return true
	})
	if !openInsideProvider {
		t.Fatal("Open is not deferred inside an Fx provider")
	}
}

func selectorNameForModule(expression ast.Expr) string {
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
