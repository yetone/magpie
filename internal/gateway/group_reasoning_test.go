package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// quietUp is a Chat vendor that answers as its key, and keeps what it was
// sent; fail makes it answer 500, so the group falls to the next member.
// It speaks Responses too, for a request sent that way, and streams a
// Chat request that asks to be.
type quietUp struct {
	key  string
	mu   sync.Mutex
	last string
	fail int
}

func (u *quietUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.last = string(b)
	fail := u.fail
	u.mu.Unlock()
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		io.WriteString(w, `{"error":{"message":"down"}}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/responses") {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from `+u.key+`"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`,
		))
		return
	}
	if strings.Contains(string(b), `"stream":true`) {
		// a translated request (a Gemini client's) is streamed
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+u.key+`"}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`,
		))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+u.key+`"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
}

func (u *quietUp) sent() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	var v map[string]any
	json.Unmarshal([]byte(u.last), &v)
	return v
}

// quietGroup is the group Mix of a member that thinks (thinks/levelled)
// and one models.dev lists as not thinking (quiet/plain), ordered, each a
// quietUp. reset begins a scenario as if the last hadn't run — no member
// resting, no conversation kept where it went (affinity, #63) — with the
// thinking member answering fail (0: as asked).
func quietGroup(t *testing.T) (thinks, quiet *quietUp, reset func(fail int)) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	// models.dev as it has these: xiaomi's own mimo-v2.6-flash thinks with
	// a switch alone, and opencode-go's "plain" is listed and doesn't
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
  "xiaomi": {"models": {
    "mimo-v2.6-flash": {"id":"mimo-v2.6-flash","reasoning":true,"reasoning_options":[{"type":"toggle"}]}}},
  "opencode-go": {"models": {
    "plain": {"id":"plain","reasoning":false},
    "levelled": {"id":"levelled","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	thinks, quiet = &quietUp{key: "kt"}, &quietUp{key: "kq"}
	for _, x := range []struct {
		id    string
		up    *quietUp
		model string
	}{
		{"thinks", thinks, "levelled"},
		{"quiet", quiet, "plain"},
	} {
		srv := httptest.NewServer(x.up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: x.id, Name: x.id, Key: "k", Models: []string{x.model},
			Chat: srv.URL + "/v1", Responses: srv.URL + "/v1", Catalog: "opencode-go"}); err != nil {
			t.Fatal(err)
		}
		// the vendor's own list, as a relay answers: ids alone
		if err := catalog.SaveLive(x.id, srv.URL+"/v1", []catalog.Model{{ID: x.model}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mix", Members: []string{"thinks/levelled", "quiet/plain"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	reset = func(fail int) {
		t.Helper()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
		thinks.mu.Lock()
		thinks.fail = fail
		thinks.mu.Unlock()
	}
	return thinks, quiet, reset
}

// A group reasons when any member does, so an agent asks it for reasoning
// a member may not take: a member known not to think is sent none of it
// (#950). The one that thinks is sent the effort the agent asked for,
// fitted as before; the quiet one is sent the request without it — some
// vendors turn it away with a 400 on a model that can't think — and the
// group routes to it as it did.
func TestGroupNonThinkingMemberNoEffort(t *testing.T) {
	fresh(t)
	thinks, quiet, reset := quietGroup(t)
	s := New()
	ask := func(extra string) (string, Route) {
		t.Helper()
		code, out := postAs(t, s, "s1", `{"model":"group/mix"`+extra+`,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		return out, lastRoute(s)
	}
	// A: the member that thinks answers, and is sent the agent's effort
	reset(0)
	out, r := ask(`,"reasoning_effort":"high"`)
	if !strings.Contains(out, "from kt") {
		t.Fatalf("A: %s", out)
	}
	if sent := thinks.sent(); sent["reasoning_effort"] != "high" {
		t.Fatalf("A: the thinking member was sent %v", sent)
	}
	if len(r.Tries) != 1 || r.Tries[0].Model != "levelled" || r.Tries[0].Effort != "high" {
		t.Fatalf("A: traced %+v", r.Tries)
	}
	// B: it fails, the quiet member answers, and is sent no reasoning ask
	reset(500)
	out, r = ask(`,"reasoning_effort":"high"`)
	if !strings.Contains(out, "from kq") {
		t.Fatalf("B: %s", out)
	}
	if sent := quiet.sent(); sent["reasoning_effort"] != nil || sent["reasoning"] != nil {
		t.Fatalf("B: the quiet member was sent %v", sent)
	}
	if sent := quiet.sent(); sent["model"] != "plain" {
		t.Fatalf("B: the quiet member was sent %v", sent)
	}
	if len(r.Tries) != 2 || r.Tries[1].Model != "plain" || r.Tries[1].Effort != "" {
		t.Fatalf("B: traced %+v", r.Tries)
	}
	// C: an agent that asks for none gets the same clean body
	reset(500)
	out, _ = ask(``)
	if !strings.Contains(out, "from kq") {
		t.Fatalf("C: %s", out)
	}
	if sent := quiet.sent(); sent["reasoning_effort"] != nil || sent["reasoning"] != nil {
		t.Fatalf("C: the quiet member was sent %v", sent)
	}
	// D: through the Responses API, where the ask is a reasoning object
	responses := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
			strings.NewReader(`{"model":"group/mix","input":"hi","reasoning":{"effort":"high","summary":"auto"}}`)))
		if rec.Code != 200 {
			t.Fatalf("D: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	reset(0)
	if out := responses(); !strings.Contains(out, "from kt") {
		t.Fatalf("D: %s", out)
	}
	if sent := thinks.sent(); sent["reasoning"] == nil {
		t.Fatalf("D: the thinking member was sent %v", sent)
	}
	reset(500)
	if out := responses(); !strings.Contains(out, "from kq") {
		t.Fatalf("D: %s", out)
	}
	if sent := quiet.sent(); sent["reasoning"] != nil || sent["reasoning_effort"] != nil {
		t.Fatalf("D: the quiet member was sent %v", sent)
	}
	// E: a quiet member the user fixed at an effort is sent it: that is
	// the user saying this model does think, over a catalog that may be
	// wrong about it — which is what #950 was reported about — so magpie's
	// word doesn't override theirs
	if err := provider.SaveGroup(provider.Group{Name: "Fix", Members: []string{"thinks/levelled", "quiet/plain:high"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	reset(500)
	code, out := postAs(t, s, "e1", `{"model":"group/fix","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "from kq") {
		t.Fatalf("E: %d %s", code, out)
	}
	if sent := quiet.sent(); sent["reasoning_effort"] != "high" {
		t.Fatalf("E: the fixed member was sent %v", sent)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[1].Fixed != "high" || r.Tries[1].Effort != "high" {
		t.Fatalf("E: traced %+v", r.Tries)
	}
}

// A Gemini client asks for reasoning in generationConfig.thinkingConfig,
// which reaches a Chat member as reasoning_effort: a quiet member is sent
// none of it, as the Anthropic and OpenAI asks aren't (#1253), through the
// gateway as it serves requests (lanGuard). The member that thinks is
// still sent it, and so is a quiet member the user fixed at an effort.
func TestGroupQuietMemberGeminiThinking(t *testing.T) {
	fresh(t)
	thinks, quiet, reset := quietGroup(t)
	h := lanGuard(New().Handler())
	gemini := func(group, config string) string {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1beta/models/group/"+group+":generateContent",
			strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0.2,"thinkingConfig":`+config+`}}`))
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", config, w.Code, w.Body)
		}
		return w.Body.String()
	}
	for _, config := range []string{`{"thinkingLevel":"high","includeThoughts":true}`, `{"thinkingBudget":8192}`, `{"thinkingBudget":-1}`} {
		reset(0)
		if out := gemini("mix", config); !strings.Contains(out, "from kt") {
			t.Fatalf("%s: %s", config, out)
		}
		if sent := thinks.sent(); sent["reasoning_effort"] == nil {
			t.Fatalf("%s: the thinking member was sent %v", config, sent)
		}
		reset(500)
		if out := gemini("mix", config); !strings.Contains(out, "from kq") {
			t.Fatalf("%s: %s", config, out)
		}
		sent := quiet.sent()
		if sent["reasoning_effort"] != nil || sent["reasoning"] != nil {
			t.Fatalf("%s: the quiet member was sent %v", config, sent)
		}
		if sent["model"] != "plain" || sent["temperature"] != 0.2 {
			t.Fatalf("%s: the quiet member lost the rest of the request: %v", config, sent)
		}
	}
	// a quiet member the user fixed at an effort keeps the ask
	if err := provider.SaveGroup(provider.Group{Name: "Fix", Members: []string{"thinks/levelled", "quiet/plain:high"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	reset(500)
	if out := gemini("fix", `{"thinkingLevel":"high"}`); !strings.Contains(out, "from kq") {
		t.Fatalf("fixed: %s", out)
	}
	if sent := quiet.sent(); sent["reasoning_effort"] != "high" {
		t.Fatalf("fixed: the fixed member was sent %v", sent)
	}
}

// The request the quiet member is sent keeps every other field: only what
// asks the model to think goes, in the agent's own protocol. Anthropic's
// thinking turned off is no ask and stays; output_config keeps its other
// keys. Gemini's thinkingConfig goes when it asks to think; a budget of 0
// is no ask and stays.
func TestWithoutReasoningAsk(t *testing.T) {
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		in    string
		want  string
	}{
		{"chat", provider.Chat, `{"model":"m","reasoning_effort":"high","temperature":0.2}`, `{"model":"m","temperature":0.2}`},
		{"chat reasoning", provider.Chat, `{"model":"m","reasoning":{"effort":"high","summary":"auto"}}`, `{"model":"m"}`},
		{"chat nothing", provider.Chat, `{"model":"m","messages":[]}`, `{"model":"m","messages":[]}`},
		{"responses", provider.Responses, `{"model":"m","reasoning":{"effort":"high","context":"all_turns"}}`, `{"model":"m"}`},
		{"anthropic on", provider.Anthropic, `{"model":"m","thinking":{"type":"enabled","budget_tokens":4096}}`, `{"model":"m"}`},
		{"anthropic adaptive", provider.Anthropic, `{"model":"m","thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`, `{"model":"m"}`},
		{"anthropic off", provider.Anthropic, `{"model":"m","thinking":{"type":"disabled"}}`, `{"model":"m","thinking":{"type":"disabled"}}`},
		{"gemini level", provider.Gemini, `{"model":"m","generationConfig":{"temperature":0.2,"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}}}`, `{"generationConfig":{"temperature":0.2},"model":"m"}`},
		{"gemini budget", provider.Gemini, `{"model":"m","generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, `{"model":"m"}`},
		{"gemini off", provider.Gemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}},"model":"m"}`, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}},"model":"m"}`},
		{"anthropic output", provider.Anthropic, `{"model":"m","output_config":{"effort":"high","format":{"type":"json"}}}`, `{"model":"m","output_config":{"format":{"type":"json"}}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := string(withoutReasoningAsk(c.proto, []byte(c.in)))
			if got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}
