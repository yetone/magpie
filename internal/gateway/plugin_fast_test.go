package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// cursorUp is an upstream for the fake plugin that answers every chat and
// keeps what each was asked: its model and service_tier.
type cursorUp struct {
	mu   sync.Mutex
	asks []map[string]any
}

func (u *cursorUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var q map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.asks = append(u.asks, q)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, `data: [DONE]`))
}

func (u *cursorUp) last(t *testing.T) map[string]any {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.asks) == 0 {
		t.Fatal("the plugin asked its upstream nothing")
	}
	return u.asks[len(u.asks)-1]
}

// Cursor's plugin from 0.2.x lists a model once per context size and no
// -fast one; from 0.2.2 it says fast on the sizes Cursor has a fast
// variant at (#1360, loosheng). A group's member is saved :fast and sent
// fast for those, and refused for the rest, as the built-in's is.
func TestCursorPluginGroupFast(t *testing.T) {
	t.Setenv("FAKE_FAST", "tier")
	up := &cursorUp{}
	pid := besideFake(t, "cursor", up)
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]bool{"fake-opus@300k": true, "fake-opus@1m": false, "fake-1": false, "fake-claude": false} {
		if got := provider.CanFast(*p, model); got != want {
			t.Errorf("CanFast(%s) = %v, want %v", model, got, want)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Members: []string{pid + "/fake-opus@1m:fast"}}); err == nil || !strings.Contains(err.Error(), "no fast mode") {
		t.Fatalf("fake-opus@1m saved fast: %v", err)
	}
	saveFastGroup(t, []string{pid + "/fake-opus@300k:fast"}, nil)
	r := postProto(t, New(), "/v1/chat/completions", `{"model":"group/f","messages":[{"role":"user","content":"hi"}]}`)
	if len(r.Tries) != 1 || !r.Tries[0].Fast {
		t.Fatalf("tries %+v", r.Tries)
	}
	if q := up.last(t); q["service_tier"] != "priority" || q["model"] != "fake-opus@300k" {
		t.Fatalf("Cursor's plugin was asked %v %v", q["model"], q["service_tier"])
	}
}

// Cursor's plugin before 0.2 listed a model's fast one as a model of its
// own, -fast: a model with one still has fast mode, the -fast one and one
// without none.
func TestCursorPluginGroupFastSibling(t *testing.T) {
	t.Setenv("FAKE_FAST", "1")
	up := &cursorUp{}
	pid := besideFake(t, "cursor", up)
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	if !provider.CanFast(*p, "fake-1") || provider.CanFast(*p, "fake-1-fast") || provider.CanFast(*p, "fake-claude") {
		t.Fatal("the Cursor plugin's fast models aren't the ones with a -fast one")
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Members: []string{pid + "/fake-claude:fast"}}); err == nil || !strings.Contains(err.Error(), "no fast mode") {
		t.Fatalf("fake-claude saved fast: %v", err)
	}
	saveFastGroup(t, []string{pid + "/fake-1:fast"}, nil)
	r := postProto(t, New(), "/v1/chat/completions", `{"model":"group/f","messages":[{"role":"user","content":"hi"}]}`)
	if len(r.Tries) != 1 || !r.Tries[0].Fast {
		t.Fatalf("tries %+v", r.Tries)
	}
	if q := up.last(t); q["service_tier"] != "priority" {
		t.Fatalf("Cursor's plugin was told %v", q["service_tier"])
	}
}

// Cursor moved onto its 0.2.x plugin keeps the agent picker's Fast (#954)
// the built-in had (#1360): a model it says is fast is switched fast and
// reaches the plugin with service_tier priority, which it runs as Cursor's
// fast variant; another agent's request for it, and a size with no fast
// variant, go as they are.
func TestCursorPluginFastPick(t *testing.T) {
	t.Setenv("FAKE_FAST", "tier")
	up := &cursorUp{}
	movedFake(t, "cursor", up)
	if err := provider.SetFastPick("opencode", "cursor/fake-opus@1m", true); err == nil || !strings.Contains(err.Error(), "no fast mode") {
		t.Fatalf("fake-opus@1m switched fast: %v", err)
	}
	if err := provider.SetFastPick("opencode", "cursor/fake-opus@300k", true); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, a := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"cursor/fake-opus@300k","messages":[{"role":"user","content":"hi"}]}`},
		{"chat streamed", "/v1/chat/completions", `{"model":"cursor/fake-opus@300k","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"cursor/fake-opus@300k","input":"hi"}`},
		{"anthropic", "/v1/messages", `{"model":"cursor/fake-opus@300k","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		r := postFrom(t, s, "opencode", a.path, a.body)
		if q := up.last(t); q["service_tier"] != "priority" || q["model"] != "fake-opus@300k" {
			t.Errorf("%s: Cursor's plugin was asked %v %v", a.name, q["model"], q["service_tier"])
		}
		if len(r.Tries) != 1 || !r.Tries[0].Fast {
			t.Errorf("%s: tries %+v", a.name, r.Tries)
		}
		r = postFrom(t, s, "codex", a.path, a.body)
		if q := up.last(t); q["service_tier"] != nil || r.Tries[0].Fast {
			t.Errorf("%s: another agent's request went fast: %v %+v", a.name, q["service_tier"], r.Tries)
		}
	}
}
