package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// #506: Codex on glm-5.3-flash through a Factory account got 403, as Grok
// Build's and Claude Code's requests did in #242, where Droid's own through
// magpie went through on the same models, endpoint and headers. droid
// 0.231.0 opens every system prompt with its line. Responses, chat and
// Gemini's generate join the agent's prompt on with one "\n". With no
// prompt of its own, the line stands alone. Anthropic's Messages takes
// the line as the first system block. droid's own request goes on byte
// for byte.
func TestFactoryOpensAsDroid(t *testing.T) {
	signIn(t)
	tok := factoryToken(map[string]any{"sub": "user_d", "org_id": "org_D"})
	var mu sync.Mutex
	got := map[string][]byte{}
	factorySite(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if int64(len(b)) != r.ContentLength {
			t.Errorf("%s: read %d bytes, Content-Length %d", r.URL.Path, len(b), r.ContentLength)
		}
		mu.Lock()
		got[r.URL.Path] = b
		mu.Unlock()
		if r.URL.Path == "/api/cli/whoami" {
			factoryJSON(w, 200, map[string]any{"userId": "user_d", "orgId": "fac_D"})
			return
		}
		io.WriteString(w, `{"id":"ok"}`)
	})
	auth, _ := json.Marshal(map[string]any{"accessToken": tok, "refreshToken": "r1",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "orgId": "org_D", "activeOrganizationId": "fac_D", "email": "d@example.com"})
	if err := addSideLogin(savedLogin{Agent: "factory", User: "d@example.com", Auth: auth}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(Accounts(), "factory")
	if !ok {
		t.Fatal("no factory provider")
	}
	send := func(url string, proto Protocol, body string) []byte {
		t.Helper()
		req, _ := http.NewRequest("POST", url, strings.NewReader(body))
		req.ContentLength = int64(len(body))
		if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		u, _ := http.NewRequest("POST", url, nil)
		mu.Lock()
		defer mu.Unlock()
		return got[u.URL.Path]
	}
	chat, responses, messages := p.Chat+"/chat/completions", p.Responses+"/responses", p.Anthropic+"/v1/messages"
	generate := p.Base(Gemini) + "/generate"
	if !strings.HasSuffix(p.Base(Gemini), "/api/llm/g/v1") {
		t.Fatalf("gemini base %s", p.Base(Gemini))
	}
	type msg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	read := func(b []byte) (out struct {
		Model        string          `json:"model"`
		Instructions *string         `json:"instructions"`
		Effort       string          `json:"reasoning_effort"`
		Stream       bool            `json:"stream"`
		Messages     []msg           `json:"messages"`
		Input        any             `json:"input"`
		System       json.RawMessage `json:"system"`
	}) {
		t.Helper()
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("sent %s: %v", b, err)
		}
		return out
	}

	// Codex on glm-5.3-flash, its Responses request made chat completions:
	// its instructions as the first system message
	b := read(send(chat, Chat, `{"model":"glm-5.3-flash","messages":[{"role":"system","content":"You are Codex, a coding agent."},{"role":"user","content":"hi <b> & co"}],"stream":true,"reasoning_effort":"high"}`))
	if len(b.Messages) != 2 || b.Messages[0].Role != "system" || b.Messages[0].Content != factoryDroidPrompt+"You are Codex, a coding agent." ||
		b.Messages[1].Content != "hi <b> & co" || b.Model != "glm-5.3-flash" || b.Effort != "high" || !b.Stream {
		t.Errorf("codex on chat: %+v", b)
	}
	// Claude Code's system blocks, made chat completions as parts: joined
	b = read(send(chat, Chat, `{"model":"kimi-k3","messages":[{"role":"system","content":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Be brief."}]},{"role":"user","content":"hi"}]}`))
	if len(b.Messages) != 2 || b.Messages[0].Content != factoryDroidPrompt+"You are Claude Code.\nBe brief." {
		t.Errorf("claude code on chat: %+v", b)
	}
	// no system prompt: droid's line alone, before the rest
	b = read(send(chat, Chat, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`))
	if len(b.Messages) != 2 || b.Messages[0].Role != "system" || b.Messages[0].Content != factoryDroidLine || b.Messages[1].Role != "user" {
		t.Errorf("no system on chat: %+v", b)
	}
	// Codex on GPT, Responses as it is: the instructions open with the line
	b = read(send(responses, Responses, `{"model":"gpt-5.5","instructions":"You are Codex, a coding agent.","input":[{"role":"user","content":"hi"}],"stream":true}`))
	if b.Instructions == nil || *b.Instructions != factoryDroidPrompt+"You are Codex, a coding agent." || len(b.Input.([]any)) != 1 || !b.Stream {
		t.Errorf("codex on responses: %+v", b)
	}
	b = read(send(responses, Responses, `{"model":"grok-4.7","input":"hi"}`))
	if b.Instructions == nil || *b.Instructions != factoryDroidLine {
		t.Errorf("no instructions on responses: %+v", b)
	}

	// Claude Code on Claude, a string system: two blocks, the line then its own
	b = read(send(messages, Anthropic, `{"model":"claude-opus-5-5","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}]}`))
	var blocks []map[string]any
	if json.Unmarshal(b.System, &blocks) != nil || len(blocks) != 2 || blocks[0]["text"] != factoryDroidLine || blocks[1]["text"] != "You are Claude Code." ||
		len(b.Messages) != 1 || b.Messages[0].Content != "hi" {
		t.Errorf("claude code on messages: system %s messages %+v", b.System, b.Messages)
	}
	// Claude Code's own blocks stay after the line, cache control included
	b = read(send(messages, Anthropic, `{"model":"claude-sonnet-5","system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Be brief."}],"messages":[{"role":"user","content":"hi"}]}`))
	blocks = nil
	if json.Unmarshal(b.System, &blocks) != nil || len(blocks) != 3 || blocks[0]["text"] != factoryDroidLine || blocks[1]["text"] != "You are Claude Code." || blocks[2]["text"] != "Be brief." {
		t.Errorf("claude code blocks: %s", b.System)
	}
	if cc, _ := blocks[1]["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("cache control: %v", blocks)
	}
	// no system prompt: the line alone
	b = read(send(messages, Anthropic, `{"model":"minimax-m2.7","messages":[{"role":"user","content":"hi"}]}`))
	blocks = nil
	if json.Unmarshal(b.System, &blocks) != nil || len(blocks) != 1 || blocks[0]["text"] != factoryDroidLine || len(b.Messages) != 1 {
		t.Errorf("no system on messages: system %s messages %+v", b.System, b.Messages)
	}

	// Gemini CLI on Factory's generate: one part, the prompt then its own text
	sent := send(generate, Gemini, `{"model":"gemini-3.1-pro-preview","systemInstruction":{"parts":[{"text":"You are Gemini CLI."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var g struct {
		SystemInstruction struct {
			Parts []map[string]any `json:"parts"`
		} `json:"systemInstruction"`
		Contents []any `json:"contents"`
	}
	if json.Unmarshal(sent, &g) != nil || len(g.SystemInstruction.Parts) != 1 || g.SystemInstruction.Parts[0]["text"] != factoryDroidPrompt+"You are Gemini CLI." || len(g.Contents) != 1 {
		t.Errorf("gemini cli on generate: %s", sent)
	}
	sent = send(generate, Gemini, `{"model":"gemini-3-flash-preview","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	g = struct {
		SystemInstruction struct {
			Parts []map[string]any `json:"parts"`
		} `json:"systemInstruction"`
		Contents []any `json:"contents"`
	}{}
	if json.Unmarshal(sent, &g) != nil || len(g.SystemInstruction.Parts) != 1 || g.SystemInstruction.Parts[0]["text"] != factoryDroidLine || len(g.Contents) != 1 {
		t.Errorf("no system on generate: %s", sent)
	}

	// droid's own requests go on byte for byte
	for _, c := range []struct {
		url   string
		proto Protocol
		body  string
	}{
		{chat, Chat, `{"model":"glm-5.3-flash","messages":[{"role":"system","content":"` + factoryDroidLine + `\nYou work in the user's terminal."},{"role":"user","content":"hi"}],"stream":true,"n":1.0}`},
		{responses, Responses, `{"model":"gpt-5.5","input":[],"store":false,"instructions":"` + factoryDroidLine + `\nYou work in the user's terminal.","stream":true}`},
		{messages, Anthropic, `{"model":"claude-opus-5-5","system":"` + factoryDroidLine + `\nYou are Claude Code.","messages":[{"role":"user","content":"hi"}]}`},
		{messages, Anthropic, `{"model":"claude-opus-5-5","system":[{"type":"text","text":"` + factoryDroidLine + `"},{"type":"text","text":"Be brief."}],"messages":[{"role":"user","content":"hi"}]}`},
		{generate, Gemini, `{"model":"gemini-3.1-pro-preview","systemInstruction":{"parts":[{"text":"` + factoryDroidLine + `\nYou are Gemini CLI."}]},"contents":[]}`},
	} {
		if sent := send(c.url, c.proto, c.body); !bytes.Equal(sent, []byte(c.body)) {
			t.Errorf("changed:\n%s\nsent:\n%s", c.body, sent)
		}
	}
}

// droid 0.231.0's registry: GLM-5.3-Flash takes images.
func TestFactoryGLMFlashImages(t *testing.T) {
	if m, ok := factoryModelOf("glm-5.3-flash"); !ok || !m.images {
		t.Errorf("glm-5.3-flash: %+v", m)
	}
	if factoryVersion != "0.231.0" {
		t.Errorf("factoryVersion %s", factoryVersion)
	}
	if m, ok := factoryModelOf("gemini-3.1-pro-preview"); !ok || m.api != Gemini || m.upstream != "google" || factoryCore("gemini-3.1-pro-preview") {
		t.Errorf("gemini-3.1-pro-preview: %+v core %v", m, factoryCore("gemini-3.1-pro-preview"))
	}
	if !factoryCore("glm-5.3") {
		t.Error("glm-5.3 is not Droid Core")
	}
}
