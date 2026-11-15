package testenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// EagerRegexps lists each package-level regexp.MustCompile (or Compile,
// MustCompilePOSIX) outside a function in the Go file at path, as
// file:line:col. Every package's init runs on every launch, so such a
// pattern is compiled for a path most launches never take; lazyre.New
// compiles on first use. A file that doesn't parse lists nothing: that is
// the compiler's to judge.
func EagerRegexps(path string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var bad []string
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
	return bad
}

// eagerHere is EagerRegexps over the package being tested (go test runs a
// package's tests in its own folder). The root package's
// TestNoEagerPackageRegexps checks every package, but a change runs its
// own packages, and that guard went red on main on six days (#6020,
// #6145, 4847a087f, 10e783080, 7869a454c, 022ffd09e): here it fails the
// run of the package that added the pattern, a -run of one test included.
func eagerHere() error {
	files, _ := filepath.Glob("*.go")
	var bad []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			bad = append(bad, EagerRegexps(f)...)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("a package-level regexp is compiled at launch; use lazyre.New:\n\t%s", strings.Join(bad, "\n\t"))
}
