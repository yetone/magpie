package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A client that speaks Messages itself (ZCode) sends its tools as they
// came, and one of them, an MCP server's, has a oneOf at its
// input_schema's root, which Anthropic's API refuses for every request
// that offers it ("input_schema does not support oneOf, allOf, or anyOf at
// the top level"): Claude behind Zed's subscription failed even "reply
// OK" (ziko on X, 2026-11-04). Passed through to an Anthropic-protocol
// upstream, such a tool goes folded to an object root from the first
// request, as a request translated to Messages always went; the other
// tools' schemas go as they came.
func TestMessagesRootUnionFolded(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var got [][]byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
		var q struct {
			Tools []map[string]any `json:"tools"`
		}
		json.Unmarshal(b, &q)
		for i, tl := range q.Tools {
			ps, _ := tl["input_schema"].(map[string]any)
			if ps != nil && (ps["oneOf"] != nil || ps["anyOf"] != nil || ps["allOf"] != nil) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"tools.`+string(rune('0'+i))+`.custom.input_schema: input_schema does not support oneOf, allOf, or anyOf at the top level"}}`)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5\",\"content\":[],\"usage\":{\"input_tokens\":9,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	plainSchema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	plain := `{"name":"read","description":"Read a file","input_schema":` + plainSchema + `}`
	union := `{"name":"goanyapi_adsense","description":"AdSense reports","input_schema":{"oneOf":[` +
		`{"type":"object","properties":{"action":{"const":"report"},"range":{"type":"string"}},"required":["action","range"]},` +
		`{"type":"object","properties":{"action":{"const":"accounts"}},"required":["action"]}]}}`
	ask := func(tools string) map[string]map[string]any {
		t.Helper()
		n := len(got)
		rec := httptest.NewRecorder()
		body := `{"model":"relay/claude-sonnet-5","max_tokens":64,"stream":true,"tools":[` + tools + `],"messages":[{"role":"user","content":"reply OK"}]}`
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "OK") {
			t.Fatalf("answered %d %s", rec.Code, rec.Body)
		}
		if len(got) != n+1 {
			t.Fatalf("upstream asked %d times, want once, folded from the first", len(got)-n)
		}
		var q struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(got[n], &q); err != nil {
			t.Fatal(err)
		}
		by := map[string]map[string]any{}
		for _, tl := range q.Tools {
			name, _ := tl["name"].(string)
			ps, _ := tl["input_schema"].(map[string]any)
			by[name] = ps
		}
		return by
	}
	same := func(ps map[string]any, want string) bool {
		var w any
		json.Unmarshal([]byte(want), &w)
		a, _ := json.Marshal(ps)
		b, _ := json.Marshal(w)
		return bytes.Equal(a, b)
	}

	by := ask(plain + `,` + union)
	ps := by["goanyapi_adsense"]
	props, _ := ps["properties"].(map[string]any)
	if ps["type"] != "object" || ps["oneOf"] != nil || props["action"] == nil || props["range"] == nil {
		t.Fatalf("union sent as %v", ps)
	}
	if !same(by["read"], plainSchema) {
		t.Fatalf("a tool with no union sent as %v", by["read"])
	}

	// no union anywhere: every schema goes as the client wrote it
	if by = ask(plain); !same(by["read"], plainSchema) {
		t.Fatalf("no union: sent as %v", by["read"])
	}
}
