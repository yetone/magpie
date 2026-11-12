package gateway

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Mistral reads prompt_cache_key to keep a conversation on the server that
// holds its cache (daoche_wuzui on X). A turn without one gets an opaque
// hash of its conversation; a client's own key and other vendors are left
// as they were.
func TestMistralPromptCacheKey(t *testing.T) {
	const msg = `{"model":"m1","messages":[{"role":"user","content":"hello there"}]}`
	run := func(preset, body, session string) string {
		f := &fake{t: t, reply: deepseekReply}
		up := setup(t, provider.Chat, f)
		p, _ := provider.Find("fake")
		p.Preset, p.Chat = preset, up.URL+"/v1"
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
		h := map[string]string{}
		if session != "" {
			h["X-Session-Id"] = session
		}
		rec, _ := postWith(t, "/v1/chat/completions", body, h)
		if rec != 200 {
			t.Fatalf("status %d", rec)
		}
		return string(f.got)
	}
	a := gjson.Get(run("mistral", msg, "sess-secret-1"), "prompt_cache_key").String()
	b := gjson.Get(run("mistral", msg, "sess-secret-1"), "prompt_cache_key").String()
	c := gjson.Get(run("mistral", msg, "sess-secret-2"), "prompt_cache_key").String()
	if a == "" || a != b || a == c {
		t.Errorf("keys %q %q %q: want the same for one session, another for another", a, b, c)
	}
	if strings.Contains(a, "sess-secret") {
		t.Errorf("key %q holds the session id", a)
	}
	if got := gjson.Get(run("mistral", strings.Replace(msg, `"messages"`, `"prompt_cache_key":"mine","messages"`, 1), "s"), "prompt_cache_key").String(); got != "mine" {
		t.Errorf("client key became %q", got)
	}
	if got := run("deepseek", msg, "s"); strings.Contains(got, "prompt_cache_key") {
		t.Errorf("another vendor got a key: %s", got)
	}
}
