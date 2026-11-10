package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
)

// helpHome points the home and every config, cache and data folder into
// one new folder, and gives it.
func helpHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for k, v := range map[string]string{
		"HOME": root, "USERPROFILE": root,
		"XDG_CONFIG_HOME": filepath.Join(root, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(root, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(root, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(root, ".local", "state"),
		"APPDATA":         filepath.Join(root, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(root, "AppData", "Local"),
		"SNOW_CONFIG_DIR": "",
		"DSH_HOME":        "",
	} {
		t.Setenv(k, v)
	}
	return root
}

// tree is every file and folder under root, with each file's bytes and
// time.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, _ := os.ReadFile(p)
		info, _ := d.Info()
		out[rel] = string(b) + "@" + info.ModTime().String()
		return nil
	})
	return out
}

// runOut runs magpie with args and gives what it printed; a run that
// doesn't come back in time (a gateway started, a window waited on) fails
// the test.
func runOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	read := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(r)
		out.Write(b)
		close(read)
	}()
	done := make(chan error, 1)
	go func() { done <- run(args) }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		os.Stdout = old
		t.Fatalf("magpie %s didn't come back: it ran", strings.Join(args, " "))
	}
	w.Close()
	os.Stdout = old
	<-read
	r.Close()
	return out.String(), err
}

// runCommands are the commands run's switch dispatches on, read from
// main.go, so a command added there is covered without a list to keep.
func runCommands(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if ix, ok := sw.Tag.(*ast.IndexExpr); !ok || types(ix) != "args[0]" {
				return true
			}
			for _, s := range sw.Body.List {
				for _, e := range s.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok {
						v, _ := strconv.Unquote(lit.Value)
						cmds = append(cmds, v)
					}
				}
			}
			return false
		})
	}
	if len(cmds) < 40 {
		t.Fatalf("read %d commands from run's switch: %v", len(cmds), cmds)
	}
	return append(cmds, "healthcheck", "heap", "cpu", "goroutines", "mtls", "https")
}

func types(ix *ast.IndexExpr) string {
	id, ok := ix.X.(*ast.Ident)
	lit, ok2 := ix.Index.(*ast.BasicLit)
	if !ok || !ok2 {
		return ""
	}
	return id.Name + "[" + lit.Value + "]"
}

// --help, -h or help after any command, and every agent, in any place,
// prints its usage and does nothing else: no file under the home is
// written and nothing is started. `magpie snow --help` set Snow's model to
// "--help" and took magpie out of it (#1457), and `magpie serve --help`
// ran a gateway.
func TestHelpOnEveryCommandDoesNothing(t *testing.T) {
	root := helpHome(t)
	cmds := runCommands(t)
	for _, a := range agent.All() {
		cmds = append(cmds, a.ID)
	}
	before := tree(t, root)
	for _, c := range cmds {
		t.Run(c, func(t *testing.T) {
			for _, args := range [][]string{
				{c, "--help"}, {c, "-h"}, {c, "help"},
				{c, "x", "--help"}, {c, "x", "y", "-h"}, {c, "--help", "x"},
			} {
				out, err := runOut(t, args...)
				if err != nil {
					t.Errorf("magpie %s: %v", strings.Join(args, " "), err)
				}
				if !strings.Contains(out, "magpie") && !strings.Contains(out, "usage") {
					t.Errorf("magpie %s printed no usage: %q", strings.Join(args, " "), out)
				}
				if got := tree(t, root); !reflect.DeepEqual(got, before) {
					t.Fatalf("magpie %s wrote under the home:\nbefore %v\nafter  %v", strings.Join(args, " "), before, got)
				}
			}
		})
	}
}

// The reporter's case (#1457, blackrion): Snow on magpie's profile, then
// `magpie snow --help` and its other forms. Each prints Snow's usage, a
// flag no agent form takes is refused, and Snow's files stay as they were.
func TestAgentHelpLeavesSnowWired(t *testing.T) {
	groupsHome(t)
	t.Setenv("SNOW_CONFIG_DIR", "")
	dir := filepath.Join(os.Getenv("HOME"), ".snow")
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o755)
	own := `{
  "snowcfg": {
    "baseUrl": "https://idealab.example.com/v1",
    "apiKey": "team-key",
    "requestMethod": "chat",
    "advancedModel": "glm-5",
    "basicModel": "glm-5"
  }
}`
	for f, s := range map[string]string{
		"profiles/default.json": own,
		"config.json":           own,
		"active-profile.json":   "{\n  \"activeProfile\": \"default\"\n}",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runOut(t, "snow", "a/m"); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	if _, ok := before[filepath.Join("profiles", "magpie.json")]; !ok {
		t.Fatalf("not wired: %v", before)
	}
	for _, args := range [][]string{
		{"snow", "--help"}, {"snow", "-h"}, {"snow", "help"}, {"snow", "model", "--help"}, {"snow-cli", "--help"},
	} {
		out, err := runOut(t, args...)
		if err != nil || !strings.Contains(out, "magpie snow <model>") {
			t.Errorf("magpie %s: %v %q", strings.Join(args, " "), err, out)
		}
		if got := tree(t, dir); !reflect.DeepEqual(got, before) {
			t.Fatalf("magpie %s changed Snow's files:\nbefore %v\nafter  %v", strings.Join(args, " "), before, got)
		}
	}
	for _, args := range [][]string{
		{"snow", "--verbose"}, {"snow", "model", "--x"}, {"snow", "-"}, {"snow", "effort", "--high"},
	} {
		_, err := runOut(t, args...)
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("magpie %s: %v", strings.Join(args, " "), err)
		}
		if got := tree(t, dir); !reflect.DeepEqual(got, before) {
			t.Fatalf("magpie %s changed Snow's files:\nbefore %v\nafter  %v", strings.Join(args, " "), before, got)
		}
	}
	// a model no profile of the user's is on, typed without a flag, is
	// refused by Snow itself the same way
	if _, err := runOut(t, "snow", "nonsense"); err == nil {
		t.Error("magpie snow nonsense taken")
	}
	if got := tree(t, dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("magpie snow nonsense changed Snow's files:\nbefore %v\nafter  %v", before, got)
	}
}
