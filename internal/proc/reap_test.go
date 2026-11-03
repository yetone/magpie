package proc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryStartedCommandIsWaited: a command magpie starts is waited for,
// in the function that starts it: Wait is what reaps it on Unix, and
// one not waited for stays a <defunct> child of magpie until magpie quits
// (acezan on Discord: 57 magpie-bun after 47 hours). Process.Release
// doesn't reap on Unix, so it is no way out: a command magpie leaves
// running goes through StartReaped. The check is by name: in each
// function, every x.Start() needs an x.Wait() (or x handed to a waiter,
// as Output's Start is, or kept in a struct whose stop waits) beside it.
func TestEveryStartedCommandIsWaited(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), ".Start()") && !strings.Contains(string(b), ".Release()") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, b, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			name := "?"
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
			case *ast.GenDecl:
				// a var holding a func, as tests swap in a fake
				if vs, ok := d.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) > 0 {
					name = vs.Names[0].Name
				}
			}
			starts := map[string]token.Pos{}
			waited := map[string]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				// handed to what owns it from here: a struct's field (whose
				// stop waits), a waiter's argument (Output's Start,
				// StartReaped)
				switch n := n.(type) {
				case *ast.KeyValueExpr:
					waited[exprName(n.Value)] = true
				case *ast.AssignStmt:
					for _, r := range n.Rhs {
						waited[exprName(r)] = true
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv := exprName(sel.X)
				switch {
				case sel.Sel.Name == "Start" && len(call.Args) == 0 && isCmd(recv):
					if _, ok := starts[recv]; !ok {
						starts[recv] = call.Pos()
					}
				case sel.Sel.Name == "Wait" && len(call.Args) == 0:
					waited[recv] = true
				case sel.Sel.Name == "Release" && len(call.Args) == 0 && strings.HasSuffix(recv, ".Process"):
					t.Errorf("%s:%d: %s releases %s rather than waiting for it; use proc.StartReaped", rel, fset.Position(call.Pos()).Line, name, recv)
				}
				for _, a := range call.Args {
					// handed to a waiter: Output's Start, StartReaped, a
					// helper that owns the command from here
					waited[exprName(a)] = true
				}
				return true
			})
			for recv, pos := range starts {
				if !waited[recv] {
					p := fset.Position(pos)
					t.Errorf("%s:%d: %s starts %s and never waits for it", rel, p.Line, name, recv)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// exprName is an expression as written: a name, or a selector of names.
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return exprName(e.X)
	case *ast.UnaryExpr:
		return exprName(e.X)
	case *ast.ParenExpr:
		return exprName(e.X)
	}
	return ""
}

// isCmd says whether a receiver's name is a command's: Start is also a
// timer's, a server's and a span's, which this check isn't about.
func isCmd(recv string) bool {
	last := recv[strings.LastIndex(recv, ".")+1:]
	l := strings.ToLower(last)
	return l == "cmd" || strings.HasSuffix(l, "cmd") || l == "c" || l == "proc" || l == "command"
}
