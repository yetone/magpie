package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A request as Claude Code sends it: its own system blocks and tools, each
// cached, cached turns, and a number too long for a float.
const zcodeAgentBody = `{"model":"GLM-5.3-Flash","max_tokens":32000,"stream":true,` +
	`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Be brief & <exact>.","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
	`"tools":[{"name":"Read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],` +
	`"messages":[{"role":"user","content":"hello"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":12345678901234567890}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","cache_control":{"type":"ephemeral"}},{"type":"text","text":"go on","cache_control":{"type":"ephemeral"}}]}]}`

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

// ZCode's Start Plan turns away a request without ZCode's own system
// prompt as unusual activity (#425, 405 / 3012), as zcode2api found. A
// Start Plan request reaches zcode.z.ai as the ZCode app sends it: its
// three cached system blocks before the agent's own, the day reminded
// before the first turn, at most four cache breakpoints, and the app's
// headers; the rest of the request as the agent sent it. A Coding Plan
// account's request goes as it is.
func TestZCodeStartSentAsTheApp(t *testing.T) {
	signIn(t)
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("TZ", "Asia/Shanghai")
	jwt := zcodeTestJWT(time.Now().Add(24 * time.Hour))
	u := newZCodeStartUpstream(t, jwt)
	u.balance = zcodeActiveStart(time.Now(), "active")

	send := func(k zcodeKey, body string) *http.Request {
		t.Helper()
		p := zcodeProvider("a@example.com", "", k)
		req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("User-Agent", "magpie/test")
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
	for k, want := range map[string]string{
		"User-Agent": "ZCode/" + zcodeAppVersion + " ai-sdk/anthropic/3.0.81", "X-ZCode-App-Version": zcodeAppVersion,
		"X-Title": "Z Code@electron", "X-ZCode-Agent": "glm", "X-ZCode-Session-Type": "main", "X-Release-Channel": "production",
		"X-Client-Language": "zh-CN", "X-Client-Timezone": "Asia/Shanghai", "HTTP-Referer": zcodeAPI, "Authorization": "Bearer " + jwt,
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"X-Request-Id", "X-ZCode-Trace-Id", "X-Platform", "X-Os-Category", "X-Device-Mid"} {
		if h.Get(k) == "" {
			t.Errorf("no %s", k)
		}
	}
	for _, k := range []string{"X-Session-Id", "X-Query-Id"} {
		if h.Get(k) != "" {
			t.Errorf("%s sent", k)
		}
	}
	if bytes.Contains(u.body, []byte("\\u00"+"3c")) || !bytes.Contains(u.body, []byte(`12345678901234567890`)) {
		t.Fatalf("body not kept as written: %s", u.body)
	}
	var got struct {
		Model     string  `json:"model"`
		MaxTokens float64 `json:"max_tokens"`
		Stream    bool    `json:"stream"`
		System    []struct {
			Text  string         `json:"text"`
			Cache map[string]any `json:"cache_control"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(u.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "GLM-5.3-Flash" || got.MaxTokens != 32000 || !got.Stream || len(got.Messages) != 3 {
		t.Fatalf("the request changed: %s", u.body)
	}
	if len(got.System) != 5 || got.System[0].Text != "You are ZCode, an interactive coding agent" ||
		!strings.HasPrefix(got.System[1].Text, "\nYou are an interactive ZCode agent that helps users with software engineering tasks.") ||
		!strings.Contains(got.System[1].Text, "# ZCode Desktop Context") ||
		!strings.HasPrefix(got.System[2].Text, "\n\n# Communicating with the user") ||
		!strings.Contains(got.System[2].Text, "\n# Environment\nYou have been invoked in the following environment:\n") ||
		!strings.Contains(got.System[2].Text, "- You are powered by the model named bigmodel-api/glm-5.3-flash.") ||
		!strings.HasSuffix(got.System[2].Text, "may have a different cause.") {
		t.Fatalf("ZCode's system prompt: %s", u.body)
	}
	for i, s := range got.System {
		if cached := s.Cache != nil; cached != (i < 3) {
			t.Errorf("system block %d cached: %v", i, cached)
		}
	}
	if got.System[3].Text != "You are Claude Code." || got.System[4].Text != "Be brief & <exact>." {
		t.Fatalf("the agent's system prompt: %+v", got.System[3:])
	}
	first := got.Messages[0].Content
	if len(first) != 2 || !strings.HasPrefix(first[0].Text, "<system-reminder>As you answer the user's questions") ||
		!strings.Contains(first[0].Text, "# currentDate\nToday's date is "+time.Now().Format("2006-01-02")+".") || first[1].Text != "hello" {
		t.Fatalf("first turn: %+v", first)
	}
	var all any
	json.Unmarshal(u.body, &all)
	if n := countCached(all); n != 4 {
		t.Fatalf("%d cache breakpoints: %s", n, u.body)
	}
	last := all.(map[string]any)["messages"].([]any)[2].(map[string]any)["content"].([]any)
	if last[0].(map[string]any)["cache_control"] != nil || last[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the last turn's breakpoint: %v", last)
	}

	// a request already ZCode's, and one with a reminder of its own, keep it
	again := zcodeStartBody(u.body, time.Now())
	if !bytes.Equal(again, u.body) {
		t.Fatalf("shaped twice:\n%s\n%s", u.body, again)
	}
	own := `{"model":"GLM-5.2","system":"mine","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"},{"type":"text","text":"hi"}]}]}`
	var o struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(zcodeStartBody([]byte(own), time.Now()), &o)
	if len(o.System) != 4 || o.System[3]["text"] != "mine" || len(o.Messages[0].Content) != 2 {
		t.Fatalf("a string system and its own reminder: %+v", o)
	}
	// what isn't a messages request is left alone
	for _, b := range []string{`not json`, `{"model":"GLM-5.2"}`} {
		if got := zcodeStartBody([]byte(b), time.Now()); string(got) != b {
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
