package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Mistral routes a conversation to the server that holds its KV cache by
// x-affinity (Vibe and pi-ai send it every turn; daoche_wuzui on X). A turn
// gets a stable opaque one per session; a client's own is kept as sent;
// another vendor is sent none.
func TestMistralAffinityHeader(t *testing.T) {
	const msg = `{"model":"m1","messages":[{"role":"user","content":"hello there"}]}`
	run := func(preset string, h map[string]string) string {
		f := &fake{t: t, reply: deepseekReply}
		up := setup(t, provider.Chat, f)
		p, _ := provider.Find("fake")
		p.Preset, p.Chat = preset, up.URL+"/v1"
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
		if code, _ := postWith(t, "/v1/chat/completions", msg, h); code != 200 {
			t.Fatalf("status %d", code)
		}
		return f.head.Get("X-Affinity")
	}
	a := run("mistral", map[string]string{"X-Session-Id": "sess-secret-1"})
	b := run("mistral", map[string]string{"X-Session-Id": "sess-secret-1"})
	c := run("mistral", map[string]string{"X-Session-Id": "sess-secret-2"})
	if a == "" || a != b || a == c {
		t.Errorf("affinity %q %q %q: want the same for one session, another for another", a, b, c)
	}
	if strings.Contains(a, "sess-secret") {
		t.Errorf("affinity %q holds the session id", a)
	}
	if got := run("mistral", map[string]string{"X-Session-Id": "s", "x-affinity": "mine"}); got != "mine" {
		t.Errorf("client affinity became %q", got)
	}
	if got := run("deepseek", map[string]string{"X-Session-Id": "s", "x-affinity": "mine"}); got != "" {
		t.Errorf("another vendor got x-affinity %q", got)
	}
	if got := run("deepseek", map[string]string{"X-Session-Id": "s"}); got != "" {
		t.Errorf("another vendor got x-affinity %q", got)
	}
}
