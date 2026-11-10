package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dshUA is DSH 0.2.2's User-Agent (llm's attributionHeaders), which its
// llm-pi-ai routes, magpie's among them, send with nothing else of its own.
const dshUA = "deepseek-harness/0.2.2-rc.1 (+https://github.com/deepseek-ai/deepseek-harness)"

// dshCompactAsk is the first line of compaction-basic's
// COMPACTION_INSTRUCTION (@deepseek-ai/dsh-compaction-basic 0.0.1-rc.3),
// the user message DSH puts after the conversation it compacts.
const dshCompactAsk = "You are now acting as a compaction engine for this AI coding assistant. Condense the conversation ABOVE into a structured checkpoint that lets another model resume the work with no loss of essential context.\n\nOutput EXACTLY the Markdown structure below: keep every section, in order."

// dshSetup is a provider serving m1, m2 and m3 on a fake Anthropic
// upstream that notes the model of the last request, and streams its
// reply when asked to: the gateway streams a translated request.
func dshSetup(t *testing.T) *desktopSubagentUpstream {
	t.Helper()
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := &desktopSubagentUpstream{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := modelOf(body)
		u.mu.Lock()
		u.model, u.path = model, r.URL.Path
		u.mu.Unlock()
		var in struct{ Stream bool }
		json.Unmarshal(body, &in)
		if !in.Stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"reply","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"on it"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"reply","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":{"input_tokens":7,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"on it"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			io.WriteString(w, e+"\n\n")
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	return u
}

// dshAsk sends a request shaped like DSH's through magpie's llm-pi-ai route:
// its User-Agent, no compaction mark, on the protocol at path, ending with
// last. It carries key, the token of the agent it is from: the gateway's
// tests set no agent list, so DSH's User-Agent names no agent here (the
// GUI's context_test checks that it names dsh).
func dshAsk(t *testing.T, s *Server, path, model, last, key string) {
	t.Helper()
	var body map[string]any
	switch path {
	case "/v1/responses":
		body = map[string]any{"model": model, "input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "fix the build"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": last}}},
		}}
	default:
		body = map[string]any{"model": model, "max_tokens": 200, "messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "fix the build"}}},
			map[string]any{"role": "assistant", "content": "done"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": last}}},
		}}
	}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("User-Agent", dshUA)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s %s: %d %s", path, model, w.Code, w.Body.String())
	}
}

// DSH compacts on the session's latest model, so a switch away from one
// whose provider is out of its limit failed on the compaction (#5395,
// Musikrv77852 on X). With a model set for its compaction, the compaction
// goes there, whatever protocol the route speaks; its other turns stay on
// the session's model, and the Routing page says why it moved.
func TestDshCompactMoved(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			u := dshSetup(t)
			if err := provider.SetDshCompactModel("fake/m2"); err != nil {
				t.Fatal(err)
			}
			s := New()
			dshAsk(t, s, path, "fake/m1", dshCompactAsk, TokenFor("dsh"))
			if got, _ := u.seen(); got != "m2" {
				t.Fatalf("compaction sent %q, want m2", got)
			}
			r := lastRoute(s)
			if r.Kind != KindCompact || r.Moved == nil || *r.Moved != (ModelMove{To: "fake/m2", Why: movedDshCompact}) {
				t.Fatalf("trace: kind %q moved %+v", r.Kind, r.Moved)
			}
			dshAsk(t, s, path, "fake/m1", "go on", TokenFor("dsh"))
			if got, _ := u.seen(); got != "m1" {
				t.Fatalf("turn sent %q, want m1", got)
			}
		})
	}
}

// Nothing set, another agent's compaction, the model set asked for itself,
// and a model set that magpie doesn't serve or the user disabled: the
// compaction goes on as asked.
func TestDshCompactLeft(t *testing.T) {
	for _, tc := range []struct{ name, target, key string }{
		{"unset", "", TokenFor("dsh")},
		{"other agent", "fake/m2", TokenFor("opencode")},
		{"same", "fake/m1", TokenFor("dsh")},
		{"unserved", "fake/missing", TokenFor("dsh")},
		{"disabled", "fake/m2", TokenFor("dsh")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := dshSetup(t)
			if err := provider.SetDshCompactModel(tc.target); err != nil {
				t.Fatal(err)
			}
			if tc.name == "disabled" {
				if err := provider.DisableModels("fake/m2"); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			dshAsk(t, s, "/v1/chat/completions", "fake/m1", dshCompactAsk, tc.key)
			if got, _ := u.seen(); got != "m1" {
				t.Fatalf("sent %q, want m1", got)
			}
			if r := lastRoute(s); r.Moved != nil {
				t.Fatalf("moved %+v", r.Moved)
			}
		})
	}
}

// A bare name names no model of magpie's.
func TestDshCompactModelRefusesBareName(t *testing.T) {
	fresh(t)
	if err := provider.SetDshCompactModel("deepseek-flash"); err == nil {
		t.Fatal("a bare name was taken")
	}
	if err := provider.SetDshCompactModel("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := provider.DshCompactModel(); got != "fake/m2" {
		t.Fatalf("read back %q", got)
	}
}
