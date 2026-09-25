package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/codexcat"
)

func TestCodexSignAndBody(t *testing.T) {
	signIn(t)
	p, _ := find(All(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, []byte(`{"prompt_cache_key":"thread-1"}`)); err != nil {
		t.Fatal(err)
	}
	h := req.Header
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer h.") || h.Get("chatgpt-account-id") != "acct-1" ||
		h.Get("originator") != "codex_cli_rs" || !strings.HasPrefix(h.Get("User-Agent"), "codex_cli_rs/0.") ||
		h.Get("OpenAI-Beta") != "responses=experimental" || h.Get("session_id") != "thread-1" || h.Get("conversation_id") != "thread-1" {
		t.Fatalf("headers: %v", h)
	}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"temperature":0.1,"stream":false,"store":true,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if _, ok := m["max_output_tokens"]; ok || m["temperature"] != nil || m["stream"] != true || m["store"] != false ||
		m["tool_choice"] != "auto" || m["parallel_tool_calls"] != true {
		t.Fatalf("body: %s", out)
	}
	if r := m["reasoning"].(map[string]any); r["effort"] != "low" || r["summary"] != "auto" {
		t.Fatalf("reasoning: %s", out)
	}
	if inc, _ := m["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include: %s", out)
	}
	if in := m["input"].([]any)[0].(map[string]any); in["role"] != "user" || in["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("input: %s", out)
	}
}

// The client's instructions ride first in the input and Codex's take their
// place; input items lose their ids, stored references and system role, and
// a tool output without its call becomes a note.
func TestCodexBodyAsCodexSendsIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5.5","model_messages":{"instructions_template":"You are Codex, a coding agent.\nMore."}}]}`), 0o644)

	out := codexBody([]byte(`{"model":"gpt-5.5","instructions":"You are Pi.","reasoning":{"effort":"ultra"},"input":[
	  {"type":"message","role":"system","content":"sys"},
	  {"type":"item_reference","id":"msg_1"},
	  {"type":"message","id":"msg_2","role":"user","content":"hi"},
	  {"type":"function_call","id":"fc_1","call_id":"c1","name":"ls","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c1","output":"a"},
	  {"type":"function_call_output","call_id":"c9","name":"cat","output":"b"}]}`))
	var m struct {
		Instructions string           `json:"instructions"`
		Reasoning    map[string]any   `json:"reasoning"`
		Input        []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Instructions != "You are Codex, a coding agent.\nMore." || m.Reasoning["effort"] != "max" {
		t.Fatalf("%s", out)
	}
	var got []string
	for _, it := range m.Input {
		if _, ok := it["id"]; ok {
			t.Fatalf("id kept: %s", out)
		}
		s, _ := it["type"].(string)
		if r, _ := it["role"].(string); r != "" {
			s += ":" + r
		}
		got = append(got, s)
	}
	if strings.Join(got, ",") != "message:developer,message:developer,message:user,function_call,function_call_output,message:assistant" {
		t.Fatalf("input: %v\n%s", got, out)
	}
	if m.Input[0]["content"].([]any)[0].(map[string]any)["text"] != "You are Pi." ||
		!strings.Contains(m.Input[5]["content"].(string), "[Previous cat result; call_id=c9]: b") {
		t.Fatalf("%s", out)
	}

	// Codex's own instructions, from Codex itself, stay as they are
	out = codexBody([]byte(`{"model":"gpt-5.5","instructions":"You are Codex, a coding agent.\nPersonality.","input":[]}`))
	json.Unmarshal(out, &m)
	if m.Instructions != "You are Codex, a coding agent.\nPersonality." || len(m.Input) != 0 {
		t.Fatalf("%s", out)
	}
}

// A conversation whose client names none gets a key of its own, the same on
// every turn; a client's own key stays.
func TestCodexPromptCacheKey(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	key := func(body string) string {
		var m struct {
			Key string `json:"prompt_cache_key"`
		}
		json.Unmarshal(codexBody([]byte(body)), &m)
		return m.Key
	}
	first := key(`{"model":"gpt-5.5","instructions":"You are Pi.","input":[{"type":"message","role":"user","content":"hi"}]}`)
	later := key(`{"model":"gpt-5.5","instructions":"You are Pi.","input":[{"type":"message","role":"user","content":"hi"},
	  {"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"a"}]}`)
	other := key(`{"model":"gpt-5.5","instructions":"You are Pi.","input":[{"type":"message","role":"user","content":"bye"}]}`)
	if first == "" || first != later || first == other || len(first) != 36 {
		t.Fatalf("first %q, later %q, other %q", first, later, other)
	}
	if k := key(`{"model":"gpt-5.5","prompt_cache_key":"thread-1","input":"hi"}`); k != "thread-1" {
		t.Fatalf("own key: %q", k)
	}
	if k := key(`{"model":"gpt-5.5","input":[]}`); k != "" {
		t.Fatalf("no conversation: %q", k)
	}
}

func TestCodexUserAgent(t *testing.T) {
	ua := codexUserAgent()
	if !strings.HasPrefix(ua, "codex_cli_rs/") || !strings.Contains(ua, " (") || !strings.Contains(ua, "; ") {
		t.Fatalf("%q", ua)
	}
}

// Instructions fetched with the account's model list are kept on disk,
// for the gateway to find though another process asked, ahead of Codex
// CLI's copy.
func TestCodexInstructionsSaved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5.5","model_messages":{"instructions_template":"cli copy"}}]}`), 0o644)
	if got := codexInstructions("gpt-5.5"); got != "cli copy" {
		t.Fatalf("before a fetch: %q", got)
	}
	if got := codexInstructions("gpt-9"); got != codexcat.Prompt {
		t.Fatalf("unknown model: %q", got)
	}
	saveCodexPrompts([]byte(`{"models":[{"slug":"gpt-5.5","model_messages":{"instructions_template":"fetched"}},{"slug":"gpt-4","base_instructions":"base"}]}`))
	if got := codexInstructions("gpt-5.5"); got != "fetched" {
		t.Fatalf("after a fetch: %q", got)
	}
	saveCodexPrompts([]byte(`{"models":[{"slug":"gpt-6","model_messages":{"instructions_template":"six"}}]}`))
	if codexInstructions("gpt-4") != "base" || codexInstructions("gpt-6") != "six" {
		t.Fatal("a second account's list replaced the first's")
	}
}

// Two saves that share an mtime both stay visible. The cache re-reads only
// when ModTime changes, and two writes in one tick do not change it. After
// the second save, Chtimes puts the first mtime back, so the collision is
// certain on every run.
func TestCodexPromptsSameMtime(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	saveCodexPrompts([]byte(`{"models":[{"slug":"gpt-4","base_instructions":"base"}]}`))
	if got := codexInstructions("gpt-4"); got != "base" {
		t.Fatalf("first account: %q", got)
	}
	path := codexPromptsPath()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	prev := st.ModTime()
	saveCodexPrompts([]byte(`{"models":[{"slug":"gpt-6","model_messages":{"instructions_template":"six"}}]}`))
	if err := os.Chtimes(path, prev, prev); err != nil {
		t.Fatal(err)
	}
	if codexInstructions("gpt-4") != "base" || codexInstructions("gpt-6") != "six" {
		t.Fatal("a second account's list replaced the first's")
	}
}
