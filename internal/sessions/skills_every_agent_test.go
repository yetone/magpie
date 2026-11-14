package sessions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// skillsCounted says, for every agent whose sessions magpie reads, how
// Sessions' Skills card learns of its skills: a test in this package that
// counts them from that agent's own transcripts, or an agent that reads
// with the same parser as one that has such a test. An agent left out
// here fails TestEveryAgentSaysItsSkills, so a new agent's skills are
// either counted or written down as not counted (MaxMooreMy on X:
// Droid's skills went uncounted, "感觉很多遗漏的").
var skillsCounted = map[string]struct {
	test   string // a test that counts this agent's skills
	parser string // or: reads with this agent's parser
	none   string // or: why its skills are not counted
}{
	"claude":         {test: "TestStatsToolsAndSkills"},
	"claude-desktop": {parser: "claude"},
	"qoder":          {parser: "claude"},
	"qoder-cn":       {parser: "claude"},
	"codex":          {test: "TestCodexSkillReads"},
	"pi":             {test: "TestPiSkillRead"},
	"raft":           {parser: "pi"},
	"cindy":          {parser: "pi"},
	"omp":            {parser: "pi"},
	"grok":           {test: "TestGrokToolsAndSkills"},
	"alma":           {test: "TestAlmaSessions"},
	"droid":          {test: "TestDroidToolsAndSkills"},

	"opencode":     {test: "TestOpenCodeToolsAndSkills"},
	"cline":        {test: "TestClineToolsAndSkills"},
	"dsh":          {test: "TestDshToolsAndSkills"},
	"zcode":        {none: "its sessions are OpenCode 1's tables, whose tool parts are not read; no real ZCode session with a skill to build a fixture from"},
	"kimi":         {none: "Kimi Code's wire tool.call is counted by name, but how it brings in a skill was not seen in a real session (only the retired Python kimi-cli 1.52 was run)"},
	"workbuddy":    {none: "its Skill function_call lines are not read; no real session with tool lines"},
	"codebuddy":    {none: "reads with WorkBuddy's parser, which counts no skills"},
	"workbuddy-ai": {none: "reads with WorkBuddy's parser, which counts no skills"},
	"hermes":       {none: "its skill_view tool is not read; hermes is not run in a sandbox (it rewrites the live launcher)"},
	"openmausbot":  {none: "a bot finds a skill with skills_list and reads its SKILL.md with its file tools, no tool of its own; the real sample has no such turn"},
	"cursor":       {none: "selected skills live in selectedContext; needs a signed-in Cursor for a real session"},
	"agy":          {none: "skill_lookup's keys are not known from a real session"},
	"antigravity":  {none: "its steps' payloads are encrypted at rest; only the usage in steps.metadata is read"},
	"minimax-code": {none: "calls only: no Sessions summaries"},
	"devin":        {none: "calls only (Usage): no Sessions summaries"},
	"gemini":       {none: "calls only: no Sessions summaries"},
}

// foundAgents reads the agent names Found() adds, from access.go's source,
// so an agent added there is in the list whether or not its folder exists.
func foundAgents(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "access.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Found" || fn.Recv != nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			if id, ok := c.Fun.(*ast.Ident); !ok || id.Name != "add" {
				return true
			}
			if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, _ := strconv.Unquote(lit.Value)
				out = append(out, s)
			}
			return true
		})
	}
	if len(out) < 20 {
		t.Fatalf("read only %d agents from Found(): %v", len(out), out)
	}
	return out
}

// testFuncs lists the Test functions in this package's _test files.
func testFuncs(t *testing.T) map[string]bool {
	t.Helper()
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, n := range names {
		src, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, n, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

func samePtr(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func sameParser(a, b sessionParser) bool {
	return samePtr(a.whole, b.whole) && samePtr(a.grow, b.grow) && samePtr(a.line, b.line) && a.claude == b.claude
}

func TestEveryAgentSaysItsSkills(t *testing.T) {
	tests := testFuncs(t)
	found := foundAgents(t)
	seen := map[string]bool{}
	for _, a := range found {
		seen[a] = true
		c, ok := skillsCounted[a]
		switch {
		case !ok:
			t.Errorf("%s has sessions but skillsCounted doesn't say whether its skills are counted: add a test that counts them from its real transcripts, or say why not", a)
		case c.test != "":
			if !tests[c.test] {
				t.Errorf("%s: no test %s in this package", a, c.test)
			}
		case c.parser != "":
			if p, ok := skillsCounted[c.parser]; !ok || p.test == "" {
				t.Errorf("%s reads with %s's parser, which has no skills test", a, c.parser)
			} else if !sameParser(parserFor(a), parserFor(c.parser)) {
				t.Errorf("%s doesn't read with %s's parser any more", a, c.parser)
			}
		case c.none == "":
			t.Errorf("%s: say how its skills are counted, or why not", a)
		}
	}
	for a := range skillsCounted {
		if !seen[a] {
			t.Errorf("skillsCounted names %s, which Found() doesn't add", a)
		}
	}
}
