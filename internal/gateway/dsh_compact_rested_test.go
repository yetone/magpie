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

// wbLimited is WorkBuddy's refusal in qtwaiter's screenshot, as its
// plugin hands it on.
const wbLimited = `{"error":{"code":null,"message":"您的使用量已超出频率限制，将在 2026-11-10 13:28:47 UTC+8 重置，您也可以切换其他模型继续使用。","param":null,"type":"rate_limit_error"}}`

// dshTwoProviders saves wb and trae, both serving deepseek-v4.1-flash on
// fake Anthropic upstreams, wb's refusing every request as out of its
// rate for an hour while limited is set. It returns the models each was
// asked for, in order, by provider.
func dshTwoProviders(t *testing.T, traeServes ...string) (asked func(string) []string) {
	t.Helper()
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	var mu sync.Mutex
	seen := map[string][]string{}
	up := func(id string, limited bool) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			model := modelOf(body)
			mu.Lock()
			seen[id] = append(seen[id], model)
			mu.Unlock()
			if limited {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(429)
				io.WriteString(w, wbLimited)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"reply","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"checkpoint"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	if err := provider.Save(provider.Provider{ID: "wb", Name: "WorkBuddy", Key: "k", Anthropic: up("wb", true), Models: []string{"deepseek-v4.1-flash", "glm-5.2"}}); err != nil {
		t.Fatal(err)
	}
	if len(traeServes) > 0 {
		if err := provider.Save(provider.Provider{ID: "trae-cn", Name: "Trae CN", Key: "k", Anthropic: up("trae-cn", false), Models: traeServes}); err != nil {
			t.Fatal(err)
		}
	}
	return func(id string) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen[id]...)
	}
}

// dshSend is dshAsk without its 200 check: the status and body.
func dshSend(s *Server, model, last string) (int, string) {
	b, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 200, "messages": []any{
		map[string]any{"role": "user", "content": "fix the build"},
		map[string]any{"role": "assistant", "content": "done"},
		map[string]any{"role": "user", "content": last},
	}})
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+TokenFor("dsh"))
	r.Header.Set("User-Agent", dshUA)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// qtwaiter on X: DSH on WorkBuddy's deepseek-v4.1-flash, out of its rate,
// switched to Trae CN's, and every try said "Cannot switch to
// magpie/trae-cn/deepseek-v4.1-flash: context compaction failed: 429 ...
// WorkBuddy: 您的使用量已超出频率限制". DSH compacts on the model it
// switches away from, and magpie answered that compaction with
// WorkBuddy's held refusal. Now it goes to the same model on Trae CN; the
// session's own turns on WorkBuddy are still refused as they were, and
// the Routing page says why the compaction moved.
func TestDshCompactLeavesRestedProvider(t *testing.T) {
	asked := dshTwoProviders(t, "deepseek-v4.1-flash", "glm-5.2")
	s := New()
	if code, body := dshSend(s, "wb/deepseek-v4.1-flash", "go on"); code != 429 || !strings.Contains(body, "频率限制") {
		t.Fatalf("WorkBuddy's turn: %d %s", code, body)
	}
	before := len(asked("wb"))
	code, body := dshSend(s, "wb/deepseek-v4.1-flash", dshCompactAsk)
	if code != 200 {
		t.Fatalf("the compaction for the switch: %d %s", code, body)
	}
	if got := asked("trae-cn"); len(got) != 1 || got[0] != "deepseek-v4.1-flash" {
		t.Fatalf("Trae CN asked for %v, want its deepseek-v4.1-flash", got)
	}
	if len(asked("wb")) != before {
		t.Fatal("WorkBuddy was asked again while it rests")
	}
	r := lastRoute(s)
	if r.Kind != KindCompact || r.Moved == nil || *r.Moved != (ModelMove{To: "trae-cn/deepseek-v4.1-flash", Why: movedDshCompactRested}) {
		t.Fatalf("trace: kind %q moved %+v", r.Kind, r.Moved)
	}
	// a turn of the session's, not a compaction, isn't moved
	if code, body := dshSend(s, "wb/deepseek-v4.1-flash", "go on"); code != 429 || !strings.Contains(body, "频率限制") {
		t.Fatalf("WorkBuddy's next turn: %d %s", code, body)
	}
	if got := asked("trae-cn"); len(got) != 1 {
		t.Fatalf("a turn went to Trae CN: %v", got)
	}
}

// A compaction on a provider that isn't resting stays there, as does one
// no other provider serves the model of (it is refused as it was), and
// one on a model whose stand-in rests too.
func TestDshCompactStaysUnlessRested(t *testing.T) {
	t.Run("not resting", func(t *testing.T) {
		asked := dshTwoProviders(t, "deepseek-v4.1-flash")
		s := New()
		if code, _ := dshSend(s, "trae-cn/deepseek-v4.1-flash", dshCompactAsk); code != 200 {
			t.Fatalf("status %d", code)
		}
		if r := lastRoute(s); r.Moved != nil {
			t.Fatalf("moved %+v", r.Moved)
		}
		if len(asked("trae-cn")) != 1 || len(asked("wb")) != 0 {
			t.Fatalf("wb %v, trae %v", asked("wb"), asked("trae-cn"))
		}
	})
	t.Run("no other provider", func(t *testing.T) {
		asked := dshTwoProviders(t, "glm-5.2")
		s := New()
		dshSend(s, "wb/deepseek-v4.1-flash", "go on")
		if code, body := dshSend(s, "wb/deepseek-v4.1-flash", dshCompactAsk); code != 429 || !strings.Contains(body, "频率限制") {
			t.Fatalf("%d %s", code, body)
		}
		if len(asked("trae-cn")) != 0 {
			t.Fatalf("Trae CN asked for %v", asked("trae-cn"))
		}
	})
	t.Run("other agent", func(t *testing.T) {
		asked := dshTwoProviders(t, "deepseek-v4.1-flash")
		s := New()
		dshSend(s, "wb/deepseek-v4.1-flash", "go on")
		b, _ := json.Marshal(map[string]any{"model": "wb/deepseek-v4.1-flash", "max_tokens": 200, "messages": []any{
			map[string]any{"role": "user", "content": dshCompactAsk},
		}})
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+TokenFor("opencode"))
		r.Header.Set("x-deepseek-harness-compact", "1")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 429 || len(asked("trae-cn")) != 0 {
			t.Fatalf("%d, Trae CN asked for %v", w.Code, asked("trae-cn"))
		}
	})
}
