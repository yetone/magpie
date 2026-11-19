package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func fixturePrompt(t *testing.T, name string, from provider.Protocol) *Prompt {
	t.Helper()
	b, err := os.ReadFile("testdata/context/" + name)
	if err != nil {
		t.Fatal(err)
	}
	p := promptOf(from, b)
	if p == nil {
		t.Fatalf("%s: no prompt", name)
	}
	return p
}

func partOf(p *Prompt, kind string) PromptPart {
	for _, x := range p.Parts {
		if x.Kind == kind {
			return x
		}
	}
	return PromptPart{}
}

func itemOf(p *Prompt, kind, name string) (PromptItem, bool) {
	for _, it := range partOf(p, kind).Items {
		if it.Name == name {
			return it, true
		}
	}
	return PromptItem{}, false
}

// A Claude Code request, as Claude Code 2.1 sends it (testdata, its text
// replaced with filler of the same length): the CLAUDE.md files in its
// first system-reminder are memory, whose they are told by how Claude Code
// describes them; an MCP server's tools are one item; the files Read read
// are files, by path; ls's output is Bash's; the harness's mid-conversation
// system messages are its own prompt.
func TestPromptOfClaudeCode(t *testing.T) {
	p := fixturePrompt(t, "claude-code.json", provider.Anthropic)
	var kinds []string
	for _, x := range p.Parts {
		kinds = append(kinds, x.Kind)
	}
	if !slices.Equal(kinds, []string{PartSystem, PartTools, PartMemory, PartFiles, PartResults, PartChat}) {
		t.Fatalf("parts %v", kinds)
	}
	if it, ok := itemOf(p, PartMemory, "/home/dev/.claude/CLAUDE.md"); !ok || it.Tag != "user" {
		t.Fatalf("user CLAUDE.md %+v", partOf(p, PartMemory))
	}
	if it, ok := itemOf(p, PartMemory, "/home/dev/proj/CLAUDE.md"); !ok || it.Tag != "project" {
		t.Fatalf("project CLAUDE.md %+v", partOf(p, PartMemory))
	}
	if _, ok := itemOf(p, PartMemory, "reminders"); !ok {
		t.Fatalf("the rest of the reminders %+v", partOf(p, PartMemory))
	}
	if it, ok := itemOf(p, PartTools, "linear"); !ok || it.Tag != "mcp" || it.N != 3 {
		t.Fatalf("linear's tools %+v", it)
	}
	if _, ok := itemOf(p, PartTools, "Bash"); !ok {
		t.Fatal("Bash's definition")
	}
	for _, f := range []string{"/home/dev/proj/main.go", "/home/dev/proj/reconcile.go"} {
		if it, ok := itemOf(p, PartFiles, f); !ok || it.Tag != "read" {
			t.Fatalf("%s: %+v", f, partOf(p, PartFiles))
		}
	}
	if it, ok := itemOf(p, PartResults, "Bash"); !ok || it.Tag != "result" {
		t.Fatalf("ls %+v", partOf(p, PartResults))
	}
	if _, ok := itemOf(p, PartSystem, "environment"); !ok {
		t.Fatalf("the # Environment system message %+v", partOf(p, PartSystem))
	}
	if p.Turns != 1 || p.Counted || p.Held {
		t.Fatalf("%+v", p)
	}
	sum := 0
	for _, x := range p.Parts {
		sum += x.Tokens
	}
	if sum != p.Tokens || p.Tokens < 10000 {
		t.Fatalf("tokens %d, parts %d", p.Tokens, sum)
	}
}

// Claude Code 2.1's skills, listed in its environment message as the
// agent sent it (testdata/workdir): they are skills, apart from the
// environment notes around them, and memory has none of them.
func TestPromptOfClaudeCodeSkills(t *testing.T) {
	p := fixturePrompt(t, "../workdir/claude-code-2.1.296.json", provider.Anthropic)
	sk, ok := itemOf(p, PartSkills, "skills")
	if !ok || sk.Tokens < 1000 {
		t.Fatalf("skills %+v", partOf(p, PartSkills))
	}
	env, ok := itemOf(p, PartSystem, "environment")
	if !ok || env.Tokens > 1000 {
		t.Fatalf("the environment notes still hold the skills %+v", env)
	}
	if _, ok := itemOf(p, PartMemory, "skills"); ok {
		t.Fatalf("the skills are memory too %+v", partOf(p, PartMemory))
	}
	var kinds []string
	for _, x := range p.Parts {
		kinds = append(kinds, x.Kind)
	}
	if !slices.Equal(kinds[:3], []string{PartSystem, PartTools, PartSkills}) {
		t.Fatalf("parts %v", kinds)
	}
}

