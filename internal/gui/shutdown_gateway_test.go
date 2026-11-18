package gui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Wails may terminate without returning to Run (macOS). Its shutdown
// callback itself must drain the gateway, including a non-handover update.
// The Web shutdown integration test exercises the shared drain and resume;
// this guard covers the native callback even in a headless test build.
func TestDesktopShutdownDrainsGateway(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "OnShutdown" {
			return true
		}
		fn, ok := kv.Value.(*ast.FuncLit)
		if !ok {
			return true
		}
		for _, stmt := range fn.Body.List {
			if _, branch := stmt.(*ast.IfStmt); branch {
				break
			}
			e, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := e.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			name, ok := call.Fun.(*ast.Ident)
			if ok && name.Name == "stopServing" {
				found = true
				break
			}
		}
		return false
	})
	if !found {
		t.Fatal("desktop shutdown does not wait for the gateway to save Claude sessions")
	}
}
