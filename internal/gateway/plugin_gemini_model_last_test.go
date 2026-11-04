package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin's Gemini model (@ai-sdk/google, the GCP AI plugin's
// gemini-4-argon, plugins#734) is asked through the plugin's fetch with the
// contents the gateway's Code Assist build makes, so Codex's compaction on
// it never reaches the vendor ending with the model's turn, nor with a
// tool's response and the compaction prompt in one user turn: Vertex AI
// answers either with 400 "Requests ending with a model turn are not
// supported" (vertexTurnsAway).
func TestPluginGeminiNeverAskedEndingWithModel(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	var mu sync.Mutex
	var bodies []map[string]any
	refused := 0
	testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		bodies = append(bodies, q)
		mu.Unlock()
		if code, out := vertexTurnsAway(0, q); code != http.StatusOK {
			mu.Lock()
			refused++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, out)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"summary"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`+"\n\n")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}

	const model = `"model":"fakeco/fake-gemini"`
	const history = `{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},` +
		`{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"call_1"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"main.go"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed it in main.go."}]}`
	s := New()
	for i, c := range []struct {
		name, path, body, want string
	}{
		{"codex compaction after the model's reply", CodexPath + "/responses",
			`{` + model + `,"instructions":"You are Codex.","stream":true,"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}],"input":[` + history + `,{"type":"compaction_trigger"}]}`,
			codexCompactPrompt},
		{"codex turn ending with the assistant's message", CodexPath + "/responses",
			`{` + model + `,"instructions":"You are Codex.","stream":true,"input":[` + history + `]}`,
			continueText},
		{"codex compaction after a tool's output", CodexPath + "/responses",
			strings.Replace(issue1533, `"model": "google-vertex/gemini-3.8-flash"`, model, 1), codexCompactPrompt},
		{"compaction after a tool's output on /v1/responses", "/v1/responses",
			strings.Replace(issue1533, `"model": "google-vertex/gemini-3.8-flash"`, model, 1), codexCompactPrompt},
	} {
		var rec *httptest.ResponseRecorder
		if c.path == CodexPath+"/responses" {
			rec = codexPostTo(s, c.body)
		} else {
			rec = vertexPost(s, c.path, c.body)
		}
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "model turn") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
			continue
		}
		mu.Lock()
		last, sent, no := bodies[len(bodies)-1], len(bodies), refused
		mu.Unlock()
		// asked once, and never in a shape the vendor turns away: a
		// refusal a retry gets past still rests the account (plugins#734)
		if sent != i+1 || no != 0 {
			t.Errorf("%s: the vendor was asked %d times for %d requests, %d of them refused", c.name, sent, i+1, no)
		}
		role, text := lastTurn(t, last["contents"])
		if role != "user" || !strings.Contains(text, c.want) {
			t.Errorf("%s: the plugin's vendor was asked ending with %s %q, want user %q", c.name, role, text, c.want)
		}
	}
}
