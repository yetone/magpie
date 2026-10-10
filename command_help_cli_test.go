package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// After a command's name, --help, -h and help were its values or were
// ignored: magpie save --help saved a profile named --help, magpie provider
// key a -h made -h the provider's key, magpie gateway-key add help made a
// key, magpie rm p1 help deleted p1 and magpie model name a/m --help named
// the model --help. Wherever they come they now show the command's usage
// and write nothing.
func TestCommandHelpWritesNothing(t *testing.T) {
	cliHome(t)
	browserFails(t)
	was := tuiRun
	tuiRun = func(func()) error {
		t.Error("magpie tui --help started the TUI")
		return nil
	}
	t.Cleanup(func() { tuiRun = was })
	for _, args := range [][]string{{"save", "p1"}, {"group", "add", "g1", "models=a/m"}} {
		if _, err := printed(t, func() error { return run(args) }); err != nil {
			t.Fatalf("magpie %s: %v", strings.Join(args, " "), err)
		}
	}
	before := homeFiles(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"save", "--help"}, "magpie save <name>"},
		{[]string{"save", "p2", "-h"}, "magpie save <name>"},
		{[]string{"use", "p1", "help"}, "magpie use <name>"},
		{[]string{"rm", "p1", "--help"}, "magpie rm <name>"},
		{[]string{"rm", "Help"}, "magpie rm <name>"},
		{[]string{"model", "name", "a/m", "--help"}, "magpie model name"},
		{[]string{"model", "context", "a/m", "128k", "help"}, "magpie model"},
		{[]string{"model", "output", "a/m", "8k", "-h"}, "magpie model"},
		{[]string{"model", "wire", "a/m", "--help"}, "magpie model"},
		{[]string{"provider", "key", "a", "--help"}, "magpie provider key"},
		{[]string{"provider", "add", "x", "url=http://127.0.0.1:1/v1", "-h"}, "magpie provider add"},
		{[]string{"provider", "models", "a", "m2", "help"}, "magpie provider key|models"},
		{[]string{"search", "add", "tavily", "--help"}, "magpie search"},
		{[]string{"gateway-key", "add", "--help"}, "magpie gateway-key list|add"},
		{[]string{"gateway-key", "add", "help"}, "magpie gateway-key list|add"},
		{[]string{"group", "set", "g1", "routing=order", "--help"}, "magpie group"},
		{[]string{"library", "mcp", "add", "m2", "--help"}, "magpie library"},
		{[]string{"webdav", "off", "--help"}, "magpie webdav"},
		{[]string{"webdav", "dismiss", "-h"}, "magpie webdav"},
		{[]string{"s3", "off", "help"}, "magpie s3"},
		{[]string{"s3", "dismiss", "--help"}, "magpie s3"},
		{[]string{"tui", "--help"}, "magpie tui"},
		{[]string{"sessions", "--json", "-h"}, "magpie sessions"},
	} {
		out, err := printed(t, func() error { return run(c.args) })
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("magpie %s: %v\n%s\nwant usage with %q", strings.Join(c.args, " "), err, out, c.want)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Errorf("magpie %s wrote:\n%s", strings.Join(c.args, " "), diff)
			before = homeFiles(t)
		}
	}
	// what tells a write: an MCP server's own -h is its argument, and goes in
	if _, err := printed(t, func() error { return run([]string{"library", "mcp", "add", "m2", "echo", "-h"}) }); err != nil {
		t.Fatal(err)
	}
	if filesChanged(before, homeFiles(t)) == "" {
		t.Fatal("magpie library mcp add m2 echo -h wrote nothing")
	}
}

// Every command run() takes shows its help, so a command added later takes
// the help words as the others do: not as its values.
func TestEveryCommandHasHelp(t *testing.T) {
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
			s, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if ix, ok := s.Tag.(*ast.IndexExpr); !ok || identName(ix.X) != "args" {
				return true
			}
			for _, st := range s.Body.List {
				for _, e := range st.(*ast.CaseClause).List {
					if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
						v, _ := strconv.Unquote(l.Value)
						cmds = append(cmds, v)
					}
				}
			}
			return false
		})
	}
	if len(cmds) < 30 {
		t.Fatalf("found only %d commands in run(): %v", len(cmds), cmds)
	}
	for _, c := range cmds {
		switch c {
		case "-h", "--help", "help", "-v", "--version", "version", "-Embedding", "claude-mcp-helper":
			continue
		}
		if _, ok := commandHelp(c); !ok {
			t.Errorf("magpie %s --help has no help: it goes to the command as a value", c)
		}
	}
}

func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
