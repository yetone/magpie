package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestLongRunningCommandsReap: `magpie serve` and `magpie web` are what a
// container runs as its PID 1 (the Dockerfile's CMD and the Docker docs'
// `web --addr 0.0.0.0:3430 --no-open`), and PID 1 is left every orphan to
// reap. `web` didn't call proc.KeepReaping, so an aistudio2api whose
// plugin host had ended stayed <defunct> under it, and the plugin waited
// on it for good (swang20201 on X).
func TestLongRunningCommandsReap(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "web_cli.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	reaps := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "KeepReaping" {
					if x, ok := s.X.(*ast.Ident); ok && x.Name == "proc" {
						reaps[fn.Name.Name] = true
					}
				}
			}
			return true
		})
	}
	for _, cmd := range []string{"serveCmd", "webCmd"} {
		if !reaps[cmd] {
			t.Errorf("%s doesn't call proc.KeepReaping: as a container's PID 1 it leaves its adopted orphans <defunct>", cmd)
		}
	}
}
