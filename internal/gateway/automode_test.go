package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/wslrun"
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

// With its levels unknown, the room is given all the same, and the model
// asked for none (TestAutoModeClassifierReasonsLeastOnAnUnknownModel); a
// conversation's own turn is left alone.
func TestAutoModeClassifierOnlyTheClassifier(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"<block>no"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	if code, body := post(t, "/v1/messages", autoModeAsk("m1")); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var asked map[string]any
	json.Unmarshal(f.got, &asked)
	if r, _ := asked["reasoning"].(map[string]any); asked["max_output_tokens"] != float64(64+classifierRoom) || r["effort"] != "none" {
		t.Errorf("unknown levels: %s", f.got)
	}
	if code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":64,"thinking":{"type":"disabled"},"system":"be brief","messages":[{"role":"user","content":"a title for this"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	asked = nil
	json.Unmarshal(f.got, &asked)
	if asked["max_output_tokens"] != float64(64) || asked["reasoning"] != nil {
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

// Claude Code 2.1.295's ask for the review, as it sent it in auto mode
// (paths shortened): the beta beside its others, and the safeguards with
// their classifier_context, an object.
const (
	reviewBetas = "claude-code-20250219,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,dangerous-tool-use-2026-09-03,afk-mode-2026-01-31"
	reviewAsked = `[{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto","platform":"macos","live_cwd":"/Users/lc/work","home_dir":"/Users/lc","rule_roots":{"userSettings":"/Users/lc/.claude","projectSettings":"/Users/lc/work"},"trusted_directories":{"primary":{"path":"/Users/lc/work","resolved":["/Users/lc/work"]},"additional":[],"network":[],"block_reads_outside_working_directories":false},"rules":{"allow":[],"deny":[],"ask":[]},"auto_mode":{"allow":[],"soft_deny":[],"hard_deny":[],"environment":[]},"restricted":false,"is_remote_mode":false,"user_identity":null,"git_state":{"cwd":"/Users/lc/work","root":null,"branch":null,"error":"not_a_repo"},"prior_turn_context":[{"tool_use_ids":["toolu_01Capture001"],"context":{"live_cwd":"/Users/lc/work","platform":"macos"}}]}}]`
	// the verdict, as Claude Code 2.1.295 parses it
	reviewed = `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_01Abc":{"type":"evaluated","outcome":"not_flagged"}}}}]`
)

// reviewAsk is a turn of Claude Code's in auto mode, asking the review.
func reviewAsk(model string) string {
	return `{"model":"` + model + `","max_tokens":64000,"stream":true,
	  "safeguards":` + reviewAsked + `,
	  "tools":[{"name":"Bash","description":"Run a shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],
	  "messages":[{"role":"user","content":[{"type":"text","text":"Run the shell command: echo hi"}]}]}`
}

// reviewReply is a streamed call to Bash, with the review's verdict on it.
func reviewReply(model string) string {
	return sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01Abc","name":"Bash","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hi\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"safeguard_results":`+reviewed+`},"usage":{"output_tokens":4}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
}

// askReview sends reviewAsk(model) to s as Claude Code does.
func askReview(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/2.1.295 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", reviewBetas)
	req.Header.Set("anthropic-version", "2023-06-01")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// reviewIn is the safeguard_results of a streamed reply's message_delta,
// where Claude Code reads them.
func reviewIn(t *testing.T, stream string) string {
	t.Helper()
	for _, e := range events(stream) {
		if e["type"] == "message_delta" {
			if d, _ := e["delta"].(map[string]any); d["safeguard_results"] != nil {
				b, _ := json.Marshal(d["safeguard_results"])
				return string(b)
			}
		}
	}
	return ""
}

// Anthropic's API reviews the actions itself when asked: the
// dangerous-tool-use beta and the safeguards field go to it as Claude Code
// sent them, and safeguard_results come back as it answered them, the call's
// id with them as it was. (This passes on main too; it guards the
// classifier's changes from reaching a Claude model.)
func TestAutoModeServerSideReviewRelayed(t *testing.T) {
	f := &fake{t: t, reply: reviewReply("claude-sonnet-5")}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	rec := askReview(t, New(), "/v1/messages?beta=true", reviewAsk("ant/claude-sonnet-5"))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(f.head.Get("anthropic-beta"), "dangerous-tool-use-2026-09-03") {
		t.Errorf("beta not forwarded: %q", f.head.Get("anthropic-beta"))
	}
	var asked struct {
		Safeguards json.RawMessage `json:"safeguards"`
		MaxTokens  int             `json:"max_tokens"`
	}
	json.Unmarshal(f.got, &asked)
	if !sameReview(asked.Safeguards, []byte(reviewAsked)) || asked.MaxTokens != 64000 {
		t.Errorf("request changed: %s", f.got)
	}
	if got := reviewIn(t, rec.Body.String()); !sameReview([]byte(got), []byte(reviewed)) || !strings.Contains(rec.Body.String(), `"id":"toolu_01Abc"`) {
		t.Errorf("safeguard_results not relayed: %s", rec.Body)
	}
}

// sameReview reports whether a and b are the same JSON value.
func sameReview(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// Only Claude reviews actions: another vendor's model behind its Anthropic
// API (DeepSeek's, Kimi's) is sent neither the beta nor the safeguards,
// which a vendor checking what it is sent turns the request away over
// (AiHubMix: 400 "safeguards: Extra inputs are not permitted"). Claude
// Code, given no verdict, classifies locally. The agent's other betas go on,
// and its count_tokens likewise goes without the beta.
func TestAutoModeReviewOnlyForClaude(t *testing.T) {
	f := &fake{t: t}
	f.refuse = func(body []byte) (int, string) {
		if strings.Contains(f.head.Get("anthropic-beta"), "dangerous-tool-use") || strings.Contains(string(body), `"safeguards"`) {
			return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"safeguards: Extra inputs are not permitted"}}`
		}
		return 0, ""
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Anthropic: up.URL, Models: []string{"deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	f.reply = sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-flash","content":[],"usage":{"input_tokens":7}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	if rec := askReview(t, s, "/v1/messages?beta=true", reviewAsk("ds/deepseek-flash")); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(string(f.got), `"safeguards"`) {
		t.Errorf("safeguards sent to DeepSeek: %s", f.got)
	}
	beta := f.head.Get("anthropic-beta")
	if strings.Contains(beta, "dangerous-tool-use") || !strings.Contains(beta, "claude-code-20250219") || !strings.Contains(beta, "afk-mode-2026-01-31") {
		t.Errorf("anthropic-beta to DeepSeek: %q", beta)
	}

	f.ctype, f.reply = "application/json", `{"input_tokens":12}`
	if rec := askReview(t, s, "/v1/messages/count_tokens?beta=true", `{"model":"ds/deepseek-flash","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("count_tokens: %d %s", rec.Code, rec.Body)
	}
	if f.path != "/v1/messages/count_tokens" || strings.Contains(f.head.Get("anthropic-beta"), "dangerous-tool-use") {
		t.Errorf("count_tokens to DeepSeek: %s %q", f.path, f.head.Get("anthropic-beta"))
	}
}

// TestAutoModeReviewedRoutes: the models whose requests can carry auto
// mode's review to Anthropic, the rule Claude Code's wiring turns the
// review off by while none of them is behind magpie (1EFLOPS on X): the
// Claude subscription, a Claude model under any vendor's spelling of it,
// and Anthropic's own API. Codex, DeepSeek's Anthropic API and an
// OpenAI-compatible relay's GPT are not.
func TestAutoModeReviewedRoutes(t *testing.T) {
	sub := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	codex := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "u"}}
	anthropic := provider.Provider{ID: "anthropic", Anthropic: "https://api.anthropic.com", Key: "k"}
	relay := provider.Provider{ID: "or", Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api", Key: "k"}
	deepseek := provider.Provider{ID: "ds", Anthropic: "https://api.deepseek.com/anthropic", Key: "k"}
	// another vendor's subscription or a plugin serves Claude models as
	// its own service: Copilot turns the safeguards away (#1756), Kiro and
	// Cursor translate the request, and none answers with a review
	copilot := provider.Provider{ID: "copilot", Anthropic: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot", User: "u"}}
	kiro := provider.Provider{ID: "kiro", Account: &provider.Account{Agent: "kiro", User: "u"}}
	plugin := provider.Provider{ID: "x-plugin", Account: &provider.Account{Agent: "plugin", User: "u"}}
	pluginOnAnthropic := provider.Provider{ID: "y-plugin", Anthropic: "https://api.anthropic.com", Account: &provider.Account{Agent: "plugin", User: "u"}}
	for _, c := range []struct {
		p     provider.Provider
		model string
		want  bool
	}{
		{sub, "claude-opus-4-8", true},
		{sub, "", true},
		{anthropic, "claude-sonnet-5", true},
		{anthropic, "some-new-name", true},
		{relay, "anthropic/claude-sonnet-5", true},
		{relay, "us.anthropic.claude-opus-4-8-v1:0", true},
		{relay, "claude-opus-4-8@20260101", true},
		{codex, "gpt-6.1-sol", false},
		{deepseek, "deepseek-v4", false},
		{relay, "openai/gpt-6", false},
		{relay, "claudette-7", false},
		{copilot, "claude-sonnet-5", false},
		{kiro, "claude-sonnet-5", false},
		{plugin, "claude-opus-4-8", false},
		{pluginOnAnthropic, "claude-opus-4-8", true},
	} {
		if got := AutoModeReviewed(c.p, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
		}
	}
}

// TestAutoModeReviewableAsksOnMessages: what Claude Code's wiring keeps
// the review on for (nexnull on Discord) is a reviewed model that is asked
// on the Messages API. A Claude model on a relay with only a Chat URL is
// translated, and a translation carries no safeguards and gets no
// results back, so it is not; the Claude subscription, Anthropic's API, a
// relay's or Bedrock's Anthropic endpoint are.
func TestAutoModeReviewableAsksOnMessages(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sub := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	anthropic := provider.Provider{ID: "anthropic", Anthropic: "https://api.anthropic.com", Key: "k"}
	relay := provider.Provider{ID: "or", Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api", Key: "k"}
	chatOnly := provider.Provider{ID: "cr", Chat: "https://relay.example/v1", Key: "k"}
	bedrock := provider.Provider{ID: "bedrock", Preset: "bedrock", Anthropic: "https://bedrock-runtime.us-east-1.amazonaws.com", Key: "k"}
	for _, c := range []struct {
		p     provider.Provider
		model string
		want  bool
	}{
		{sub, "claude-opus-4-8", true},
		{anthropic, "claude-sonnet-5", true},
		{relay, "anthropic/claude-sonnet-5", true},
		{bedrock, "us.anthropic.claude-opus-4-8-v1:0", true},
		{chatOnly, "claude-sonnet-5", false},
		{chatOnly, "deepseek-v4.1-flash", false},
		{relay, "openai/gpt-6", false},
	} {
		if got := AutoModeReviewable(c.p, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
		}
	}
}

// TestAutoModeAnsweredOnlyWhereResultsComeBack: Claude Code 2.1.322 with
// its telemetry off refuses every action whose reply has no
// safeguard_results (JoyYoung2Lucky on X), so the wiring keeps the review
// on only where they surely come back: Anthropic's own API and the Claude
// subscription. A relay or Bedrock is sent the safeguards but may drop the
// results; another vendor's subscription isn't sent them.
func TestAutoModeAnsweredOnlyWhereResultsComeBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sub := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	copilot := provider.Provider{ID: "copilot", Anthropic: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot", User: "u"}}
	anthropic := provider.Provider{ID: "anthropic", Anthropic: "https://api.anthropic.com", Key: "k"}
	relay := provider.Provider{ID: "or", Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api", Key: "k"}
	bedrock := provider.Provider{ID: "bedrock", Preset: "bedrock", Anthropic: "https://bedrock-runtime.us-east-1.amazonaws.com", Key: "k"}
	ds := provider.Provider{ID: "deepseek", Anthropic: "https://api.deepseek.com/anthropic", Key: "k"}
	for _, c := range []struct {
		p     provider.Provider
		model string
		want  bool
	}{
		{sub, "claude-opus-4-8", true},
		{anthropic, "claude-sonnet-5", true},
		{relay, "anthropic/claude-sonnet-5", false},
		{bedrock, "us.anthropic.claude-opus-4-8-v1:0", false},
		{copilot, "claude-sonnet-5", false},
		{ds, "deepseek-v4.1-flash", false},
	} {
		if got := AutoModeAnswered(c.p, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
		}
	}
}

// Claude Code asks for the review on every auto-mode request. A Claude
// model on another vendor's subscription (Copilot here, which answers the
// safeguards with "Extra inputs are not permitted", #1756) is sent neither
// the beta nor the safeguards, so it isn't refused and asked again; the
// Claude subscription and Anthropic's API get both.
func TestAutoModeReviewLeftOffOtherSubscriptions(t *testing.T) {
	s := &Server{}
	asked := []string{"dangerous-tool-use-2026-09-03", "interleaved-thinking-2025-05-14"} // as askedBetas splits them
	body := []byte(`{"model":"claude-sonnet-5","safeguards":[{"type":"dangerous_tool_use"}],"messages":[]}`)
	for _, c := range []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{ID: "copilot", Anthropic: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot", User: "u"}}, false},
		{provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}, true},
		{provider.Provider{ID: "anthropic", Anthropic: "https://api.anthropic.com", Key: "k"}, true},
	} {
		betas, out := s.serverReview(c.p, asked, body)
		beta := slices.ContainsFunc(betas, isServerReviewBeta)
		guards := gjson.GetBytes(out, "safeguards").Exists()
		if beta != c.want || guards != c.want {
			t.Errorf("%s: beta %v, safeguards %v, want %v", c.p.ID, beta, guards, c.want)
		}
		if !slices.Contains(betas, "interleaved-thinking-2025-05-14") {
			t.Errorf("%s: another beta went too: %v", c.p.ID, betas)
		}
	}
}

// A provider that turned the beta away (Bedrock's runtime: Unexpected
// value(s) for the anthropic-beta header) is asked again without it, and
// without the safeguards it turns on, which it would turn away next.
func TestAutoModeReviewGoesWithItsBeta(t *testing.T) {
	f := &fake{t: t, reply: reviewReply("claude-sonnet-5")}
	f.refuse = func(body []byte) (int, string) {
		if strings.Contains(f.head.Get("anthropic-beta"), "dangerous-tool-use") {
			return 400, `{"message":"Unexpected value(s) ` + "`dangerous-tool-use-2026-09-03`" + ` for the ` + "`anthropic-beta`" + ` header. Please consult our documentation at docs.anthropic.com or try again without the header."}`
		}
		if strings.Contains(string(body), `"safeguards"`) {
			return 400, `{"message":"safeguards: Extra inputs are not permitted"}`
		}
		return 0, ""
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for i := range 2 {
		f.calls = 0
		rec := askReview(t, s, "/v1/messages?beta=true", reviewAsk("ant/claude-sonnet-5"))
		if rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body)
		}
		if strings.Contains(string(f.got), `"safeguards"`) || !strings.Contains(f.head.Get("anthropic-beta"), "claude-code-20250219") {
			t.Errorf("turn %d went as %q: %s", i, f.head.Get("anthropic-beta"), f.got)
		}
		if i == 1 && f.calls != 1 {
			t.Errorf("the next turn took %d requests, want 1", f.calls)
		}
	}
}

// A reply its vendor gave whole goes to Claude Code, which asked a stream,
// as the stream would have said it: the verdict in the message_delta,
// where Claude Code reads it, not the message_start, where it finds no
// result and leaves the review for the session.
func TestAutoModeReviewOfAWholeReplyStreamed(t *testing.T) {
	whole := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_01Abc","name":"Bash","input":{"command":"echo hi"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":4},"safeguard_results":` + reviewed + `}`
	rec := httptest.NewRecorder()
	if !wholeAsStream(rec, provider.Anthropic, []byte(whole)) {
		t.Fatal("not a reply")
	}
	if got := reviewIn(t, rec.Body.String()); !sameReview([]byte(got), []byte(reviewed)) {
		t.Errorf("verdict not in message_delta: %s", rec.Body)
	}
	for _, e := range events(rec.Body.String()) {
		if m, _ := e["message"].(map[string]any); e["type"] == "message_start" && m["safeguard_results"] != nil {
			t.Errorf("verdict in message_start: %v", e)
		}
	}
}

// A turn of Claude Code's translated to an Anthropic endpoint (the
// candidates' loop, not relayed) asks Claude the review and gives Claude
// Code its verdict; another vendor's model isn't asked it.
func TestAutoModeReviewTranslated(t *testing.T) {
	req, err := parseAnthropic([]byte(reviewAsk("claude-sonnet-5")))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Safeguards json.RawMessage `json:"safeguards"`
	}
	json.Unmarshal(buildAnthropic(req, "claude-sonnet-5"), &out)
	if !sameReview(out.Safeguards, []byte(reviewAsked)) {
		t.Errorf("safeguards to Claude: %s", out.Safeguards)
	}
	if b := buildAnthropic(req, "deepseek-flash"); strings.Contains(string(b), `"safeguards"`) {
		t.Errorf("safeguards to DeepSeek: %s", b)
	}

	rec := httptest.NewRecorder()
	enc := &anthropicEncoder{w: newSSEWriter(rec), model: "claude-sonnet-5"}
	dec := decoder(provider.Anthropic)
	for _, e := range events(reviewReply("claude-sonnet-5")) {
		b, _ := json.Marshal(e)
		if err := dec(string(b), enc.event); err != nil {
			t.Fatal(err)
		}
	}
	enc.finish()
	if got := reviewIn(t, rec.Body.String()); !sameReview([]byte(got), []byte(reviewed)) || !strings.Contains(rec.Body.String(), `"id":"toolu_01Abc"`) {
		t.Errorf("verdict not given back: %s", rec.Body)
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
	testenv.Program(t, filepath.Join(dir, "claude"), script)
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
		// thinking off as it asked: on, Claude Code thought thousands of
		// output tokens before each 64-token verdict (0xAncientTwo)
		if !strings.Contains(" "+r+" ", " --thinking disabled ") {
			t.Errorf("run thinks: %s", r)
		}
	}
}

// The subscription bridge keeps the API's verdict opaque, including a denial,
// skipped/unavailable calls and future fields. Missing results stay missing.
func TestClaudeBridgeSafeguardResults(t *testing.T) {
	for _, verdict := range []string{
		`[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_client":{"type":"evaluated","outcome":"flagged","explanation":"dangerous"}}},"future":{"x":1}}]`,
		`[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_client":{"type":"evaluated","outcome":"not_flagged"}}}}]`,
		`[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_client":{"type":"skipped"}}}}]`,
		`[{"type":"dangerous_tool_use","status":{"type":"unavailable","reason":"error"}}]`,
		`[{"type":"future_check","status":{"type":"unknown"}}]`,
		`null`,
	} {
		for _, whole := range []bool{false, true} {
			name := "stream "
			if whole {
				name = "whole "
			}
			t.Run(name+verdict, func(t *testing.T) {
				ch := make(chan Event, 16)
				run := &subscriptionRun{segment: ch}
				delta := map[string]any{"stop_reason": "tool_use"}
				if verdict != "null" {
					delta["safeguard_results"] = json.RawMessage(verdict)
				}
				d, _ := json.Marshal(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": delta}})
				lines := []string{
					`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_client","model":"claude-sonnet-5"}}}`,
					`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_client","name":"mcp__magpie__Bash"}}}`,
					string(d),
					`{"type":"stream_event","event":{"type":"message_stop"}}`,
				}
				if whole {
					message := map[string]any{"id": "msg_whole", "model": "claude-sonnet-5", "stop_reason": "tool_use",
						"content": []any{map[string]any{"type": "tool_use", "id": "toolu_client", "name": "mcp__magpie__Bash", "input": map[string]any{}}}}
					if verdict != "null" {
						message["safeguard_results"] = json.RawMessage(verdict)
					}
					d, _ := json.Marshal(map[string]any{"type": "assistant", "message": message})
					lines = append(append([]string{}, cutAttempt...), string(d))
				}
				run.readOutput(strings.NewReader(strings.Join(lines, "\n")))
				rec := httptest.NewRecorder()
				enc := encoder(provider.Anthropic, newSSEWriter(rec), &Request{Model: "claude-sonnet-5"})
				var col collector
				for e := range ch {
					enc.event(e)
					col.add(e)
				}
				enc.finish()
				result := col.finish()
				var response map[string]any
				json.Unmarshal(renderAnthropic(result, "claude-sonnet-5"), &response)
				for _, e := range events(rec.Body.String()) {
					if e["type"] == "message_delta" {
						delta := e["delta"].(map[string]any)
						actual, _ := json.Marshal(delta["safeguard_results"])
						var wantValue any
						json.Unmarshal([]byte(verdict), &wantValue)
						want, _ := json.Marshal(wantValue)
						if string(actual) != string(want) {
							t.Fatalf("stream verdict changed: %s != %s", actual, want)
						}
					}
				}
				actual, _ := json.Marshal(response["safeguard_results"])
				var wantValue any
				json.Unmarshal([]byte(verdict), &wantValue)
				want, _ := json.Marshal(wantValue)
				if string(actual) != string(want) {
					t.Fatalf("non-stream verdict changed: %s != %s", actual, want)
				}
				if !strings.Contains(rec.Body.String(), `"id":"toolu_client"`) || !strings.Contains(string(renderAnthropic(result, "claude-sonnet-5")), `"id":"toolu_client"`) {
					t.Fatal("tool ID changed")
				}
			})
		}
	}
}

// Exercise the actual subprocess/control wire: changed and removed contexts
// reach an existing run; a rejected update restarts it with the new context.
func TestClaudeBridgeSafeguardContextUpdates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Python script stands in for Claude Code")
	}
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
body=json.loads(os.environ.get('CLAUDE_CODE_EXTRA_BODY','{}'))
effort=sys.argv[sys.argv.index('--effort')+1] if '--effort' in sys.argv else ''
effort_id=''
turns=0
def send(x): print(json.dumps(x),flush=True)
for line in sys.stdin:
 x=json.loads(line)
 if x['type']=='control_request':
  if x['request']['subtype']=='initialize':
   send({'type':'control_response','response':{'request_id':x['request_id'],'subtype':'success'}})
   continue
  settings=x['request']['settings']
  if 'effortLevel' in settings:
   effort=settings['effortLevel']
   effort_id=x['request_id']
   continue
  if effort_id:
   send({'type':'control_response','response':{'request_id':effort_id,'subtype':'success'}})
  fresh=json.loads(x['request']['settings']['env']['CLAUDE_CODE_EXTRA_BODY'])
  rejected=turns>0 and 'reject' in json.dumps(fresh)
  if not rejected:body=fresh
  send({'type':'control_response','response':{'request_id':'unrelated','subtype':'success'}})
  send({'type':'control_response','response':{'request_id':x['request_id'],'subtype':'error' if rejected else 'success','error':'probe rejected'}})
  continue
 turns+=1
 send({'type':'stream_event','event':{'type':'message_start','message':{'id':'m','model':'claude-sonnet-5'}}})
 text=str(os.getpid())+' '+effort+' '+json.dumps(body,sort_keys=True)
 send({'type':'stream_event','event':{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':text}}})
 send({'type':'stream_event','event':{'type':'message_delta','delta':{'stop_reason':'end_turn'}}})
 send({'type':'stream_event','event':{'type':'message_stop'}})
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CODE_EXTRA_BODY", `{"model":"must-not-leak"}`)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	messages := []map[string]any{{"role": "assistant", "content": "seed"}, {"role": "user", "content": "hi"}}
	var pid string
	for i, context := range []string{"first", "second", "", "third", "reject"} {
		b := map[string]any{"model": "claude-sonnet-5", "max_tokens": 64, "stream": false, "messages": messages}
		effort := []string{"high", "low"}[i%2]
		b["thinking"] = map[string]any{"type": "adaptive"}
		b["output_config"] = map[string]any{"effort": effort}
		if context != "" {
			b["safeguards"] = []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{"marker": context, "future": []int{1, 2}}}}
		}
		body, _ := json.Marshal(b)
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(body)))
		req.Header.Set("anthropic-beta", "claude-code-20250219,dangerous-tool-use-2026-09-03")
		rec := httptest.NewRecorder()
		var usage Usage
		if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-sonnet-5", body, &usage); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var a struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || len(a.Content) != 1 {
			t.Fatalf("bad reply %s", rec.Body)
		}
		text := a.Content[0].Text
		parts := strings.SplitN(text, " ", 3)
		if len(parts) != 3 {
			t.Fatalf("no pid/context: %s", text)
		}
		if parts[1] != effort {
			t.Fatalf("stale effort: %s, want %s", parts[1], effort)
		}
		if pid != "" && (parts[0] == pid) == (context == "reject") {
			t.Fatalf("wrong reuse for %q: %s => %s", context, pid, parts[0])
		}
		pid = parts[0]
		var extra map[string]json.RawMessage
		if err := json.Unmarshal([]byte(parts[2]), &extra); err != nil {
			t.Fatal(err)
		}
		expected := safeguardBody(nil)
		if v := b["safeguards"]; v != nil {
			raw, _ := json.Marshal(v)
			expected = safeguardBody(raw)
		}
		got, _ := json.Marshal(extra)
		if string(got) != expected {
			t.Fatalf("stale/modified context: %s != %s", got, expected)
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": text}, map[string]any{"role": "user", "content": "next"})
	}
}

