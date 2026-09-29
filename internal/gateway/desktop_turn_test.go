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
	if got := desktopTurn("claude-haiku-4-5", title); got != "fake/m1" {
		t.Fatalf("title = %q", got)
	}
	StandIn = func(agent, model string) string {
		if agent == "claude-desktop" {
			return "fake/claude-opus-4-8"
		}
		return ""
	}
	t.Cleanup(func() { StandIn = nil })
	if got := desktopTurn("claude-haiku-4-5", title); got != "fake/claude-opus-4-8" {
		t.Fatalf("title with a stand-in = %q", got)
	}
	// a session on a real Claude model magpie serves keeps it
	if got := desktopTurn("fake/claude-opus-4-8", turn); got != "fake/claude-opus-4-8" {
		t.Fatalf("turn = %q", got)
	}
}
