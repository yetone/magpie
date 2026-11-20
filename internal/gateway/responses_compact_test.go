package gateway

import (
	"encoding/json"
	"strings"
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