// The list runs to the next blank line, and what follows it stays where
// it was.
func TestSplitSkillsEnds(t *testing.T) {
	b := newPromptBuilder()
	rest := b.splitSkills("Notes.\n\n"+skillsList+"\n\n- a: one\n  more of a\n- b: two\n\nToday's date is 2026-10-24.", "user")
	if rest != "Notes.\n\nToday's date is 2026-10-24." {
		t.Fatalf("rest %q", rest)
	}
	if it, ok := itemOf(b.prompt(), PartSkills, "skills"); !ok || it.Tokens != tokensOf(skillsList+"\n\n- a: one\n  more of a\n- b: two") {
		t.Fatalf("skills %+v", it)
	}
	if rest := b.splitSkills("no list here", "user"); rest != "no list here" {
		t.Fatalf("no list %q", rest)
	}
}

// A prompt kept when the skills were memory is read with them apart, and
// a memory part they made up alone goes.
func TestSkillsApartOfKeptPrompt(t *testing.T) {
	p := &Prompt{Tokens: 60, Parts: []PromptPart{
		{Kind: PartTools, Tokens: 10, Items: []PromptItem{{Name: "Bash", Tokens: 10}}},
		{Kind: PartMemory, Tokens: 30, Items: []PromptItem{{Name: "skills", Tag: "skills", Tokens: 20}, {Name: "/p/AGENTS.md", Tag: "project", Tokens: 10}}},
		{Kind: PartChat, Tokens: 20, Items: []PromptItem{{Name: "1", Tag: "turn", Tokens: 20}}},
	}}
	p.skillsApart()
	var got []string
	for _, x := range p.Parts {
		got = append(got, x.Kind+":"+strconv.Itoa(x.Tokens))
	}
	if !slices.Equal(got, []string{"tools:10", "skills:20", "memory:10", "chat:20"}) {
		t.Fatalf("parts %v", got)
	}
	if _, ok := itemOf(p, PartMemory, "skills"); ok {
		t.Fatal("still memory")
	}
	alone := &Prompt{Parts: []PromptPart{{Kind: PartMemory, Tokens: 20, Items: []PromptItem{{Name: "skills", Tag: "skills", Tokens: 20}}}}}
	alone.skillsApart()
	if len(alone.Parts) != 1 || alone.Parts[0].Kind != PartSkills {
		t.Fatalf("alone %+v", alone.Parts)
	}
	var none *Prompt
	none.skillsApart()
}

// A Codex request, as Codex 0.1xx sends ChatGPT's backend: its tools come
// in additional_tools, its own functions one by one and any other
// namespace as one; AGENTS.md comes as a user message; the skills list as
// a developer one; and exec's JavaScript reading files with cat reads them.
func TestPromptOfCodex(t *testing.T) {
	p := fixturePrompt(t, "codex.json", provider.Responses)
	if it, ok := itemOf(p, PartMemory, "/home/dev/proj/AGENTS.md"); !ok || it.Tag != "project" {
		t.Fatalf("AGENTS.md %+v", partOf(p, PartMemory))
	}
	if it, ok := itemOf(p, PartSkills, "skills"); !ok || it.Tag != "skills" {
		t.Fatalf("skills %+v", partOf(p, PartSkills))
	}
	if _, ok := itemOf(p, PartMemory, "skills"); ok {
		t.Fatalf("the skills are memory too %+v", partOf(p, PartMemory))
	}
	if it, ok := itemOf(p, PartTools, "collaboration"); !ok || it.Tag != "namespace" || it.N < 2 {
		t.Fatalf("collaboration %+v", partOf(p, PartTools))
	}
	if _, ok := itemOf(p, PartTools, "exec"); !ok {
		t.Fatalf("exec %+v", partOf(p, PartTools))
	}
	for _, f := range []string{"main.go", "reconcile.go"} {
		if it, ok := itemOf(p, PartFiles, f); !ok || it.Tag != "shell" {
			t.Fatalf("%s: %+v", f, partOf(p, PartFiles))
		}
	}
	if _, ok := itemOf(p, PartSystem, "environment_context"); !ok {
		t.Fatalf("environment %+v", partOf(p, PartSystem))
	}
	if _, ok := itemOf(p, PartSystem, "prompt"); !ok {
		t.Fatalf("Codex's own prompt %+v", partOf(p, PartSystem))
	}
	if p.Turns != 1 {
		t.Fatalf("turns %d", p.Turns)
	}
}

