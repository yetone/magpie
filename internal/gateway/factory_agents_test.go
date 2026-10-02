package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Every agent speaks one of the gateway's client APIs. droid2api covers the
// same four wires (Anthropic Messages, OpenAI Responses, chat completions,
// Gemini generateContent). Whichever of them asks for a Factory model, the
// body Factory receives opens with Droid's line and still carries the
// agent's own prompt. A Gemini model goes to Factory's generate route; the
// others stay on the wire droid sends that model on.
func TestFactoryFromEveryAgentWire(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	f := &fake{t: t, code: http.StatusForbidden, ctype: "application/json", reply: `{"error":{"message":"no"}}`}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	t.Cleanup(provider.FactoryBaseForTest(up.URL, up.URL+"/eu"))

	auth, _ := json.Marshal(map[string]any{
		"accessToken": "tok", "refreshToken": "r",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
		"orgId":     "org_D", "activeOrganizationId": "fac_D", "email": "d@example.com",
	})
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{{
		"agent": "factory", "user": "d@example.com", "plan": "pro", "on": true, "auth": json.RawMessage(auth),
	}})
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	const line = "You are Droid, an AI software engineering agent built by Factory."
	ask := func(path, body, own, wantPath, wantProvider string) {
		t.Helper()
		f.got, f.path, f.head = nil, "", nil
		post(t, path, body)
		if f.path != wantPath {
			t.Fatalf("%s went to %s, body %s", path, f.path, f.got)
		}
		if got := f.head.Get("x-api-provider"); got != wantProvider {
			t.Errorf("%s x-api-provider %q", path, got)
		}
		if !strings.Contains(string(f.got), line) || !strings.Contains(string(f.got), own) {
			t.Errorf("opening or agent prompt missing from %s", f.got)
		}
		var m map[string]any
		if json.Unmarshal(f.got, &m) != nil {
			t.Fatalf("upstream %s", f.got)
		}
		head := openingOf(m)
		if !strings.HasPrefix(head, line) {
			t.Errorf("opening %q\n%s", head, f.got)
		}
	}

	// Claude Code, on each kind of Factory model.
	ask("/v1/messages", `{"model":"factory/claude-opus-5-5","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`,
		"You are Claude Code.", "/api/llm/a/v1/messages", "anthropic")
	ask("/v1/messages", `{"model":"factory/gpt-5.5","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`,
		"You are Claude Code.", "/api/llm/o/v1/responses", "openai")
	ask("/v1/messages", `{"model":"factory/glm-5.3","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`,
		"You are Claude Code.", "/api/llm/o/v1/chat/completions", "fireworks")
	ask("/v1/messages", `{"model":"factory/gemini-3.1-pro-preview","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`,
		"You are Claude Code.", "/api/llm/g/v1/generate", "google")

	// Codex, and Pi (its system prompt is a developer item, not instructions).
	ask("/v1/responses", `{"model":"factory/gpt-5.5","instructions":"You are Codex.","input":[{"role":"user","content":"hi"}]}`,
		"You are Codex.", "/api/llm/o/v1/responses", "openai")
	ask("/v1/responses", `{"model":"factory/claude-opus-5-5","instructions":"You are Codex.","input":[{"role":"user","content":"hi"}]}`,
		"You are Codex.", "/api/llm/a/v1/messages", "anthropic")
	ask("/v1/responses", `{"model":"factory/gemini-3.8-flash","instructions":"You are Codex.","input":[{"role":"user","content":"hi"}]}`,
		"You are Codex.", "/api/llm/g/v1/generate", "google")
	ask("/v1/responses", `{"model":"factory/gpt-5.5","input":[{"role":"developer","content":"You are Pi."},{"role":"user","content":"hi"}]}`,
		"You are Pi.", "/api/llm/o/v1/responses", "openai")

	// OpenCode and Crush, on chat completions.
	ask("/v1/chat/completions", `{"model":"factory/glm-5.3","messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"hi"}]}`,
		"You are OpenCode.", "/api/llm/o/v1/chat/completions", "fireworks")
	ask("/v1/chat/completions", `{"model":"factory/claude-opus-5-5","messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"hi"}]}`,
		"You are OpenCode.", "/api/llm/a/v1/messages", "anthropic")
	ask("/v1/chat/completions", `{"model":"factory/gpt-5.5","messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"hi"}]}`,
		"You are OpenCode.", "/api/llm/o/v1/responses", "openai")
	ask("/v1/chat/completions", `{"model":"factory/gemini-3-flash-preview","messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"hi"}]}`,
		"You are OpenCode.", "/api/llm/g/v1/generate", "google")

	// Gemini CLI and Antigravity.
	for _, model := range []string{"claude-opus-5-5", "gpt-5.5", "glm-5.3", "gemini-3.1-pro-preview"} {
		want, prov := "/api/llm/a/v1/messages", "anthropic"
		switch model {
		case "gpt-5.5":
			want, prov = "/api/llm/o/v1/responses", "openai"
		case "glm-5.3":
			want, prov = "/api/llm/o/v1/chat/completions", "fireworks"
		case "gemini-3.1-pro-preview":
			want, prov = "/api/llm/g/v1/generate", "google"
		}
		ask("/v1beta/models/factory/"+model+":generateContent",
			`{"systemInstruction":{"parts":[{"text":"You are Gemini CLI."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			"You are Gemini CLI.", want, prov)
	}
}

// openingOf is the text a Factory body opens with, on whichever wire it took.
func openingOf(m map[string]any) string {
	if s, ok := m["instructions"].(string); ok {
		return s
	}
	switch s := m["system"].(type) {
	case string:
		return s
	case []any:
		if len(s) > 0 {
			b, _ := s[0].(map[string]any)
			t, _ := b["text"].(string)
			return t
		}
	}
	if si, ok := m["systemInstruction"].(map[string]any); ok {
		parts, _ := si["parts"].([]any)
		if len(parts) > 0 {
			b, _ := parts[0].(map[string]any)
			t, _ := b["text"].(string)
			return t
		}
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) > 0 {
		msg, _ := msgs[0].(map[string]any)
		if msg["role"] == "system" {
			switch c := msg["content"].(type) {
			case string:
				return c
			case []any:
				if len(c) > 0 {
					b, _ := c[0].(map[string]any)
					t, _ := b["text"].(string)
					return t
				}
			}
		}
	}
	return ""
}

// A chat client gets Gemini's streamed answer back as chat completions.
func TestFactoryGeminiStreamsToChat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	f := &fake{t: t, reply: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello-from-factory\"}]},\"finishReason\":\"STOP\"}]}\n\n"}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	t.Cleanup(provider.FactoryBaseForTest(up.URL, up.URL+"/eu"))

	auth, _ := json.Marshal(map[string]any{
		"accessToken": "tok", "refreshToken": "r",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
		"orgId":     "org_D", "activeOrganizationId": "fac_D", "email": "d@example.com",
	})
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{{
		"agent": "factory", "user": "d@example.com", "plan": "pro", "on": true, "auth": json.RawMessage(auth),
	}})
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	code, body := post(t, "/v1/chat/completions", `{"model":"factory/gemini-3.1-pro-preview","stream":true,"messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello-from-factory") {
		t.Fatalf("chat %d %s", code, body)
	}
	if f.path != "/api/llm/g/v1/generate" || !strings.Contains(string(f.got), "You are OpenCode.") || strings.Contains(string(f.got), `"stream"`) {
		t.Fatalf("upstream %s %s", f.path, f.got)
	}

	// generateContent asks for one JSON body. Factory answers SSE; the
	// client gets the JSON, and the upstream body has no stream field.
	code, body = post(t, "/v1beta/models/factory/gemini-3.1-pro-preview:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || strings.HasPrefix(body, "data:") || !strings.Contains(body, "hello-from-factory") {
		t.Fatalf("json %d %s", code, body)
	}
	if strings.Contains(string(f.got), `"stream"`) {
		t.Fatalf("stream sent upstream: %s", f.got)
	}

	code, body = post(t, "/v1beta/models/factory/gemini-3.1-pro-preview:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.Contains(body, "data:") || !strings.Contains(body, "hello-from-factory") {
		t.Fatalf("sse %d %s", code, body)
	}
	if strings.Contains(string(f.got), `"stream"`) {
		t.Fatalf("stream sent upstream: %s", f.got)
	}
}