// The beta must reach a Claude Code running in WSL as well as a native run.
func TestClaudeBridgeSafeguardWSLEnv(t *testing.T) {
	cli := claudeCLI{wsl: &wslrun.Tool{}}
	env := cli.env([]string{"ANTHROPIC_BETAS=dangerous-tool-use-2026-09-03"}, "")
	if !slices.Contains(env, "ANTHROPIC_BETAS=dangerous-tool-use-2026-09-03") ||
		!slices.Contains(env, "WSLENV=ANTHROPIC_BETAS") {
		t.Fatalf("safety beta not forwarded to WSL: %v", env)
	}
}

// A context update and its tool-result handoff own the run together, through
// the reply's end. Another caller cannot update it or abort its owner: it is
// a conversation of its own from there (a fork sub-agent of the same lead),
// and gets a run of its own, not a 409 (ylorn on Discord).
func TestClaudeBridgeSafeguardConcurrentHandoff(t *testing.T) {
	s := New()
	b := s.subscription
	input, stdin := io.Pipe()
	stdout, output := io.Pipe()
	run := &subscriptionRun{bridge: b, token: "tok", stdin: stdin, safeguardBeta: "dangerous-tool-use-2026-09-03",
		pending: map[string]chan mcpToolResult{"a": make(chan mcpToolResult, 1), "b": make(chan mcpToolResult, 1)}}
	b.runs[run.token], b.calls["a"], b.calls["b"] = run, run, run
	t.Cleanup(func() { b.abortAll(); stdin.Close(); input.Close(); output.Close(); stdout.Close() })
	go run.readOutput(stdout)
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			var ctl struct {
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(scanner.Bytes(), &ctl) == nil {
				fmt.Fprintf(output, "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":%q}}\n", ctl.RequestID)
			}
		}
	}()
	req := &Request{SafeguardBeta: run.safeguardBeta, Safeguards: json.RawMessage(`[{"context":"A"}]`),
		Messages: []Message{{Role: "assistant"}, {Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "a", Text: "A"}}}}}
	found, results := b.findRun(req)
	if found != run || !found.claimResume() {
		t.Fatal("first claim failed")
	}
	if err := run.setSafeguards(req); err != nil {
		t.Fatal(err)
	}
	run.endSegment() // a late end with no attached segment cannot release A
	// B arrives after A's ACK and before its handoff, then again while A's
	// reply is active, using another call that still maps to the same run.
	blocked := func(id string) {
		t.Helper()
		body := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"safeguards":[{"context":"B"}],"messages":[{"role":"assistant","content":"call"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"B"}]}]}`)
		done := make(chan int, 1)
		var started atomic.Bool
		go func() {
			rec := httptest.NewRecorder()
			code, _ := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", req.Model, "", body, &Usage{},
				func(context.Context, *Request) (*subscriptionRun, <-chan Event, error) {
					started.Store(true)
					return nil, nil, fmt.Errorf("a run of its own")
				})
			done <- code
		}()
		select {
		case code := <-done:
			if code == 409 || !started.Load() || run.closed || string(run.safeguards) != string(req.Safeguards) {
				t.Fatalf("second caller: status %d, own run %v, closed %v, context %s", code, started.Load(), run.closed, run.safeguards)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("second caller was held on the claimed run")
		}
	}
	blocked("a")
	waiter := run.pending["a"]
	segment, err := run.continueWith(results, "", nil)
	if err != nil || (<-waiter).Content[0]["text"] != "A" {
		t.Fatalf("first handoff: %v", err)
	}
	blocked("b")
	fmt.Fprintln(output, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"m"}}}`)
	fmt.Fprintln(output, `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"}}}`)
	fmt.Fprintln(output, `{"type":"stream_event","event":{"type":"message_stop"}}`)
	for range segment {
	}
	req.Messages[1].Parts[0].CallID = "b"
	if next, _ := b.findRun(req); next != run || !next.claimResume() {
		t.Fatal("claim not released at segment end")
	}
}

