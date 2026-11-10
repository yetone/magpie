package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lazyre"
)

// TestAllLazyPatternsCompile compiles every regexp the binary's packages
// compile on first use (internal/lazyre), so a bad one fails here and not
// on a user's machine.
func TestAllLazyPatternsCompile(t *testing.T) {
	if n := lazyre.MustAll(); n < 500 {
		t.Fatalf("only %d lazy patterns registered; the binary's packages hold more", n)
	}
}

// TestNoEagerPackageRegexps fails on a package-level regexp.MustCompile
// (or regexp.Compile) outside a function: every package's init runs on
// every launch, `magpie statusline` on each Claude Code message included,
// so such a pattern is compiled for a path most launches never take. Use
// lazyre.New, which compiles on first use.
func TestNoEagerPackageRegexps(t *testing.T) {
	var bad []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "site") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // not this test's to judge
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			ast.Inspect(gd, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				if se, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := se.X.(*ast.Ident); ok && id.Name == "regexp" && (se.Sel.Name == "MustCompile" || se.Sel.Name == "Compile" || se.Sel.Name == "MustCompilePOSIX") {
						bad = append(bad, fset.Position(se.Pos()).String())
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("%s: a package-level regexp is compiled at launch; use lazyre.New", b)
	}
}
