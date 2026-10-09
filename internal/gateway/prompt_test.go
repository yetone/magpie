package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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

// A Codex request, as Codex 0.1xx sends ChatGPT's backend: its tools come
// in additional_tools, its own functions one by one and any other
// namespace as one; AGENTS.md comes as a user message; the skills list as
// a developer one; and exec's JavaScript reading files with cat reads them.
func TestPromptOfCodex(t *testing.T) {
	p := fixturePrompt(t, "codex.json", provider.Responses)
	if it, ok := itemOf(p, PartMemory, "/home/dev/proj/AGENTS.md"); !ok || it.Tag != "project" {
		t.Fatalf("AGENTS.md %+v", partOf(p, PartMemory))
	}
	if it, ok := itemOf(p, PartMemory, "skills"); !ok || it.Tag != "skills" {
		t.Fatalf("skills %+v", partOf(p, PartMemory))
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