// Codex sends an MCP server's tools as a namespace named as its tools are,
// mcp__<server>__: that one is the server's, its own (clock…) aren't MCP.
func TestPromptOfCodexMCP(t *testing.T) {
	body := `{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[
		{"type":"namespace","name":"mcp__linear__","tools":[{"type":"function","name":"list_issues","description":"` + strings.Repeat("issues ", 200) + `"},{"type":"function","name":"get_issue","description":"one issue"}]},
		{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","description":"sleeps"}]}]}`
	p := promptOf(provider.Responses, []byte(body))
	if it, ok := itemOf(p, PartTools, "linear"); !ok || it.Tag != "mcp" || it.N != 2 {
		t.Fatalf("linear %+v", partOf(p, PartTools))
	}
	if it, ok := itemOf(p, PartTools, "clock"); !ok || it.Tag != "namespace" {
		t.Fatalf("clock %+v", partOf(p, PartTools))
	}
}

// Chat Completions: a tool's answer goes by the name of the call it
// answers, a file it read by the file; a user's message begins a turn.
func TestPromptOfChat(t *testing.T) {
	body := `{"model":"m","messages":[
	 {"role":"system","content":"You are OpenCode.\nInstructions from: /p/AGENTS.md\nbe brief, always"},
	 {"role":"user","content":"look at a.go"},
	 {"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"/p/a.go\"}"}},
	   {"id":"c2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"go test ./...\"}"}}]},
	 {"role":"tool","tool_call_id":"c1","content":"package a\nfunc A() {}"},
	 {"role":"tool","tool_call_id":"c2","content":"ok  a 0.1s"},
	 {"role":"user","content":[{"type":"text","text":"and this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],
	 "tools":[{"type":"function","function":{"name":"read","parameters":{}}},{"type":"function","function":{"name":"mcp__gh__issue","parameters":{}}}]}`
	p := promptOf(provider.Chat, []byte(body))
	if p == nil || p.Turns != 2 {
		t.Fatalf("%+v", p)
	}
	if it, ok := itemOf(p, PartMemory, "/p/AGENTS.md"); !ok || it.Tag != "project" {
		t.Fatalf("OpenCode's instructions %+v", partOf(p, PartMemory))
	}
	if _, ok := itemOf(p, PartFiles, "/p/a.go"); !ok {
		t.Fatalf("files %+v", partOf(p, PartFiles))
	}
	if it, ok := itemOf(p, PartFiles, "image"); !ok || it.Tokens != imageTokens {
		t.Fatalf("image %+v", it)
	}
	if _, ok := itemOf(p, PartResults, "bash"); !ok {
		t.Fatalf("results %+v", partOf(p, PartResults))
	}
	if it, ok := itemOf(p, PartTools, "gh"); !ok || it.Tag != "mcp" {
		t.Fatalf("tools %+v", partOf(p, PartTools))
	}
}

