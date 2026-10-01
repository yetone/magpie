package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// autoModeAsk is Claude Code's auto mode classifier's first stage as it
// sends it: its prompt asking for <block>yes</block> or <block>no</block>,
// the conversation so far as a user message, the transcript in one of its
// own, no tools, thinking off and 64 tokens to answer in (#250).
func autoModeAsk(model string) string {
	return `{"model":"` + model + `","max_tokens":64,"temperature":1,"stream":true,
	  "system":[{"type":"text","text":"You are a security monitor for an autonomous coding agent.\n## Output Format\nAnswer <block>yes</block><category>…</category><reason>…</reason> to block the action, else <block>no</block>."}],
	  "thinking":{"type":"disabled"},
	  "stop_sequences":["</block>"],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"The user's settings allow reading and editing files in the project."}]},
	    {"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"User: tidy up the build\nAction: Bash rm -rf build/\n"},{"type":"text","text":"</transcript>\n"}]}]}`
}

// saidNo reports whether a reply's text is the classifier's "no", however
// its JSON escapes the tag.
func saidNo(body string) bool {
	return strings.Contains(body, "<block>no") || strings.Contains(body, "u003cblock") && strings.Contains(body, "u003eno")
}

// A model that reasons whatever it is told spends the classifier's 64
// tokens reasoning and answers nothing, which Claude Code takes for no
// verdict and blocks the action: through a translated API the classifier
// gets room to answer in and the least reasoning the model has.
func TestAutoModeClassifierOnAReasoningModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		proto  provider.Protocol
		reply  string
		levels []string
		tokens string // the field the budget goes in
		effort func(up map[string]any) any
		want   any
	}{{
		name:  "responses",
		proto: provider.Responses,
		reply: sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"<block>no"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`),
		levels: []string{"none", "low", "medium", "high"},
		tokens: "max_output_tokens",
		effort: func(up map[string]any) any {
			r, _ := up["reasoning"].(map[string]any)
			return r["effort"]
		},
		want: "none",
	}, {
		name:  "chat",
		proto: provider.Chat,
		reply: sse(
			`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"<block>no"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`),
		levels: []string{"low", "medium", "high"},
		tokens: "max_tokens",
		effort: func(up map[string]any) any { return up["reasoning_effort"] },
		want:   "low",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t, reply: tc.reply}
			up := setup(t, tc.proto, f)
			if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 128000, Efforts: tc.levels}}); err != nil {
				t.Fatal(err)
			}
			code, body := post(t, "/v1/messages", autoModeAsk("m1"))
			if code != 200 || !saidNo(body) {
				t.Fatalf("%d %s", code, body)
			}
			var asked map[string]any
			json.Unmarshal(f.got, &asked)
			if n, _ := asked[tc.tokens].(float64); n < 64+classifierRoom {
				t.Errorf("%s = %v, want room to reason and answer: %s", tc.tokens, asked[tc.tokens], f.got)
			}
			if got := tc.effort(asked); got != tc.want {
				t.Errorf("effort %v, want %v: %s", got, tc.want, f.got)
			}
		})
	}
}

// With its levels unknown, the reasoning is left to the vendor, but the
// room is given all the same; a conversation's own turn is left alone.
func TestAutoModeClassifierOnlyTheClassifier(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	if code, body := post(t, "/v1/messages", autoModeAsk("m1")); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var asked map[string]any
	json.Unmarshal(f.got, &asked)
	if asked["max_output_tokens"] != float64(64+classifierRoom) || asked["reasoning"] != nil {
		t.Errorf("unknown levels: %s", f.got)
	}
	if code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":64,"thinking":{"type":"disabled"},"system":"be brief","messages":[{"role":"user","content":"a title for this"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	json.Unmarshal(f.got, &asked)
	if asked["max_output_tokens"] != float64(64) {
		t.Errorf("a title ask was changed: %s", f.got)
	}
}

// A vendor's model behind an Anthropic API (GLM, Kimi, MiniMax…) gets the
// room too; the rest of the request goes as it was sent.
func TestAutoModeClassifierAnthropicVendor(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<block>no"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	if code, body := post(t, "/v1/messages", autoModeAsk("m1")); code != 200 || !saidNo(body) {
		t.Fatalf("%d %s", code, body)
	}
	var asked map[string]any
	json.Unmarshal(f.got, &asked)
	if asked["max_tokens"] != float64(64+classifierRoom) {
		t.Errorf("max_tokens %v: %s", asked["max_tokens"], f.got)
	}
	if s, _ := asked["stop_sequences"].([]any); len(s) != 1 || s[0] != "</block>" || asked["temperature"] != float64(1) {
		t.Errorf("the rest changed: %s", f.got)
	}
}

// Anthropic's API reviews the actions itself when asked: the
// dangerous-tool-use beta and the safeguards field go to it as Claude Code
// sent them, and safeguard_results come back as it answered them. (This
// passes on main too; it guards the classifier's changes from reaching a
// Claude model.)
func TestAutoModeServerSideReviewRelayed(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":7}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01Abc","name":"Bash","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","safeguard_results":[{"type":"dangerous_tool_use","tool_uses":[{"tool_use_id":"toolu_01Abc","status":"evaluated","result":"not_flagged"}]}]},"usage":{"output_tokens":4}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"ant/claude-sonnet-5","max_tokens":64,"stream":true,
	  "safeguards":[{"type":"dangerous_tool_use","classifier_context":"The user allows file edits in the project."}],
	  "tools":[{"name":"Bash","input_schema":{"type":"object"}}],
	  "messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"list the files"}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/2.1.285 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,dangerous-tool-use-2026-09-03")
	req.Header.Set("anthropic-version", "2023-06-01")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(f.head.Get("anthropic-beta"), "dangerous-tool-use-2026-09-03") {
		t.Errorf("beta not forwarded: %q", f.head.Get("anthropic-beta"))
	}
	var asked map[string]any
	json.Unmarshal(f.got, &asked)
	sg, _ := asked["safeguards"].([]any)
	if len(sg) != 1 || sg[0].(map[string]any)["classifier_context"] != "The user allows file edits in the project." || asked["max_tokens"] != float64(64) {
		t.Errorf("request changed: %s", f.got)
	}
	var delta map[string]any
	for _, e := range events(rec.Body.String()) {
		if e["type"] == "message_delta" {
			delta, _ = e["delta"].(map[string]any)
		}
	}
	res, _ := delta["safeguard_results"].([]any)
	if len(res) != 1 || !strings.Contains(rec.Body.String(), `"tool_use_id":"toolu_01Abc"`) {
		t.Errorf("safeguard_results not relayed: %s", rec.Body)
	}
}

// On a Claude subscription each classifier call runs a Claude Code of its
// own: it runs at low effort, to answer within Claude Code's minute, and
// isn't kept waiting for a next turn it will never have, which would push
// the conversations' own runs out.
func TestAutoModeClassifierOnTheClaudeBridge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := `#!/bin/sh
echo "$@" >> '` + args + `'
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<block>no"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for i := 0; i < 3; i++ {
		body := autoModeAsk("claude-sonnet-5")
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		if !saidNo(rec.Body.String()) {
			t.Fatalf("no verdict: %s", rec.Body)
		}
	}
	s.subscription.mu.Lock()
	n := len(s.subscription.idle)
	s.subscription.mu.Unlock()
	if n != 0 {
		t.Errorf("%d classifier runs left waiting, want 0", n)
	}
	b, _ := os.ReadFile(args)
	runs := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(runs) != 3 {
		t.Fatalf("%d runs: %s", len(runs), b)
	}
	for _, r := range runs {
		if !strings.Contains(" "+r+" ", " --effort low ") {
			t.Errorf("run not at low effort: %s", r)
		}
	}
}
