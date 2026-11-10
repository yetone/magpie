package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude Code conversation on one of two DeepSeek keys stays on it
// across a pause DeepSeek still has it cached through (on X: "Claude code
// 通过 Magpie 链接 DeepSeek 缓存命中率只有 50%"). DeepSeek clears an unused
// cache after hours, and a real conversation read 98–99% back after 7 and
// 26 minutes idle, but five minutes was its cold, so routing moved the
// conversation to the other key, which read none of it from cache.
// Another vendor's key is cold at five minutes as before.
func TestDeepSeekConversationStaysPastFiveMinutes(t *testing.T) {
	deepseek := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "deepseek", Key: key, Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic"}, model: "deepseek-v4-pro", rest: "deepseek"}
	}
	other := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "ant", Key: key, Anthropic: "https://api.anthropic.com"}, model: "claude-sonnet-5-5", rest: "ant"}
	}
	first := []byte(`{"messages":[{"role":"user","content":"fix the bug"}]}`)
	again := []byte(`{"messages":[{"role":"user","content":"fix the bug"},{"role":"assistant","content":"done"},{"role":"user","content":"now the tests"}]}`)
	for i, c := range []struct {
		name       string
		last, next candidate
		ago        time.Duration
		why        string
	}{
		{"DeepSeek, 6 minutes on", deepseek("sk-a"), deepseek("sk-b"), 6 * time.Minute, "cache"},
		{"DeepSeek, 26 minutes on", deepseek("sk-a"), deepseek("sk-b"), 26 * time.Minute, "cache"},
		{"DeepSeek, past its window", deepseek("sk-a"), deepseek("sk-b"), deepseekCold + time.Minute, "cold"},
		{"another vendor, 6 minutes on", other("sk-a"), other("sk-b"), 6 * time.Minute, "cold"},
		{"another vendor, 4 minutes on", other("sk-a"), other("sk-b"), 4 * time.Minute, "cache"},
	} {
		h := http.Header{}
		h.Set("x-claude-code-session-id", "deepseek-cold-"+string(rune('a'+i)))
		cs := []candidate{c.next, c.last}
		pl := planned{order: []Weighed{{}, {}}}
		_, _, _, key := affine("t", "", false, false, false, false, h, provider.Anthropic, first, first, cs, pl)
		answered(key, c.last, 1, Usage{Input: 300, CacheRead: 13696})
		t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })
		sticks.Lock()
		st := sticks.m[key]
		st.at = st.at.Add(-c.ago)
		sticks.m[key] = st
		sticks.Unlock()
		got, _, a, _ := affine("t", "", false, false, false, false, h, provider.Anthropic, again, again, cs, pl)
		if a.Why != c.why || a.Kept != (c.why == "cache") {
			t.Fatalf("%s: why %q kept %v, want %q", c.name, a.Why, a.Kept, c.why)
		}
		want := c.next
		if c.why == "cache" {
			want = c.last
		}
		if got[0].who() != want.who() {
			t.Fatalf("%s: went to %s, want %s", c.name, got[0].who(), want.who())
		}
	}
}

// A group's rules keep a DeepSeek conversation's effort through the same
// pause (keepsEffort): the window comes from coldAfter.
func TestDeepSeekColdAfter(t *testing.T) {
	ds := provider.Provider{ID: "deepseek", Anthropic: "https://api.deepseek.com/anthropic"}
	if got := coldAfter(ds); got != deepseekCold {
		t.Fatalf("DeepSeek's cold is %v, want %v", got, deepseekCold)
	}
	for _, p := range []provider.Provider{
		{ID: "ant", Anthropic: "https://api.anthropic.com"},
		{ID: "relay", Chat: "https://relay.example.com/v1"},
	} {
		if got := coldAfter(p); got != cacheCold {
			t.Fatalf("%s's cold is %v, want %v", p.ID, got, cacheCold)
		}
	}
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tr := turnRule{effort: "high", at: at, answered: at, size: 40_000, cold: coldAfter(ds)}
	if !keepsEffort(tr, "low", 41_000, at.Add(26*time.Minute)) {
		t.Fatal("a DeepSeek conversation 26 minutes on dropped its effort")
	}
}
