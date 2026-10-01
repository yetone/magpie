package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// ccRelayReply is an Anthropic stream saying one word.
var ccRelayReply = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":5}}}`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"searched"}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
	`data: {"type":"message_stop"}`)

// ccOnly refuses what a relay that serves only Claude Code refuses (#359:
// sub2api's claude_code_only groups): a request without Claude Code's
// metadata.user_id.
func ccOnly(body []byte) (int, string) {
	var q struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &q) != nil || q.Metadata.UserID == "" {
		return 403, `{"type":"error","error":{"type":"permission_error","message":"This group is restricted to the official Claude Code client."}}`
	}
	return 0, ""
}

// Claude Code's WebSearch (#359) on an Anthropic relay that doesn't search
// by itself, as magpie sees it, is built again rather than relayed — with
// magpie's search when a provider that searches is set up, without the
// server tool when none is — and still carries Claude Code's metadata, as
// a relay that serves only Claude Code checks for it.
func TestClaudeCodeWebSearchKeepsMetadata(t *testing.T) {
	const userID = `{"device_id":"d1","account_uuid":"","session_id":"s1"}`
	ask := func(t *testing.T, model string) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 100, "stream": true,
			"system":   []map[string]any{{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}, {"type": "text", "text": "You are an assistant for performing a web search tool use"}},
			"metadata": map[string]any{"user_id": userID},
			"messages": []map[string]any{{"role": "user", "content": "Perform a web search for the query: latest go"}},
			"tools":    []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": 8}},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(string(body)))
		req.Header.Set("User-Agent", "claude-cli/2.1.285 (external, cli)")
		req.Header.Set("X-App", "cli")
		req.Header.Set("Anthropic-Beta", "claude-code-20250219")
		New().Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	check := func(t *testing.T, f *fake) {
		t.Helper()
		var got struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		json.Unmarshal(f.got, &got)
		if got.Metadata.UserID != userID {
			t.Errorf("metadata lost: %s", f.got)
		}
		if f.head.Get("User-Agent") != "claude-cli/2.1.285 (external, cli)" || f.head.Get("X-App") != "cli" {
			t.Errorf("Claude Code's headers lost: %v", f.head)
		}
	}

	t.Run("no searcher", func(t *testing.T) {
		f := &fake{t: t, reply: ccRelayReply, refuse: ccOnly}
		setHome(t, t.TempDir()) // no signed-in agent searches
		setup(t, provider.Anthropic, f)
		if _, _, ok := searcher(); ok {
			t.Fatal("a searcher is set up")
		}
		if code, body := ask(t, "fake/m1"); code != 200 || !strings.Contains(body, "searched") {
			t.Fatalf("%d %s", code, body)
		}
		check(t, f)
		if strings.Contains(string(f.got), "web_search_20250305") {
			t.Errorf("server tool sent to a relay magpie doesn't know searches: %s", f.got)
		}
	})

	t.Run("magpie searches", func(t *testing.T) {
		f := &fake{t: t, reply: ccRelayReply, refuse: ccOnly}
		setHome(t, t.TempDir())
		setup(t, provider.Anthropic, f)
		srch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5"}]}`)
		}))
		defer srch.Close()
		hosts := searchHosts[provider.Anthropic]
		searchHosts[provider.Anthropic] = append(hosts, provider.HostOf(srch.URL))
		defer func() { searchHosts[provider.Anthropic] = hosts }()
		if err := provider.Save(provider.Provider{ID: "srch", Name: "Search", Key: "k", Anthropic: srch.URL}); err != nil {
			t.Fatal(err)
		}
		p, _ := provider.Find("srch")
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if sp, _, ok := searcher(); !ok || sp.ID != "srch" {
			t.Fatalf("searcher %v %v", sp.ID, ok)
		}
		if code, body := ask(t, "fake/m1"); code != 200 || !strings.Contains(body, "searched") {
			t.Fatalf("%d %s", code, body)
		}
		check(t, f)
		if !strings.Contains(string(f.got), `"name":"web_search"`) {
			t.Errorf("magpie's search not offered: %s", f.got)
		}
	})

	t.Run("bedrock", func(t *testing.T) {
		// Bedrock still never gets it (#176), built again or not
		fresh(t)
		up := &bedrock{}
		srv := httptest.NewServer(up)
		t.Cleanup(srv.Close)
		p, err := provider.FromPreset("bedrock")
		if err != nil {
			t.Fatal(err)
		}
		p.ID, p.Key = "fake", "ABSK-test"
		p.Anthropic, p.Chat = srv.URL+"/anthropic", srv.URL+"/openai/v1"
		p.Models = []string{"apac.anthropic.claude-opus-5-5"}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if code, body := ask(t, "fake/apac.anthropic.claude-opus-5-5"); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		c := up.last()
		if c.path != "/anthropic/v1/messages" {
			t.Fatalf("upstream %s", c.path)
		}
		if _, ok := c.body["metadata"]; ok {
			t.Errorf("metadata sent to Bedrock: %v", c.body)
		}
	})
}
