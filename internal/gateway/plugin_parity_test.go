package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// movedFake is the fake plugin signed in as the built-in id it was moved
// onto, its requests going to up.
func movedFake(t *testing.T, id string, up http.Handler) {
	t.Helper()
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", id)
	t.Cleanup(plugin.Settle)
	testenv.FakeVendor(t, up)
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{id: map[string]any{"state": provider.MovePlugin}})
	if err := os.WriteFile(filepath.Join(dir, "migrations.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if !provider.Moved(id) {
		t.Fatalf("%s isn't marked moved", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, id, 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find(id); err != nil || !p.IsPlugin() {
		t.Fatalf("Find(%s) = %+v, %v", id, p, err)
	}
}

// Factory moved onto its plugin counts a prompt's tokens as the built-in
// did, by estimate: the vendor isn't asked.
func TestMovedCountTokensEstimated(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	movedFake(t, "factory", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"input_tokens":999}`))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"factory/fake-claude","messages":[{"role":"user","content":"hello there"}]}`))
	New().Handler().ServeHTTP(rec, req)
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || got.InputTokens == 0 {
		t.Fatalf("count_tokens: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) > 0 || got.InputTokens == 999 {
		t.Fatalf("the vendor was asked to count: %v, %d", asked, got.InputTokens)
	}
}

// thoughtChat is a fast request with an assistant turn that reasoned, and
// chatBody the chat request built of it for host.
var thoughtChat = &Request{Fast: true, Messages: []Message{
	{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
	{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "pondered"}, {Kind: Text, Text: "hello"}}},
	{Role: "user", Parts: []Part{{Kind: Text, Text: "again"}}},
}}

func chatBody(t *testing.T, host string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buildChat(thoughtChat, "m", host, false), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Cursor moved onto its plugin is told fast mode as the built-in told
// Cursor: its plugin reads the chat request's service_tier. No other
// chat upstream is told it.
func TestCursorPluginFast(t *testing.T) {
	if m := chatBody(t, "cursor"); m["service_tier"] != "priority" {
		t.Errorf("Cursor's plugin wasn't told fast: %v", m["service_tier"])
	}
	for _, host := range []string{"api.openai.com", "openrouter.ai", "zed", "api.deepseek.com", provider.CommandCodePlanID} {
		if m := chatBody(t, host); m["service_tier"] != nil {
			t.Errorf("%s was told a service tier", host)
		}
	}
}

// A Command Code Go conversation moved onto its plugin keeps its
// reasoning, as the built-in replayed it to /alpha/generate: its plugin
// reads reasoning_content. DeepSeek, Qoder's plugin, which sends it on
// as Qoder's client does, and Devin's, which gives it to swe-2 as its
// thinking (plugins#703), get it too; others don't.
func TestCommandCodePluginReasoning(t *testing.T) {
	reasoning := func(host string) any {
		return chatBody(t, host)["messages"].([]any)[1].(map[string]any)["reasoning_content"]
	}
	for _, host := range []string{provider.CommandCodePlanID, "api.deepseek.com", "qoder", "qoder-cn", "devin"} {
		if got := reasoning(host); got != "pondered" {
			t.Errorf("%s got reasoning %v", host, got)
		}
	}
	for _, host := range []string{"api.openai.com", "cursor", "zed", "copilot.tencent.com", "api.moonshot.cn"} {
		if got := reasoning(host); got != nil {
			t.Errorf("%s got reasoning_content %v", host, got)
		}
	}
}

// A moved provider's account is told in the trace as its provider's, so
// its plan reads as the built-in's did, not as a plugin's.
func TestTraceNamesMovedAccount(t *testing.T) {
	p := provider.Provider{ID: "grok", Name: "Grok", Account: &provider.Account{Agent: "plugin", User: "me@example.com", Plan: "SuperGrok Heavy"}}
	w := weighed(candidate{p: p, rest: "grok", model: "grok-4"}, p, weighing{}, false, "")
	if w.Kind != "account" || w.Agent != "grok" || w.Plan != "SuperGrok Heavy" {
		t.Fatalf("weighed = %+v, want grok's account", w)
	}
}

// A failed tool result reaches the plugins of Devin and Qoder as their
// built-ins sent it, "Error: " first, and Cursor's as is_error, which its
// plugin reads; no other chat upstream is told either.
func TestPluginToolFailureSaid(t *testing.T) {
	failed := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "go"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "Bash", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "exit status 1", IsError: true}}},
	}}
	tool := func(host string) map[string]any {
		var m struct{ Messages []map[string]any }
		if err := json.Unmarshal(buildChat(failed, "m", host, false), &m); err != nil {
			t.Fatal(err)
		}
		for _, msg := range m.Messages {
			if msg["role"] == "tool" {
				return msg
			}
		}
		t.Fatalf("%s: no tool message in %v", host, m.Messages)
		return nil
	}
	for _, host := range []string{"devin", "qoder", "qoder-cn"} {
		if got := tool(host); got["content"] != "Error: exit status 1" || got["is_error"] != nil {
			t.Errorf("%s: %v", host, got)
		}
	}
	if got := tool("cursor"); got["content"] != "exit status 1" || got["is_error"] != true {
		t.Errorf("cursor: %v", got)
	}
	for _, host := range []string{"api.openai.com", "api.deepseek.com", "zed", provider.CommandCodePlanID} {
		if got := tool(host); got["content"] != "exit status 1" || got["is_error"] != nil {
			t.Errorf("%s: %v", host, got)
		}
	}
}

// A moved Command Code account's chat is built for its plugin, not for the
// built-in's API host it shows, so a Go conversation keeps its reasoning
// (chatReplaysReasoning) as one added on the plugin does.
func TestMovedBuildHostIsPlugin(t *testing.T) {
	movedFake(t, provider.CommandCodePlanID, http.NotFoundHandler())
	p, err := provider.Find(provider.CommandCodePlanID)
	if err != nil {
		t.Fatal(err)
	}
	if got := buildHost(*p); got != provider.CommandCodePlanID {
		t.Fatalf("buildHost = %q, Host = %q", got, p.Host())
	}
}
