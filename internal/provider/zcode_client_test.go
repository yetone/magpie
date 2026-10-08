package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A request as Claude Code sends it: its own system blocks and tools, each
// cached, its working directory named, cached turns, metadata of its own,
// and a number too long for a float.
const zcodeAgentBody = `{"model":"GLM-5.3-Flash","max_tokens":32000,"stream":true,"metadata":{"user_id":"claude-user","x":1},` +
	`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Be brief & <exact>.\n - Primary working directory: /work/proj\n","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
	`"tools":[{"name":"Read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],` +
	`"messages":[{"role":"user","content":"hello"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":12345678901234567890}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","cache_control":{"type":"ephemeral"}},{"type":"text","text":"go on"}]}]}`

// countCached counts the cache breakpoints anywhere in v.
func countCached(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if x["cache_control"] != nil {
			n++
		}
		for _, y := range x {
			n += countCached(y)
		}
	case []any:
		for _, y := range x {
			n += countCached(y)
		}
	}
	return n
}

// zcodeHash is the spec's hash of a text: SHA-256 over its UTF-8, the
// first 8 bytes in hex.
func zcodeHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// ZCode's prompt is the text of the plugin the Start Plan serves, byte for
// byte: the hashes of its strings, computed over the plugin's own.
func TestZCodeStartPromptIsThePlugins(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"cliPrefix":                    {zcodePrompt.Prefix, "46dd360a22c87a92"},
		"stableSections[0]":            {zcodePrompt.Stable, "3f21ff9a88a03a76"},
		"stableSections[1]":            {zcodePrompt.Desktop, "39a1e86c93452c01"},
		"stableSections.join":          {zcodeStable(), "49bda31511fc9670"},
		"dynamicSections.before":       {zcodePrompt.BeforeEnvironment, "bbdc66b399d297ff"},
		"dynamicSections.afterEnviron": {zcodePrompt.AfterEnvironment, "1732b7f098a925d7"},
	} {
		if got := zcodeHash(c.text); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// ZCode's Start Plan turns away a request without ZCode's own system
// prompt as unusual activity (#425, 405 / 3012), and dressed as the desktop
// app or as ZCode's CLI source has it, some still were (miaopasi and ARNO
// on Discord: "0.1.633版本还是不行，我给的插件还是成功的"). A Start Plan
// request reaches zcode.z.ai as the plugin ARNO sent sends it: its three
// cached system blocks (the desktop context in the second) before the
// agent's own uncached text, the context prefix as a turn of its own, one
// cache mark in the turns on the last block of the last, none on tools,
// metadata.user_id naming the device, and the plugin's headers, the token
// as a Bearer only, no X-Device-Mid or anthropic-beta; the rest of the
// request as the agent sent it. A Coding Plan account's request goes as it
// is.
func TestZCodeStartSentAsTheApp(t *testing.T) {
	signIn(t)
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("TZ", "Asia/Shanghai")
	t.Setenv("SHELL", "/bin/zsh")
	// the day the context prefix gives, the same whenever the test runs
	oldClock := zcodeClock
	t.Cleanup(func() { zcodeClock = oldClock })
	zcodeClock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	jwt := zcodeTestJWT(time.Now().Add(24 * time.Hour))
	u := newZCodeStartUpstream(t, jwt)
	u.balance = zcodeActiveStart(time.Now(), "active")

	send := func(k zcodeKey, body string) *http.Request {
		t.Helper()
		p := zcodeProvider("a@example.com", "", k)
		req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("User-Agent", "magpie/test")
		req.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
		if err := p.Sign(context.Background(), req, Anthropic, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req
	}

	req := send(zcodeKey{Base: ZCodeZaiBase, JWT: jwt}, zcodeAgentBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("sent: %v %v", err, res)
	}
	res.Body.Close()
	h := u.model.Header
	osVersion := zcodePlatform() + " " + zcodeOSRelease() + " " + zcodeArch()
	category := map[string]string{"darwin": "macos", "win32": "windows"}[zcodePlatform()]
	if category == "" {
		category = "linux"
	}
	for k, want := range map[string]string{
		"User-Agent": "ZCode/3.14.3 ai-sdk/anthropic/3.0.81", "X-ZCode-App-Version": "3.14.3", "anthropic-version": "2023-06-01",
		"X-Title": "Z Code@cli", "X-ZCode-Agent": "glm", "X-ZCode-Session-Type": "main", "X-Release-Channel": "production",
		"X-Client-Language": "zh-CN", "X-Client-Timezone": "Asia/Shanghai", "HTTP-Referer": "https://zcode.z.ai", "Authorization": "Bearer " + jwt,
		"X-Platform": zcodePlatform() + "-" + zcodeArch(), "X-Os-Category": category, "X-Os-Version": osVersion,
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, k := range []string{"X-Request-Id", "X-ZCode-Trace-Id"} {
		if !uuidRe.MatchString(h.Get(k)) {
			t.Errorf("%s: %q", k, h.Get(k))
		}
	}
	for _, k := range []string{"X-Session-Id", "X-Query-Id", "X-Device-Mid", "X-Api-Key", "anthropic-beta"} {
		if h.Get(k) != "" {
			t.Errorf("%s sent", k)
		}
	}
	if bytes.Contains(u.body, []byte("\\u00"+"3c")) || !bytes.Contains(u.body, []byte(`12345678901234567890`)) {
		t.Fatalf("body not kept as written: %s", u.body)
	}
	type block struct {
		Type  string         `json:"type"`
		Text  string         `json:"text"`
		Cache map[string]any `json:"cache_control"`
	}
	var got struct {
		Model     string         `json:"model"`
		MaxTokens float64        `json:"max_tokens"`
		Stream    bool           `json:"stream"`
		Metadata  map[string]any `json:"metadata"`
		System    []block        `json:"system"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(u.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "GLM-5.3-Flash" || got.MaxTokens != 32000 || !got.Stream || len(got.Messages) != 4 {
		t.Fatalf("the request changed: %s", u.body)
	}
	env := strings.Join([]string{
		"# Environment",
		"You have been invoked in the following environment:",
		"- Primary working directory: /work/proj",
		"- Is a git repository: no",
		"- Platform: " + zcodePlatform(),
		"- Shell: zsh",
		"- OS Version: " + osVersion,
		"- You are powered by the model named zai-api/GLM-5.3-Flash.",
	}, "\n")
	if len(got.System) != 5 ||
		got.System[0].Text != "You are ZCode, an interactive coding agent" ||
		zcodeHash(got.System[1].Text) != "49bda31511fc9670" ||
		got.System[2].Text != "\n\n"+zcodePrompt.BeforeEnvironment+"\n\n"+env+"\n\n"+zcodePrompt.AfterEnvironment ||
		zcodeHash(zcodePrompt.BeforeEnvironment) != "bbdc66b399d297ff" || zcodeHash(zcodePrompt.AfterEnvironment) != "1732b7f098a925d7" {
		t.Fatalf("ZCode's system prompt: %s", u.body)
	}
	for i, s := range got.System {
		if cached := s.Cache != nil; cached != (i < 3) || cached && (len(s.Cache) != 1 || s.Cache["type"] != "ephemeral") {
			t.Errorf("system block %d cache: %v", i, s.Cache)
		}
	}
	if got.System[3].Text != "You are Claude Code." || !strings.HasPrefix(got.System[4].Text, "Be brief & <exact>.") {
		t.Fatalf("the agent's system prompt: %+v", got.System[3:])
	}
	var prefix []block
	json.Unmarshal(got.Messages[0].Content, &prefix)
	want := "<system-reminder>As you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-09-30.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.</system-reminder>"
	if got.Messages[0].Role != "user" || len(prefix) != 1 || prefix[0].Type != "text" || prefix[0].Text != want || prefix[0].Cache != nil {
		t.Fatalf("the context prefix: %s", got.Messages[0].Content)
	}
	if got.Messages[1].Role != "user" || string(got.Messages[1].Content) != `"hello"` {
		t.Fatalf("the first turn: %s", got.Messages[1].Content)
	}
	var all any
	json.Unmarshal(u.body, &all)
	if n := countCached(all); n != 4 {
		t.Fatalf("%d cache breakpoints: %s", n, u.body)
	}
	last := all.(map[string]any)["messages"].([]any)[3].(map[string]any)["content"].([]any)
	if last[0].(map[string]any)["cache_control"] != nil || last[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the last turn's breakpoint: %v", last)
	}
	if tools := all.(map[string]any)["tools"].([]any); tools[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("a tool's cache mark kept: %v", tools)
	}
	var uid map[string]any
	if got.Metadata["x"] != float64(1) || json.Unmarshal([]byte(got.Metadata["user_id"].(string)), &uid) != nil ||
		got.Metadata["user_id"] != `{"device_id":"`+zcodeDeviceMid()+`","account_uuid":"","session_id":""}` {
		t.Fatalf("metadata: %v", got.Metadata)
	}

	// shaped once only; a string system, a string last turn, BigModel
	again := zcodeStartBody(u.body, "zai-api", time.Now())
	if !bytes.Equal(again, u.body) {
		t.Fatalf("shaped twice:\n%s\n%s", u.body, again)
	}
	own := `{"model":"GLM-5.2","system":"mine","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"hi"}]}`
	var o struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(zcodeStartBody([]byte(own), zcodeStartProvider(ZCodeBigModelBase), time.Now()), &o)
	if len(o.System) != 4 || o.System[3]["text"] != "mine" || o.System[3]["cache_control"] != nil || len(o.Messages) != 4 ||
		!strings.Contains(o.System[2]["text"].(string), "- You are powered by the model named bigmodel-api/GLM-5.2.") {
		t.Fatalf("a string system: %+v", o)
	}
	if b, _ := json.Marshal(o.Messages[3].Content); string(b) != `[{"cache_control":{"type":"ephemeral"},"text":"hi","type":"text"}]` {
		t.Fatalf("a string last turn: %s", b)
	}
	if b, _ := json.Marshal(o.Messages[1].Content); strings.Contains(string(b), "cache_control") {
		t.Fatalf("an earlier turn's cache mark kept: %s", b)
	}
	// what isn't a messages request is left alone
	for _, b := range []string{`not json`, `{"model":"GLM-5.2"}`} {
		if got := zcodeStartBody([]byte(b), "zai-api", time.Now()); string(got) != b {
			t.Fatalf("%s became %s", b, got)
		}
	}

	// the GLM Coding Plan: the agent's request as it was, and none of the app's headers
	u.plans = []any{map[string]any{"productName": "GLM Coding Lite", "status": "VALID"}}
	zcodeRoutes.Lock()
	zcodeRoutes.m = map[string]zcodeRoute{}
	zcodeRoutes.Unlock()
	req = send(zcodeKey{Key: "key.secret", Base: ZCodeZaiBase, JWT: jwt}, zcodeAgentBody)
	if !strings.HasPrefix(req.URL.Path, "/api/anthropic/") {
		t.Fatalf("coding plan went to %s", req.URL)
	}
	b, _ := io.ReadAll(req.Body)
	if string(b) != zcodeAgentBody {
		t.Fatalf("coding plan body changed: %s", b)
	}
	for _, k := range []string{"X-ZCode-Agent", "X-ZCode-Trace-Id", "X-Title", "X-Client-Language"} {
		if req.Header.Get(k) != "" {
			t.Errorf("coding plan sent %s", k)
		}
	}
	if req.Header.Get("User-Agent") != "magpie/test" {
		t.Errorf("coding plan User-Agent: %q", req.Header.Get("User-Agent"))
	}
}
