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

// The conversation goes in the session headers Codex CLI 0.162 sends,
// session-id and thread-id, by which the ChatGPT backend finds its cached
// prompt; the body's prompt_cache_key alone left a request with tools
// uncached (#1473). Older Codex's underscored names don't go on, relayed
// or not, and a request without a key keeps the session its client named.
func TestCodexSignSessionHeaders(t *testing.T) {
	signIn(t)
	p, _ := find(All(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	req.Header.Set("session_id", "relayed")
	req.Header.Set("conversation_id", "relayed")
	if err := p.Sign(context.Background(), req, Responses, []byte(`{"prompt_cache_key":"thread-1"}`)); err != nil {
		t.Fatal(err)
	}
	h := req.Header
	if h.Get("session-id") != "thread-1" || h.Get("thread-id") != "thread-1" {
		t.Fatalf("session-id %q thread-id %q, want thread-1: %v", h.Get("session-id"), h.Get("thread-id"), h)
	}
	for k := range h {
		if strings.Contains(k, "_") {
			t.Errorf("header %s: %v", k, h)
		}
	}

	// a body without a key (no user message yet): the client's own session
	req, _ = http.NewRequest("POST", p.Responses+"/responses", nil)
	req.Header.Set("session-id", "codex-thread")
	req.Header.Set("thread-id", "codex-thread")
	if err := p.Sign(context.Background(), req, Responses, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("session-id") != "codex-thread" || req.Header.Get("thread-id") != "codex-thread" {
		t.Fatalf("keyless request lost its session: %v", req.Header)
	}

	// a client that names none: each turn of a conversation gets the
	// same session, from how it starts
	turn := func(input string) string {
		req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
		body := p.Prepare([]byte(`{"model":"gpt-5.5","instructions":"You are omp.","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[` + input + `]}`))
		if err := p.Sign(context.Background(), req, Responses, body); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("session-id")
	}
	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}`
	first := turn(user)
	next := turn(user + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"and the test?"}]}`)
	if first == "" || first != next {
		t.Fatalf("sessions %q and %q", first, next)
	}
}

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
		h.Get("OpenAI-Beta") != "responses=experimental" || h.Get("session-id") != "thread-1" || h.Get("thread-id") != "thread-1" {
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
	ua := codexUserAgent("0.159.0")
	if !strings.HasPrefix(ua, "codex_cli_rs/0.159.0 (") || !strings.Contains(ua, " (") || !strings.Contains(ua, "; ") {
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
	// a model neither list has but Codex's bundled catalog does: that
	// catalog's, as Codex CLI sends it, not its prompt for an unknown model
	if got := codexInstructions("gpt-6-astra"); got != codexcat.Official("gpt-6-astra") || got == codexcat.Prompt {
		t.Fatalf("bundled model: %.60q", got)
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

// Codex's Fast mode reaches the ChatGPT backend as service_tier "priority",
// its Ultrafast as "ultrafast"; any other tier is dropped as before.
func TestCodexBodyServiceTier(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tier := func(body string) (any, bool) {
		var m map[string]any
		json.Unmarshal(codexBody([]byte(body)), &m)
		v, ok := m["service_tier"]
		return v, ok
	}
	if v, _ := tier(`{"model":"gpt-6-sol","service_tier":"priority","input":"hi"}`); v != "priority" {
		t.Errorf("priority: %v", v)
	}
	if v, _ := tier(`{"model":"gpt-6-sol","service_tier":"ultrafast","input":"hi"}`); v != "ultrafast" {
		t.Errorf("ultrafast: %v", v)
	}
	for _, s := range []string{`"flex"`, `"auto"`, `"default"`, `"fast"`, `null`} {
		if v, ok := tier(`{"model":"gpt-6-sol","service_tier":` + s + `,"input":"hi"}`); ok {
			t.Errorf("%s kept as %v", s, v)
		}
	}
	if v, ok := tier(`{"model":"gpt-6-sol","input":"hi"}`); ok {
		t.Errorf("none became %v", v)
	}
}

// Standalone notifications retain the metadata the native backend recognizes;
// paired outputs and ID-bearing historical orphans keep their existing rules.
func TestCodexStandaloneNotifications(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, id := range []string{"", `,"call_id":""`} {
			body := []byte(`{"model":"gpt-5.5","input":[{"type":"` + kind + `","id":"standalone","name":"send_message_to_thread","namespace":"codex_app","output":"native notice"` + id + `}]}`)
			var before, after struct {
				Input []map[string]any `json:"input"`
			}
			json.Unmarshal(body, &before)
			json.Unmarshal(codexBody(body), &after)
			b, _ := json.Marshal(before.Input)
			a, _ := json.Marshal(after.Input)
			if string(a) != string(b) {
				t.Fatalf("standalone changed: %s -> %s", b, a)
			}
		}
	}
}

// A replayed web search goes with the web_search tool declared where the
// request carries its tools (#1270): in the tools, or for Codex's Responses
// Lite in an additional_tools item, which the backend wants there; a
// request that offered no tools still calls none, and one that chose a
// tool keeps its choice.
func TestDeclareSearch(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const search = `{"type":"web_search_call","status":"completed","action":{"type":"search","query":"go release"}}`
	const user = `{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}`
	// web.run as Codex declares it (codex-rs/ext/web-search, tool.rs)
	const webRun = `{"type":"namespace","name":"web","description":"Tools in the web namespace.","tools":[{"type":"function","name":"run","description":"Search the web.","strict":false,"parameters":{"type":"object","properties":{"search_query":{"type":"array"}}}}]}`
	type sent struct {
		Input []struct {
			Type  string           `json:"type"`
			Tools []map[string]any `json:"tools"`
		} `json:"input"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice any              `json:"tool_choice"`
	}
	types := func(ts []map[string]any) string {
		var out []string
		for _, t := range ts {
			out = append(out, t["type"].(string))
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		name, body          string
		lite                bool
		tools, extra, first string
		choice              any
	}{
		{"Lite on an account: into its additional_tools", `{"model":"gpt-6.1-sol","parallel_tool_calls":false,"reasoning":{"effort":"low","context":"all_turns"},"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[]}]},` + search + `,` + user + `]}`, false, "", "namespace,web_search", "additional_tools", "auto"},
		{"Lite with no tools item: one first", `{"model":"gpt-6.1-sol","parallel_tool_calls":false,"reasoning":{"effort":"low","context":"all_turns"},"input":[` + search + `,{"type":"compaction_trigger"}]}`, false, "", "web_search", "additional_tools", "none"},
		{"declared already", `{"model":"gpt-5.5","tools":[{"type":"web_search_preview"}],"input":[` + search + `]}`, false, "web_search_preview", "", "web_search_call", "auto"},
		{"declared in additional_tools", `{"model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"web_search"}]},` + search + `]}`, false, "", "web_search", "additional_tools", "auto"},
		// Codex's web.run is its search: the backend refuses it beside the
		// hosted tool (Savin on Discord, Codex 0.163.0-alpha)
		{"web.run in Lite's additional_tools", `{"model":"gpt-6.1-sol","reasoning":{"effort":"low","context":"all_turns"},"input":[{"type":"additional_tools","role":"developer","tools":[` + webRun + `]},` + search + `,` + user + `]}`, false, "", "namespace", "additional_tools", "auto"},
		{"web.run among the tools", `{"model":"gpt-5.5","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},` + webRun + `],"input":[` + search + `,` + user + `]}`, false, "function,namespace", "", "web_search_call", "auto"},
		{"web.run flat", `{"model":"gpt-5.5","tools":[{"type":"function","name":"web.run","parameters":{"type":"object"}}],"input":[` + search + `]}`, false, "function", "", "web_search_call", "auto"},
		{"another namespace's run", `{"model":"gpt-5.5","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]}],"input":[` + search + `]}`, false, "namespace,web_search", "", "web_search_call", "auto"},
	} {
		var got sent
		if err := json.Unmarshal(codexBody([]byte(c.body)), &got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var extra []map[string]any
		for _, it := range got.Input {
			extra = append(extra, it.Tools...)
		}
		if types(got.Tools) != c.tools || types(extra) != c.extra || got.Input[0].Type != c.first || got.ToolChoice != c.choice {
			t.Errorf("%s: tools %q, additional %q, first %s, tool_choice %v", c.name, types(got.Tools), types(extra), got.Input[0].Type, got.ToolChoice)
		}
	}

	// a choice of a tool stays; tools that aren't a list are left alone
	for in, want := range map[string]string{
		`{"tools":[],"tool_choice":"required","input":[` + search + `]}`:              `"tool_choice":"required"`,
		`{"tool_choice":{"type":"function","name":"shell"},"input":[` + search + `]}`: `"tool_choice":{"name":"shell","type":"function"}`,
		`{"tools":{},"input":[` + search + `]}`:                                       `"tools":{}`,
		`{"tools":[],"input":[` + user + `]}`:                                         `"tools":[]`,
	} {
		if out := string(DeclareSearch([]byte(in), false)); !strings.Contains(out, want) {
			t.Errorf("%s:\n%s", in, out)
		}
	}
	// Codex's Lite header names a request Lite without anything in it
	var lite sent
	json.Unmarshal(DeclareSearch([]byte(`{"input":[`+search+`]}`), true), &lite)
	if len(lite.Tools) != 0 || lite.Input[0].Type != "additional_tools" || types(lite.Input[0].Tools) != "web_search" || lite.ToolChoice != "none" {
		t.Errorf("Lite: %+v", lite)
	}
}

// #2154 (neavo): the ChatGPT backend turns prompt_cache_options away on
// every model (400 "prompt_cache_options is not supported on this model"),
// so the Codex request goes without it; the cache key stays.
func TestCodexBodyDropsPromptCacheOptions(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var m map[string]any
	json.Unmarshal(codexBody([]byte(`{"model":"gpt-5.5","prompt_cache_key":"k","prompt_cache_options":{"mode":"explicit"},"input":"hi"}`)), &m)
	if _, ok := m["prompt_cache_options"]; ok {
		t.Errorf("prompt_cache_options sent: %v", m)
	}
	if m["prompt_cache_key"] != "k" {
		t.Errorf("cache key lost: %v", m)
	}
}

// A message's status goes with its id, and Codex's reasoning items carry
// none, so both lose it with the id (Doby on Discord: Pi's assistant
// message sent again with status "completed" was refused 400 "Unknown
// parameter: 'input[8].status'"). A tool call keeps its status, as Codex
// sends it.
func TestCodexInputDropsStatusWithID(t *testing.T) {
	in := []any{}
	json.Unmarshal([]byte(`[
	  {"role":"user","content":"hi","status":"completed"},
	  {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"e","status":"completed"},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}],"status":"completed","id":"msg_1"},
	  {"type":"function_call","id":"fc_1","call_id":"c1","name":"ls","arguments":"{}","status":"completed"},
	  {"type":"local_shell_call","id":"lsh_1","call_id":"c2","status":"completed","action":{"type":"exec","command":["ls"]}},
	  {"type":"function_call_output","call_id":"c1","output":"a"},
	  {"type":"local_shell_call_output","call_id":"c2","output":"b"}]`), &in)
	out := codexInput(in)
	want := []bool{false, false, false, true, true, false, false}
	if len(out) != len(want) {
		t.Fatalf("items: %v", out)
	}
	for i, item := range out {
		_, has := item.(map[string]any)["status"]
		if has != want[i] {
			t.Errorf("input[%d] status kept %v, want %v: %v", i, has, want[i], item)
		}
	}
}
