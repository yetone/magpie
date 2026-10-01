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

// Cursor's plugin has fast mode where the built-in Cursor has it, for a
// model with a -fast one: a group's member is saved :fast and sent fast,
// and one with no -fast one is refused, as the built-in's is.
func TestCursorPluginGroupFast(t *testing.T) {
	t.Setenv("FAKE_FAST", "1")
	var mu sync.Mutex
	var tiers []any
	pid := besideFake(t, "cursor", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		tiers = append(tiers, q["service_tier"])
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
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
	mu.Lock()
	defer mu.Unlock()
	if len(tiers) != 1 || tiers[0] != "priority" {
		t.Fatalf("Cursor's plugin was told %v", tiers)
	}
}