// classifierAsk is auto mode's first stage as Claude Code 2.1.310 sends
// it to a model it doesn't know: no stream (a sideQuery), stopping at
// </block>, thinking turned off and 64 tokens, or (thinking left out)
// 64 and 2048 more for a model that can't turn it off.
func classifierAsk(model string, off bool) string {
	thinking, tokens := `"thinking":{"type":"disabled"},`, "64"
	if !off {
		thinking, tokens = "", "2112"
	}
	return `{"model":"` + model + `","max_tokens":` + tokens + `,"temperature":0,` + thinking + `
	  "system":[{"type":"text","text":"You are a security monitor for an autonomous coding agent.\n## Output Format\nAnswer <block>yes</block> to block the action, else <block>no</block>."}],
	  "stop_sequences":["</block>"],
	  "messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\nUser: tidy up the build\nAction: Bash rm -rf build/\n</transcript>\n"}]}]}`
}

// classifierUpstream is a fake upstream for proto answering the
// classifier's "no".
func classifierUpstream(t *testing.T, proto provider.Protocol) *fake {
	if proto == provider.Responses {
		return &fake{t: t, reply: sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"gpt6.1-sol","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"<block>no"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"gpt6.1-sol","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`)}
	}
	return &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"gpt6.1-sol","choices":[{"delta":{"role":"assistant","content":"<block>no"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
}

// askedEffort is the reasoning effort an upstream request for proto asked.
func askedEffort(proto provider.Protocol, body []byte) any {
	var up map[string]any
	json.Unmarshal(body, &up)
	if proto == provider.Responses {
		r, _ := up["reasoning"].(map[string]any)
		return r["effort"]
	}
	return up["reasoning_effort"]
}

// classifierProvider saves an OpenAI-compatible relay the user added by
// its address, serving gpt6.1-sol, a model whose levels magpie doesn't
// know, on proto (CyberCaius on Discord: a new-api relay).
func classifierProvider(t *testing.T, proto provider.Protocol, f *fake) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt6.1-sol"}}
	if proto == provider.Responses {
		p.Responses = up.URL + "/v1"
	} else {
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if levels := p.Efforts("gpt6.1-sol"); len(levels) > 0 {
		t.Fatalf("levels known: %v", levels)
	}
}

// askClassifier sends Claude Code's classifier ask to s, as Claude Code does:
// waiting for the whole reply.
func askClassifier(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// Auto mode's classifier waits 60 seconds for its verdict (Claude Code
// 2.1.310, per attempt). A reasoning model whose levels magpie doesn't
// know, asked with no effort, reasoned at its vendor's default over the
// whole policy and transcript, past those 60 seconds, and Claude Code
// closed the request (CyberCaius on Discord: every classifier request
// ended 499 on a new-api relay's gpt6.1-sol). It is asked to reason
// least, whether Claude Code turned thinking off or left it out.
func TestAutoModeClassifierReasonsLeastOnAnUnknownModel(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		for _, off := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/off=%v", proto, off), func(t *testing.T) {
				f := classifierUpstream(t, proto)
				classifierProvider(t, proto, f)
				code, body := askClassifier(t, New(), classifierAsk("relay/gpt6.1-sol", off))
				if code != 200 || !saidNo(body) {
					t.Fatalf("%d %s", code, body)
				}
				if got := askedEffort(proto, f.got); got != "none" {
					t.Errorf("effort %v, want none: %s", got, f.got)
				}
			})
		}
	}
}

// The effort magpie adds is its own ask, not the client's: an upstream
// that takes no none is asked at low, one that takes no reasoning effort
// at all is asked without it, as it was before, and each is remembered,
// so the next classifier ask goes once.
func TestAutoModeClassifierEffortRefusedGoesWithout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		proto  provider.Protocol
		refuse func(effort any) string
		want   any
	}{{
		name:  "levels named",
		proto: provider.Chat,
		refuse: func(e any) string {
			return map[bool]string{true: `{"error":{"message":"Invalid value: 'none'. Supported values are: 'low', 'medium', and 'high'.","param":"reasoning_effort"}}`}[e == "none"]
		},
		want: "low",
	}, {
		name:  "chat field unknown",
		proto: provider.Chat,
		refuse: func(e any) string {
			return map[bool]string{true: `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`}[e != nil]
		},
		want: nil,
	}, {
		name:  "responses field unsupported",
		proto: provider.Responses,
		refuse: func(e any) string {
			return map[bool]string{true: `{"error":{"message":"Unsupported parameter: 'reasoning.effort' is not supported with this model.","param":"reasoning.effort"}}`}[e != nil]
		},
		want: nil,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := classifierUpstream(t, tc.proto)
			f.refuse = func(body []byte) (int, string) {
				if msg := tc.refuse(askedEffort(tc.proto, body)); msg != "" {
					return 400, msg
				}
				return 0, ""
			}
			classifierProvider(t, tc.proto, f)
			s := New()
			for i := range 2 {
				f.calls = 0
				code, body := askClassifier(t, s, classifierAsk("relay/gpt6.1-sol", true))
				if code != 200 || !saidNo(body) {
					t.Fatalf("ask %d: %d %s", i, code, body)
				}
				if got := askedEffort(tc.proto, f.got); got != tc.want {
					t.Errorf("ask %d: effort %v, want %v: %s", i, got, tc.want, f.got)
				}
				if i == 1 && f.calls != 1 {
					t.Errorf("the next ask took %d requests, want 1", f.calls)
				}
			}
		})
	}
}

// CyberCaius on Discord: after a switch in /model, the first auto mode
// classifier request went to the model before it. Claude Code names its
// sonnet tier for the classifier, and that tier followed the old main
// model until magpie moved it. The classifier goes to the model the tier
// follows now (ClassifierStandIn); a turn of the conversation naming the
// same model, another session still on it, goes as asked.
func TestAutoModeClassifierFollowsTheSwitchedModel(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, reply: sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"<block>no"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	p, _ := provider.Find("fake")
	p.Models = []string{"m1", "m2"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	var asked []string
	ClassifierStandIn = func(agent, model string) string {
		asked = append(asked, agent+" "+model)
		if model == "fake/m1" {
			return "fake/m2"
		}
		return ""
	}
	t.Cleanup(func() { ClassifierStandIn = nil })
	send := func(body string) {
		t.Helper()
		f.got = nil
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("User-Agent", "claude-cli/2.1.310 (external, cli)")
		req.Header.Set("x-api-key", Token+"-claude")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	send(autoModeAsk("fake/m1"))
	if got := modelOf(f.got); got != "m2" {
		t.Fatalf("the classifier went to %q, want the switched-to m2 (asked %v)", got, asked)
	}
	send(`{"model":"fake/m1","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`)
	if got := modelOf(f.got); got != "m1" {
		t.Fatalf("a conversation's turn went to %q, want m1 as asked", got)
	}
}
