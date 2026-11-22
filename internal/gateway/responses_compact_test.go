package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7281 (jinpwng): Pi's Responses compaction extension asks POST
// /v1/responses/compact of a provider set to the Responses API. magpie
// answered it the 404 of an unknown path and never asked the upstream.
// From the generic /v1 base URL, and from the bare one, a key's provider
// on Responses is asked once, at /responses/compact under its base, and
// its unary compaction object comes back as it came, the sealed item
// untouched; the next /responses carrying it goes back there (#1099).
func TestResponsesCompactRelayed(t *testing.T) {
	const reply = `{"id":"resp_c1","object":"response.compaction","created_at":1,"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"gAAAAopaque=="}],"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150}}`
	for _, path := range []string{"/v1/responses/compact", "/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			f := &fake{t: t, ctype: "application/json", reply: reply}
			setup(t, provider.Responses, f)
			// an earlier compaction in the input stays the client's: not
			// magpie's summary path (handle's codexCompact)
			code, body := post(t, path, `{"model":"fake/m1","instructions":"summarise",
			  "input":[{"type":"compaction","encrypted_content":"gAAAAearlier=="},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}]}`)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			if f.calls != 1 || f.path != "/v1/responses/compact" {
				t.Fatalf("upstream asked %d times, at %q", f.calls, f.path)
			}
			var got map[string]any
			if err := json.Unmarshal(f.got, &got); err != nil {
				t.Fatalf("upstream got %s", f.got)
			}
			if got["model"] != "m1" || !strings.Contains(string(f.got), "gAAAAearlier==") || strings.Contains(string(f.got), `"stream":true`) {
				t.Errorf("upstream got %s", f.got)
			}
			var out struct {
				Object string `json:"object"`
				Output []struct {
					Type string `json:"type"`
					Enc  string `json:"encrypted_content"`
				} `json:"output"`
			}
			if err := json.Unmarshal([]byte(body), &out); err != nil || out.Object != "response.compaction" || len(out.Output) != 1 || out.Output[0].Enc != "gAAAAopaque==" {
				t.Fatalf("client got %s", body)
			}
			// who sealed it is remembered, for the next /responses
			cp, ok := checkpointOf([]byte(`{"input":[{"type":"compaction","encrypted_content":"gAAAAopaque=="}]}`))
			if !ok || cp.Provider != "fake" {
				t.Errorf("sealed by %+v, %v", cp, ok)
			}
		})
	}
}

