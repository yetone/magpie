package gateway

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop's session titles (and Claude Code's small tasks in its
// Code tab) go to the model its session is on, not to the Claude model it
// picks for them or names by itself (ARNO)
func TestClaudeDesktopAuxiliaryModel(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL,
		Models: []string{"m1", "claude-sonnet-5-thinking", "m2"}}); err != nil {
		t.Fatal(err)
	}
	send := func(key, model string, tools bool) string {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":200,"stream":true,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`
		if tools {
			body = `{"model":"` + model + `","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		}
		f.got = nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", key)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		return modelOf(f.got)
	}
	desktop, other := Token+"-claude-desktop", Token+"-claude"

	// nothing picked yet: the title of a new session's first message goes to
	// the model a new session starts on, the first one listed (ARNO)
	if got := send(desktop, "claude-sonnet-5-thinking", false); got != "m1" {
		t.Fatalf("before any turn: sent %q, want m1", got)
	}
	if _, err := os.Stat(desktopPickedPath()); err == nil {
		t.Fatal("a title picked a model")
	}
	// a turn (tools) on a non-Claude model, listed to Desktop by its alias
	if got := send(desktop, aliasFor("fake/m1"), true); got != "m1" {
		t.Fatalf("turn: sent %q", got)
	}
	// the title goes to the session's model
	if got := send(desktop, "claude-sonnet-5-thinking", false); got != "m1" {
		t.Fatalf("title: sent %q, want m1", got)
	}
	// a chat turn without tools on a Claude model the user picked stays on it
	f.got = nil
	chat := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5-thinking","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	chat.Header.Set("x-api-key", desktop)
	New().Handler().ServeHTTP(httptest.NewRecorder(), chat)
	if got := modelOf(f.got); got != "claude-sonnet-5-thinking" {
		t.Fatalf("tool-less chat turn: sent %q", got)
	}
	// a Claude model magpie doesn't serve, tools or not, goes there too
	for _, tools := range []bool{false, true} {
		if got := send(desktop, "claude-haiku-4-5-20251001", tools); got != "m1" {
			t.Fatalf("unserved haiku (tools %v): sent %q", tools, got)
		}
	}
	// a tool-less request for a model that doesn't read as Claude's is as asked
	if got := send(desktop, aliasFor("fake/m2"), false); got != "m2" {
		t.Fatalf("tool-less m2: sent %q", got)
	}
	// another agent's requests are untouched
	if got := send(other, "claude-sonnet-5-thinking", false); got != "claude-sonnet-5-thinking" {
		t.Fatalf("Claude Code's request: sent %q", got)
	}
	// a session on the Claude model itself keeps it for its title
	if got := send(desktop, "claude-sonnet-5-thinking", true); got != "claude-sonnet-5-thinking" {
		t.Fatalf("turn on sonnet: sent %q", got)
	}
	if got := send(desktop, "claude-sonnet-5-thinking", false); got != "claude-sonnet-5-thinking" {
		t.Fatalf("title on a sonnet session: sent %q", got)
	}

	// kept across a restart
	send(desktop, "fake/m2", true)
	if b, _ := os.ReadFile(desktopPickedPath()); strings.TrimSpace(string(b)) != "fake/m2" {
		t.Fatalf("kept %q", b)
	}
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	if got := send(desktop, "claude-sonnet-5-thinking", false); got != "m2" {
		t.Fatalf("title after a restart: sent %q, want m2", got)
	}
}

func TestHasTools(t *testing.T) {
	for body, want := range map[string]bool{
		`{"model":"x"}`:                         false,
		`{"model":"x","tools":[]}`:              false,
		`{"model":"x","tools":[{"name":"a"}]}`:  true,
		`{"system":"no \"tools\" here"}`:        false,
		`{"tools":[{"type":"function"}],"x":1}`: true,
	} {
		if hasTools([]byte(body)) != want {
			t.Errorf("hasTools(%s) = %v", body, !want)
		}
	}
}

// before any turn, a title goes to the model the agent is set to stand in
// for it if there is one, else to the first model listed to Desktop; a
// request for that first model itself, or a real Claude model asked for
// with tools, is as asked
func TestClaudeDesktopDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"m1", "claude-opus-4-8"}}); err != nil {
		t.Fatal(err)
	}
	title := []byte(`{"max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)
	turn := []byte(`{"max_tokens":32000,"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"hi"}]}`)
	if got := desktopDefault("claude-sonnet-5"); got != "fake/m1" {
		t.Fatalf("default = %q", got)
	}
	if got := desktopDefault("fake/m1"); got != "" {
		t.Fatalf("default for the first model itself = %q", got)
	}
	if got := desktopTurn("claude-haiku-4-5", title, false, false); got != "fake/m1" {
		t.Fatalf("title = %q", got)
	}
	StandIn = func(agent, model string) string {
		if agent == "claude-desktop" {
			return "fake/claude-opus-4-8"
		}
		return ""
	}
	t.Cleanup(func() { StandIn = nil })
	if got := desktopTurn("claude-haiku-4-5", title, false, false); got != "fake/claude-opus-4-8" {
		t.Fatalf("title with a stand-in = %q", got)
	}
	// a session on a real Claude model magpie serves keeps it
	if got := desktopTurn("fake/claude-opus-4-8", turn, false, false); got != "fake/claude-opus-4-8" {
		t.Fatalf("turn = %q", got)
	}
}

// Claude Code in Claude Desktop names the model of its own background
// requests on purpose: its auto mode's safety classifier asks for its sonnet
// with no tools and a small max_tokens, and goes to that model, not to the
// session's (#1539, tete1030). Desktop's title still goes to the session's
// model, from Desktop's own client or from Claude Code when Desktop asks it.
func TestClaudeDesktopClassifierKeepsItsModel(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL,
		Models: []string{"claude-opus-5-5", "claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	const cli = "claude-cli/2.1.295 (external, claude-desktop)"
	const electron = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.6608.0 Chrome/142.0.7444.235 Electron/39.2.7 Safari/537.36"
	send := func(ua, body string) string {
		t.Helper()
		f.got = nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return modelOf(f.got)
	}
	// the session is on opus
	if got := send(cli, `{"model":"claude-opus-5-5","max_tokens":64000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`); got != "claude-opus-5-5" {
		t.Fatalf("turn: sent %q", got)
	}
	// the safety classifier's first stage: no tools, max_tokens 64 or 256,
	// the latter with 2048 of thinking on top; the notification classifier
	for _, body := range []string{
		`{"model":"claude-sonnet-5","max_tokens":1024,"stream":true,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.296.523; cc_entrypoint=claude-desktop;"},{"type":"text","text":"A user kicked off a Claude Code agent to do a coding task and walked away."}],"messages":[{"role":"user","content":"…"}]}`,
		`{"model":"claude-sonnet-5","max_tokens":64,"stream":true,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.295; cc_entrypoint=claude-desktop;"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],"messages":[{"role":"user","content":"<transcript>…</transcript>"}]}`,
		`{"model":"claude-sonnet-5","max_tokens":2304,"thinking":{"type":"enabled","budget_tokens":2048},"stream":true,"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],"messages":[{"role":"user","content":"<transcript>…</transcript>"}]}`,
	} {
		if got := send(cli, body); got != "claude-sonnet-5" {
			t.Fatalf("classifier: sent %q, want claude-sonnet-5", got)
		}
	}
	// Desktop's title, from its own client and from Claude Code
	title := `{"model":"claude-sonnet-5","max_tokens":200,"stream":true,"system":"You write short session titles. Reply with only the tagged fields the prompt asks for.","messages":[{"role":"user","content":"hi"}]}`
	if got := send(electron, title); got != "claude-opus-5-5" {
		t.Fatalf("Desktop's title: sent %q, want claude-opus-5-5", got)
	}
	// as Claude Code 2.1.296 sends `claude -p --model claude-sonnet-5
	// --system-prompt "You write short session titles. …" --tools ""`
	cliTitle := `{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"fix the mobile login button","cache_control":{"type":"ephemeral"}}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.296.3fd; cc_entrypoint=claude-desktop;"},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"You write short session titles. Reply with only the tagged fields the prompt asks for.","cache_control":{"type":"ephemeral"}}],"tools":[],"max_tokens":64000,"stream":true}`
	if got := send(cli, cliTitle); got != "claude-opus-5-5" {
		t.Fatalf("Claude Code's title for Desktop: sent %q, want claude-opus-5-5", got)
	}
	// a model magpie doesn't serve still goes to the session's
	if got := send(cli, `{"model":"claude-haiku-4-5-20251001","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`); got != "claude-opus-5-5" {
		t.Fatalf("unserved haiku: sent %q", got)
	}
}

func TestDesktopTitle(t *testing.T) {
	for body, want := range map[string]bool{
		`{"system":"You write short session titles. Reply…"}`:                                               true,
		`{"system":[{"type":"text","text":"cc"},{"type":"text","text":"You write short session titles."}]}`: true,
		`{"system":"You are a security monitor."}`:                                                          false,
		`{"system":"x","messages":[{"role":"user","content":"You write short session titles."}]}`:           false,
		`{"system":[{"type":"text","text":"Quote: You write short session titles."}]}`:                      false,
	} {
		if desktopTitle([]byte(body)) != want {
			t.Errorf("desktopTitle(%s) = %v", body, !want)
		}
	}
}

// A subagent on another served model must not replace the lead's pick,
// even when no subagent model is set. A later lead turn still records.
func TestDesktopSubagentTurnKeepsSessionPick(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"hi"}]}`)
	for _, turn := range []struct {
		model  string
		sub    bool
		picked string
	}{
		{"fake/m1", false, "fake/m1"},
		{"fake/m2", true, "fake/m1"},
		{"fake/m2", false, "fake/m2"},
	} {
		if got := desktopTurn(turn.model, body, true, turn.sub); got != turn.model {
			t.Fatalf("turn sent %q, want %q", got, turn.model)
		}
		if b, err := os.ReadFile(desktopPickedPath()); err != nil || strings.TrimSpace(string(b)) != turn.picked {
			t.Fatalf("subagent %v: picked %q, want %q (%v)", turn.sub, b, turn.picked, err)
		}
	}
	if got := desktopTurn("claude-haiku-unserved", body, true, true); got != "fake/m2" {
		t.Fatalf("unserved subagent sent %q, want the session's model", got)
	}
}

// Through the server: a subagent of Desktop's Code tab on another model
// (its tier's) leaves the session on the lead's model, so the title
// Desktop asks for next goes there, not to the subagent's model.
func TestDesktopSubagentTurnLeavesTitleOnLead(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL,
		Models: []string{"m1", "m2", "claude-sonnet-5-thinking"}}); err != nil {
		t.Fatal(err)
	}
	send := func(model, sub string, tools bool) string {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":200,"stream":true,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`
		if tools {
			body = `{"model":"` + model + `","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		}
		f.got = nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		if tools {
			req.Header.Set("User-Agent", "claude-cli/2.1.295 (external, claude-desktop-3p, agent-sdk/0.3.295)")
		}
		if sub != "" {
			req.Header.Set("x-claude-code-agent-id", sub)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		return modelOf(f.got)
	}
	send("fake/m1", "", true)
	if got := send("fake/m2", "worker", true); got != "m2" {
		t.Fatalf("subagent sent %q, want m2", got)
	}
	if got := send("claude-sonnet-5-thinking", "", false); got != "m1" {
		t.Fatalf("title after a subagent's turn: sent %q, want the lead's m1", got)
	}
}

// Claude Code's WebSearch request as it goes out: 2.1.328's, captured
// from the CLI against a fake server (2.1.317, 2.1.326 and 2.1.330 send
// the same). Its option names web_search, but the request carries
// tool_choice auto, with no thinking. The texts are cut short and the
// user id made up; the keys, their order and the tool are as sent.
const desktopWebSearchBody = `{"model":"fake/m1","messages":[{"role":"user","content":[{"type":"text","text":"Perform a web search for the query: Go release notes"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.328"},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK."},{"type":"text","text":"You run a web search for another Claude model"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}],"tool_choice":{"type":"auto"},"metadata":{"user_id":"{\"device_id\":\"d0\"}"},"max_tokens":128000,"output_config":{"effort":"max"},"stream":true}`

func TestDesktopSearchCall(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"search", desktopWebSearchBody, true},
		{"forced by name", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"}`, `"tool_choice":{"type":"tool","name":"web_search"}`, 1), true},
		{"any tool", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"}`, `"tool_choice":{"type":"any"}`, 1), true},
		{"new tool version", strings.ReplaceAll(desktopWebSearchBody, "20250305", "20260209"), true},
		{"different prompt", strings.ReplaceAll(desktopWebSearchBody, "You run a web search for another Claude model", "Search the web"), true},
		{"no prompt", `{"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"auto"}}`, true},
		{"prompt only", `{"system":"You run a web search for another Claude model"}`, false},
		{"no tools", `{"tool_choice":{"type":"auto"}}`, false},
		{"empty tools", `{"tools":[],"tool_choice":{"type":"auto"}}`, false},
		{"null tools", `{"tools":null,"tool_choice":{"type":"auto"}}`, false},
		{"multiple tools", strings.ReplaceAll(desktopWebSearchBody, `"tools":[`, `"tools":[{"name":"Bash"},`), false},
		{"another tool after it", strings.Replace(desktopWebSearchBody, `"max_uses":8}]`, `"max_uses":8},{"name":"Bash"}]`, 1), false},
		{"client tool", strings.ReplaceAll(desktopWebSearchBody, `"type":"web_search_20250305",`, ""), false},
		{"other server tool", strings.ReplaceAll(desktopWebSearchBody, "web_search_20250305", "web_fetch_20250910"), false},
		{"no version prefix", strings.ReplaceAll(desktopWebSearchBody, "web_search_20250305", "web_search"), false},
		{"other tool name", strings.Replace(desktopWebSearchBody, `"name":"web_search"`, `"name":"other"`, 1), false},
		{"none", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"}`, `"tool_choice":{"type":"none"}`, 1), false},
		{"other choice name", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"}`, `"tool_choice":{"type":"tool","name":"Bash"}`, 1), false},
		{"no choice", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"},`, "", 1), false},
		{"invalid json", desktopWebSearchBody[:len(desktopWebSearchBody)-1], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := desktopSearchCall([]byte(tc.body)); got != tc.want {
				t.Fatalf("desktopSearchCall = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDesktopWebSearchHaiku(t *testing.T) {
	for _, sub := range []bool{false, true} {
		t.Run(map[bool]string{false: "lead", true: "subagent"}[sub], func(t *testing.T) {
			desktopSubagentSetup(t)
			if err := SetDesktopTier("haiku", "fake/m2"); err != nil {
				t.Fatal(err)
			}
			desktopTurn("fake/m1", []byte(`{"tools":[{"name":"Bash"}]}`), true, false)
			if to, why := desktopMove("fake/m1", []byte(desktopWebSearchBody), true, sub); to != "fake/m2" || why != "desktop-search" {
				t.Fatalf("WebSearch: %q by %q, want fake/m2 by desktop-search", to, why)
			}
		})
	}
}

func TestDesktopWebSearchScope(t *testing.T) {
	for _, tc := range []struct {
		name, haiku string
		code        bool
	}{
		{"unset haiku", "", true},
		{"not Claude Code", "fake/m2", false},
		{"same model", "fake/m1", true},
		{"unserved haiku", "gone/m2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desktopSubagentSetup(t)
			if err := SetDesktopTier("haiku", tc.haiku); err != nil {
				t.Fatal(err)
			}
			desktopTurn("fake/m1", []byte(`{"tools":[{"name":"Bash"}]}`), true, false)
			if to, why := desktopMove("fake/m1", []byte(desktopWebSearchBody), tc.code, false); to != "fake/m1" || why != "" {
				t.Fatalf("WebSearch: %q by %q, want fake/m1 unchanged", to, why)
			}
		})
	}
}

func TestDesktopWebSearchLeavesToolTurns(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"multiple tools", strings.ReplaceAll(desktopWebSearchBody, `"tools":[`, `"tools":[{"name":"Bash"},`)},
		{"none", strings.Replace(desktopWebSearchBody, `"tool_choice":{"type":"auto"}`, `"tool_choice":{"type":"none"}`, 1)},
		{"ordinary turn", `{"tools":[{"name":"Bash"}],"tool_choice":{"type":"auto"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desktopSubagentSetup(t)
			if err := SetDesktopTier("haiku", "fake/m2"); err != nil {
				t.Fatal(err)
			}
			desktopTurn("fake/m1", []byte(`{"tools":[{"name":"Bash"}]}`), true, false)
			if to, why := desktopMove("fake/m3", []byte(tc.body), true, false); to != "fake/m3" || why != "" {
				t.Fatalf("tool turn: %q by %q, want fake/m3 unchanged", to, why)
			}
			if b, err := os.ReadFile(desktopPickedPath()); err != nil || strings.TrimSpace(string(b)) != "fake/m3" {
				t.Fatalf("tool turn picked %q, want fake/m3 (%v)", b, err)
			}
		})
	}
}

func TestDesktopWebSearchKeepsSessionPick(t *testing.T) {
	desktopSubagentSetup(t)
	if err := SetDesktopTier("haiku", "fake/m2"); err != nil {
		t.Fatal(err)
	}
	desktopTurn("fake/m1", []byte(`{"tools":[{"name":"Bash"}]}`), true, false)
	desktopPicked.Lock()
	picked, at := desktopPicked.model, desktopPicked.at
	desktopPicked.Unlock()
	for _, asked := range []string{"fake/m1", "fake/m3"} {
		if to, why := desktopMove(asked, []byte(desktopWebSearchBody), true, false); to != "fake/m2" || why != "desktop-search" {
			t.Fatalf("WebSearch on %s: %q by %q", asked, to, why)
		}
		desktopPicked.Lock()
		got, when := desktopPicked.model, desktopPicked.at
		desktopPicked.Unlock()
		if got != picked || !when.Equal(at) {
			t.Fatalf("WebSearch replaced session pick %q at %v with %q at %v", picked, at, got, when)
		}
		if b, err := os.ReadFile(desktopPickedPath()); err != nil || strings.TrimSpace(string(b)) != picked {
			t.Fatalf("WebSearch wrote session pick %q, want %q (%v)", b, picked, err)
		}
	}
}