// Gemini matches a function's response to its call by name.
func TestPromptOfGemini(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"You are Gemini CLI."}]},
	 "contents":[{"role":"user","parts":[{"text":"read go.mod"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"go.mod"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"output":"module x"}}}]}],
	 "tools":[{"functionDeclarations":[{"name":"read_file","parameters":{}}]}]}`
	p := promptOf(provider.Gemini, []byte(body))
	if p == nil || p.Turns != 1 {
		t.Fatalf("%+v", p)
	}
	if _, ok := itemOf(p, PartFiles, "go.mod"); !ok {
		t.Fatalf("files %+v", p.Parts)
	}
}

// A Responses request that names a previous response holds its earlier
// turns at the vendor's.
func TestPromptHeld(t *testing.T) {
	p := promptOf(provider.Responses, []byte(`{"previous_response_id":"resp_1","input":[{"role":"user","content":"go on"}]}`))
	if p == nil || !p.Held || p.Turns != 1 {
		t.Fatalf("%+v", p)
	}
	if promptOf(provider.Anthropic, []byte(`not json`)) != nil {
		t.Fatal("a body that isn't one")
	}
}

// Calibrated, the parts add up to what the vendor counted, and the one the
// trace holds is left as it was.
func TestPromptCalibrated(t *testing.T) {
	p := fixturePrompt(t, "claude-code.json", provider.Anthropic)
	before, _ := json.Marshal(p)
	c := p.calibrated(31337, 200000)
	sum := 0
	for _, x := range c.Parts {
		sum += x.Tokens
	}
	if !c.Counted || c.Tokens != 31337 || sum != 31337 || c.Window != 200000 {
		t.Fatalf("%+v (sum %d)", c, sum)
	}
	if after, _ := json.Marshal(p); string(after) != string(before) {
		t.Fatal("calibrating changed the estimate")
	}
	if again := c.calibrated(5, 0); again.Tokens != 31337 || again.Window != 200000 {
		t.Fatalf("counted twice %+v", again)
	}
}

func TestFilesOfCommand(t *testing.T) {
	for cmd, want := range map[string][]string{
		"cat main.go reconcile.go":   {"main.go", "reconcile.go"},
		"sed -n 1,80p internal/x.go": {"internal/x.go"},
		"head -n 40 README.md":       {"README.md"},
		`bash -lc "nl -ba a.go"`:     {"a.go"},
		"cd /p && cat go.mod":        {"go.mod"},
		"cat a.go | grep x":          nil,
		"go test ./...":              nil,
		"ls":                         nil,
	} {
		if got := filesOfCommand(cmd); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", cmd, got, want)
		}
	}
}

// Through the gateway: the route says what its prompt held, scaled to
// what the vendor counted, and the window of the model that answered.
func TestRouteTellsItsPrompt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn","usage":{"input_tokens":1200,"cache_read_input_tokens":40000,"cache_creation_input_tokens":800,"output_tokens":9}}`)
	}))
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "cw", Name: "CW", Anthropic: srv.URL, Models: []string{"m1"}, Key: "k",
		Contexts: map[string]int{"m1": 200000}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile("testdata/context/claude-code.json")
	var q map[string]any
	json.Unmarshal(b, &q)
	q["model"], q["stream"] = "cw/m1", false
	b, _ = json.Marshal(q)
	s := New()
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(b))))
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) != 1 || rs[0].Status != 200 {
		t.Fatalf("%+v", rs)
	}
	p := rs[0].Prompt
	if p == nil || !p.Counted || p.Tokens != 42000 || p.Window != 200000 {
		t.Fatalf("prompt %+v", p)
	}
	if _, ok := itemOf(p, PartMemory, "/home/dev/proj/CLAUDE.md"); !ok {
		t.Fatalf("memory %+v", p.Parts)
	}
	if rs[0].Conv == "" || !strings.HasPrefix(rs[0].Conv, "magpie-") {
		t.Fatalf("conversation %q", rs[0].Conv)
	}
}

// Pi lists its skills in an <available_skills> block after a paragraph
// on loading them (pi-coding-agent 0.73.1's formatSkillsForPrompt, its
// bytes in testdata/context): the block and the paragraph are skills, on
// every protocol, and the system prompt keeps what is around them.
func TestPromptOfPiSkills(t *testing.T) {
	raw, err := os.ReadFile("testdata/context/pi-0.73.1-system.txt")
	if err != nil {
		t.Fatal(err)
	}
	sys := string(raw)
	at := strings.Index(sys, "The following skills provide specialized")
	end := strings.Index(sys, "</available_skills>") + len("</available_skills>")
	if at < 0 || end < at {
		t.Fatal("fixture has no skills block")
	}
	block := sys[at:end]

	b := newPromptBuilder()
	rest := b.splitSkills(sys, "system")
	if strings.Contains(rest, "<skill>") || strings.Contains(rest, "The following skills") {
		t.Fatalf("the system prompt keeps the skills: %q", rest[max(0, len(rest)-400):])
	}
	if !strings.HasSuffix(rest, "Run `go test ./...` before you commit.\n\nCurrent date: 2026-11-19\nCurrent working directory: /home/u/proj") ||
		!strings.HasPrefix(rest, "You are an expert coding assistant operating inside pi") {
		t.Fatalf("what is around the block moved: %q", rest[max(0, len(rest)-200):])
	}
	if it, ok := itemOf(b.prompt(), PartSkills, "skills"); !ok || it.Tokens != tokensOf(block) {
		t.Fatalf("skills %+v, want %d tokens", it, tokensOf(block))
	}

	str, _ := json.Marshal(sys)
	for _, c := range []struct {
		from provider.Protocol
		body string
	}{
		{provider.Chat, `{"model":"m","messages":[{"role":"system","content":` + string(str) + `},{"role":"user","content":"hi"}]}`},
		{provider.Anthropic, `{"model":"m","system":[{"type":"text","text":` + string(str) + `}],"messages":[{"role":"user","content":"hi"}]}`},
	} {
		p := promptOf(c.from, []byte(c.body))
		if p == nil {
			t.Fatalf("%s: no prompt", c.from)
		}
		sk, ok := itemOf(p, PartSkills, "skills")
		if !ok || sk.Tokens != tokensOf(block) {
			t.Fatalf("%s: skills %+v", c.from, partOf(p, PartSkills))
		}
		if s := partOf(p, PartSystem); s.Tokens <= 0 || s.Tokens > tokensOf(sys)-tokensOf(block)+2 {
			t.Fatalf("%s: system %d tokens, the whole prompt is %d and its skills %d", c.from, s.Tokens, tokensOf(sys), tokensOf(block))
		}
	}
}