// A model that isn't asked on Responses at its provider has no
// /responses/compact: the client is told so in a 400 it can fall back
// on, not a 404, and the upstream is never asked a translation of it.
func TestResponsesCompactRefusedOffResponses(t *testing.T) {
	for _, path := range []string{"/v1/responses/compact", "/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			f := &fake{t: t, ctype: "application/json", reply: `{}`}
			setup(t, provider.Chat, f)
			code, body := post(t, path, `{"model":"fake/m1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
			if code != 400 || !strings.Contains(body, "/responses/compact is not supported for fake/m1") || !strings.Contains(body, "invalid_request_error") {
				t.Fatalf("%d %s", code, body)
			}
			if f.calls != 0 {
				t.Errorf("upstream asked %d times: %s", f.calls, f.got)
			}
		})
	}
}

// compactSealedStream is the ChatGPT backend's reply to a request ending in
// compaction_trigger, as it streamed it on 2026-11-22 (gpt-6.2-sol): the
// compaction item added, compacting, done, then the response completed
// with that item alone as its output.
func compactSealedStream(enc string) string {
	item := `{"type":"compaction","id":"cmp_7285","encrypted_content":"` + enc + `"}`
	return sse(
		`data: {"type":"response.created","response":{"id":"resp_7285","object":"response","created_at":1795000000,"status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","id":"cmp_7285"}}`,
		`data: {"type":"response.compaction.compacting","output_index":0}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`data: {"type":"response.completed","response":{"id":"resp_7285","object":"response","created_at":1795000000,"status":"completed","output":[`+item+`],"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150}}}`)
}

// compactAsked is a /responses/compact as Pi's extension sends it: the
// conversation so far, an earlier compaction in it.
const compactAsked = `{"model":"%s","instructions":"summarise","input":[
  {"type":"compaction","encrypted_content":"gAAAAearlier=="},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"now the tests"}]}]}`

// checkCompaction checks body is the response.compaction for compactAsked
// sealed as enc: its two user messages, then the compaction.
func checkCompaction(t *testing.T, body, enc string) {
	t.Helper()
	var out struct {
		Object string `json:"object"`
		Output []struct {
			Type string `json:"type"`
			Role string `json:"role"`
			Enc  string `json:"encrypted_content"`
		} `json:"output"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Object != "response.compaction" || len(out.Output) != 3 ||
		out.Output[0].Role != "user" || out.Output[1].Role != "user" || out.Output[2].Type != "compaction" || out.Output[2].Enc != enc || out.Usage.Total != 150 {
		t.Fatalf("client got %s", body)
	}
}

// #7285 (Arobotmaster, jinpwng): /responses/compact for codex/gpt-5.4 on
// a ChatGPT account was a 400 telling the client to compact on
// /responses. The backend has no /responses/compact (404 to every body),
// so the account is asked as Codex asks it: /responses, the input with
// compaction_trigger last, streamed; and the client gets the
// response.compaction, its user messages then the sealed item. Who sealed
// it is remembered, as a key's provider's is.
func TestResponsesCompactChatGPTAccount(t *testing.T) {
	for _, path := range []string{"/v1/responses/compact", "/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			codexSignedIn(t)
			var mu sync.Mutex
			var asked []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				asked = append(asked, r.URL.Path+" "+string(b))
				mu.Unlock()
				if r.URL.Path != "/backend-api/codex/responses" {
					w.WriteHeader(404)
					io.WriteString(w, `{"detail":"Not Found"}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, compactSealedStream("gAAAAB-sealed-by-chatgpt"))
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })

			code, body := post(t, path, strings.Replace(compactAsked, "%s", "codex/gpt-5.4", 1))
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			checkCompaction(t, body, "gAAAAB-sealed-by-chatgpt")
			mu.Lock()
			defer mu.Unlock()
			if len(asked) != 1 || !strings.HasPrefix(asked[0], "/backend-api/codex/responses ") {
				t.Fatalf("backend asked %q", asked)
			}
			got := strings.TrimPrefix(asked[0], "/backend-api/codex/responses ")
			var req struct {
				Stream bool              `json:"stream"`
				Input  []json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal([]byte(got), &req); err != nil || !req.Stream || len(req.Input) == 0 ||
				string(req.Input[len(req.Input)-1]) != `{"type":"compaction_trigger"}` || !strings.Contains(got, "gAAAAearlier==") {
				t.Errorf("backend got %s", got)
			}
			cp, ok := checkpointOf([]byte(`{"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-chatgpt"}]}`))
			if !ok || cp.Provider != "codex" {
				t.Errorf("sealed by %+v, %v", cp, ok)
			}
		})
	}
}

// #7285's sibling: a Copilot account, whose API has no /responses/compact
// either, is asked the same, on its /responses.
func TestResponsesCompactCopilotAccount(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	copilotFake(t, "gho_compact7285", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-5.3-codex","name":"GPT-5.3-Codex","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat"}}]}`)
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+string(b))
		mu.Unlock()
		if r.URL.Path != "/responses" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, compactSealedStream("gAAAAB-sealed-by-copilot"))
	})
	code, body := post(t, "/v1/responses/compact", strings.Replace(compactAsked, "%s", "copilot/gpt-5.3-codex", 1))
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	checkCompaction(t, body, "gAAAAB-sealed-by-copilot")
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "/responses ") || !strings.Contains(asked[0], `{"type":"compaction_trigger"}`) {
		t.Errorf("Copilot was asked %q", asked)
	}
}

// A model that answers the trigger with no compaction item — a reply,
// say — is a 502, never a response.compaction holding nothing sealed.
func TestResponsesCompactAccountWithoutCompaction(t *testing.T) {
	codexSignedIn(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	code, body := post(t, "/v1/responses/compact", strings.Replace(compactAsked, "%s", "codex/gpt-5.4", 1))
	if code != 502 || !strings.Contains(body, "without a compaction item") {
		t.Fatalf("%d %s", code, body)
	}
}
