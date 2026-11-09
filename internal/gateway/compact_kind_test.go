package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// ccCompactAsk is the last user text of Claude Code's compaction as
// 2.1.316 writes it (PZ: its CRITICAL preamble, then ITe's ask), cut
// short after the ask's first lines.
const ccCompactAsk = "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\n" +
	"- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.\n" +
	"- You already have all the context you need in the conversation above.\n" +
	"- Tool calls will be REJECTED and will waste your only turn — you will fail the task.\n" +
	"- Your entire response must be plain text: an <analysis> block followed by a <summary> block.\n\n" +
	"Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.\n" +
	"This summary should be thorough in capturing technical details, code patterns, and architectural decisions"

// An agent compacting its conversation through the gateway is recorded
// as a compaction (Kind "compact", the Usage page's Compaction purpose),
// on every route a client can send it by, as Codex's are. Claude Code's
// compaction is a call its transcript never records (skipTranscript), so
// the gateway's row is the only place it is counted; recorded as a plain
// turn, it read as no compaction at all (shangyuan0818 on Discord), and
// the OTel export left it to a transcript that doesn't have it.
func TestCompactionRecordedAsCompaction(t *testing.T) {
	ccSystem := `[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.316; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}]`
	tools := `[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]`
	ask, _ := json.Marshal(ccCompactAsk)
	for _, tc := range []struct {
		name, path, body string
		compact          bool
	}{
		// the cache-sharing fork (tengu_compact_cache_prefix, on by
		// default): the turn's own system prompt and tools, the ask put
		// after the last tool result
		{"claude code fork", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":20000,"system":` + ccSystem + `,"tools":` + tools + `,"messages":[` +
			`{"role":"user","content":"fix the failing test"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},{"type":"text","text":` + string(ask) + `}]}]}`, true},
		// its fallback: a system prompt of its own, no tools
		{"claude code plain", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":20000,"system":[{"type":"text","text":"You are a helpful AI assistant tasked with summarizing conversations."}],"messages":[` +
			`{"role":"user","content":"fix the failing test"},{"role":"assistant","content":"fixed"},{"role":"user","content":[{"type":"text","text":` + string(ask) + `}]}]}`, true},
		{"chat", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"system","content":"You are a context summarization agent. You are given a conversation between a user and an agent."},{"role":"user","content":"fix it"}]}`, true},
		{"responses", "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":` + quote(codexCompactPrompt) + `}]}`, true},
		{"turn", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":300,"system":` + ccSystem + `,"tools":` + tools + `,"messages":[{"role":"user","content":"fix the failing test"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(&paced{events: copilotThinkingReply})
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Anthropic: up.URL, Key: "k", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
			req.Header.Set("X-Claude-Code-Session-Id", "cc-session")
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			u := lastUsage(t)
			want := ""
			if tc.compact {
				want = "compact"
			}
			if u.Kind != want || u.Output != 1277 {
				t.Fatalf("recorded as kind %q with %d output tokens, want kind %q", u.Kind, u.Output, want)
			}
			rows, _, _ := usage.Ledger(usage.All, usage.Filter{Purpose: usage.PurposeOf("compact")})
			if tc.compact != (len(rows) == 1) {
				t.Fatalf("the Compaction purpose lists %d rows", len(rows))
			}
		})
	}
}
