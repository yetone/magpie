package usage

import (
	"embed"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rowCodeRoots are where a session file's chunk of rows is made, written
// and read back; logCodeRoots where a log is read into blocks, kept and
// read back.
var (
	rowCodeRoots = []string{"sessionChunk", "sessionRowPricer", "diskHeader", "diskEntry", "toDisk", "onDisk", "readDiskEntry", "readDiskEntries", "diskEntryPath", "diskEntryArchive"}
	logCodeRoots = []string{"readLogSnapshot", "logDiskRead", "logDiskOf", "logDiskWrite", "logDiskSave"}
)

// A file's name is the source of every declaration its rows are made
// through, reached from the roots by what each name in them is declared
// as: a file left out would keep the rows of code that has changed, and
// one put in reads every session file again on an upgrade that changed
// nothing of them (#2278).
func TestRequestDiskNamesTheCodeOfItsRows(t *testing.T) {
	for _, c := range []struct {
		name  string
		roots []string
		src   embed.FS
	}{{"rowSource", rowCodeRoots, rowSource}, {"logSource", logCodeRoots, logSource}} {
		reached := rowCodeFiles(t, c.roots)
		embedded, err := fs.Glob(c.src, "*.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range reached {
			if !slices.Contains(embedded, f) {
				t.Errorf("%s: %s is reached but isn't in its go:embed", c.name, f)
			}
		}
		for _, f := range embedded {
			if slices.Contains(reached, f) {
				continue
			}
			if ok, err := build.Default.MatchFile(".", f); err == nil && ok {
				t.Errorf("%s: %s is in its go:embed but isn't reached", c.name, f)
			}
		}
		t.Logf("%s reached: %s", c.name, strings.Join(reached, " "))
	}
}

// rowCodeFiles is the files of this package's declarations reached from
// roots, type-checked for this computer's system.
func rowCodeFiles(t *testing.T, roots []string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}", ".").Output()
	if err != nil {
		t.Skip("go list -export:", err)
	}
	exports := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if path, file, ok := strings.Cut(line, "\t"); ok && file != "" {
			exports[path] = file
		}
	}
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if ok, err := build.Default.MatchFile(".", name); err != nil || !ok {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("github.com/yetone/magpie/internal/usage", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	declOf := map[types.Object]ast.Node{}
	// assigned is each package variable's assignments outside its own
	// declaration: a hook set in another file runs in the rows' code too
	assigned := map[types.Object][]ast.Node{}
	byName := map[string][]types.Object{} // methods, for calls through an interface
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				o := info.Defs[d.Name]
				declOf[o] = d
				if d.Recv != nil {
					byName[d.Name.Name] = append(byName[d.Name.Name], o)
				}
				if d.Body == nil {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if a, ok := n.(*ast.AssignStmt); ok {
						for _, l := range a.Lhs {
							if id, ok := l.(*ast.Ident); ok {
								if v, ok := info.Uses[id].(*types.Var); ok && v.Parent() == pkg.Scope() {
									assigned[v] = append(assigned[v], d)
								}
							}
						}
					}
					return true
				})
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						declOf[info.Defs[s.Name]] = s
					case *ast.ValueSpec:
						for _, n := range s.Names {
							declOf[info.Defs[n]] = s
						}
					}
				}
			}
		}
	}
	seen := map[types.Object]bool{}
	reached := map[string]bool{}
	var visit func(o types.Object)
	walk := func(n ast.Node) {
		reached[filepath.Base(fset.Position(n.Pos()).Filename)] = true
		ast.Inspect(n, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok {
				visit(info.Uses[id])
			}
			return true
		})
	}
	visit = func(o types.Object) {
		if o == nil || seen[o] {
			return
		}
		seen[o] = true
		if fn, ok := o.(*types.Func); ok {
			if recv := fn.Type().(*types.Signature).Recv(); recv != nil && types.IsInterface(recv.Type()) {
				for _, m := range byName[fn.Name()] {
					visit(m)
				}
			}
		}
		if o.Pkg() != pkg {
			return
		}
		if n, ok := declOf[o]; ok {
			walk(n)
		}
		for _, n := range assigned[o] {
			walk(n)
		}
		// a type's methods an interface outside may call: encoding,
		// printing and sorting
		if tn, ok := o.(*types.TypeName); ok {
			if nt, ok := tn.Type().(*types.Named); ok {
				for m := range nt.Methods() {
					switch m.Name() {
					case "MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText", "MarshalBinary", "UnmarshalBinary",
						"GobEncode", "GobDecode", "String", "Error", "Format", "Len", "Less", "Swap":
						visit(m)
					}
				}
			}
		}
	}
	for _, name := range roots {
		o := pkg.Scope().Lookup(name)
		if o == nil {
			t.Fatalf("no %s in the package: the roots name what the rows are made through", name)
		}
		visit(o)
	}
	var list []string
	for f := range reached {
		list = append(list, f)
	}
	slices.Sort(list)
	return list
}