// An <available_skills> named in prose, or a block left open, isn't
// taken for skills.
func TestSplitSkillsBlockOnlyWhole(t *testing.T) {
	for _, s := range []string{
		"Agents on the standard list skills in an <available_skills> block.",
		"Skills:\n\n<available_skills>\n  <skill>\n    <name>a</name>",
	} {
		b := newPromptBuilder()
		if rest := b.splitSkills(s, "system"); rest != s {
			t.Fatalf("%q became %q", s, rest)
		}
		if _, ok := itemOf(b.prompt(), PartSkills, "skills"); ok {
			t.Fatalf("%q: skills", s)
		}
	}
}

// Pi's instruction files, each under "## <path>" in its "# Project
// Context" (pi-coding-agent 0.73.1's buildSystemPrompt), are memory, and
// its skills and closing lines after the last file aren't part of it.
func TestPromptOfPiMemory(t *testing.T) {
	raw, err := os.ReadFile("testdata/context/pi-0.73.1-system.txt")
	if err != nil {
		t.Fatal(err)
	}
	str, _ := json.Marshal(string(raw))
	p := promptOf(provider.Chat, []byte(`{"model":"m","messages":[{"role":"system","content":`+string(str)+`},{"role":"user","content":"hi"}]}`))
	if p == nil {
		t.Fatal("no prompt")
	}
	for path, tag := range map[string]string{"/home/u/.pi/agent/AGENTS.md": "user", "/home/u/proj/AGENTS.md": "project"} {
		it, ok := itemOf(p, PartMemory, path)
		if !ok || it.Tag != tag {
			t.Fatalf("%s: %+v in %+v", path, it, partOf(p, PartMemory))
		}
	}
	file := "## /home/u/proj/AGENTS.md\n\n# Project notes\n\nRun `go test ./...` before you commit.\n\n\n\n"
	if it, _ := itemOf(p, PartMemory, "/home/u/proj/AGENTS.md"); it.Tokens != tokensOf(file) {
		t.Fatalf("the last file holds %d tokens, its text is %d", it.Tokens, tokensOf(file))
	}
	if _, ok := itemOf(p, PartSkills, "skills"); !ok {
		t.Fatalf("skills %+v", partOf(p, PartSkills))
	}
	b := newPromptBuilder()
	if rest := b.splitSkills(b.splitMemory(string(raw)), "system"); strings.Contains(rest, "AGENTS.md") || strings.Contains(rest, "Run `go test") ||
		!strings.HasSuffix(rest, "\nCurrent date: 2026-11-19\nCurrent working directory: /home/u/proj") || !strings.Contains(rest, piContext) {
		t.Fatalf("system prompt %q", rest[max(0, len(rest)-300):])
	}
	// without Pi's header, a "## /path/AGENTS.md" line is the prompt's own
	b = newPromptBuilder()
	if s := "Notes.\n\n## /p/AGENTS.md\n\nkeep this"; b.splitMemory(s) != s {
		t.Fatal("a heading outside Pi's context was taken")
	}
}
