package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A conversation on one of two ChatGPT accounts stays on it across a
// pause the ChatGPT backend still has it cached through (leelantao on X:
// two Plus accounts spent 90% in a day). Five minutes was every vendor's
// cold, so after a coffee break routing moved the conversation to the
// other account, which read none of it from cache and refused the
// reasoning the first had sealed. An API key's conversation is cold at
// five minutes as before, and a ChatGPT one past chatGPTCold.
func TestChatGPTConversationStaysPastFiveMinutes(t *testing.T) {
	chatgpt := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-6-sol", rest: "codex"}
	}
	keyed := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "oai", Key: key, Responses: "https://api.openai.com/v1"}, model: "gpt-6-sol", rest: "oai"}
	}
	first := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}]}`)
	again := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"now the tests"}]}]}`)
	for i, c := range []struct {
		name       string
		last, next candidate
		ago        time.Duration
		why        string
	}{
		{"ChatGPT, 6 minutes on", chatgpt("a@example.com"), chatgpt("b@example.com"), 6 * time.Minute, "cache"},
		{"ChatGPT, 12 minutes on", chatgpt("a@example.com"), chatgpt("b@example.com"), 12 * time.Minute, "cache"},
		{"ChatGPT, past its window", chatgpt("a@example.com"), chatgpt("b@example.com"), chatGPTCold + time.Minute, "cold"},
		{"an API key, 6 minutes on", keyed("sk-a"), keyed("sk-b"), 6 * time.Minute, "cold"},
		{"an API key, 4 minutes on", keyed("sk-a"), keyed("sk-b"), 4 * time.Minute, "cache"},
	} {
		h := http.Header{}
		h.Set("x-claude-code-session-id", "chatgpt-cold-"+string(rune('a'+i)))
		// the other account goes first by routing: Smart puts the one with
		// more left first, which after a turn on the last is the other
		cs := []candidate{c.next, c.last}
		pl := planned{order: []Weighed{{}, {}}}
		_, _, _, key := affine("t", "", false, false, false, false, h, provider.Responses, first, first, cs, pl)
		answered(key, c.last, 1, Usage{Input: 1200, CacheRead: 20000, CacheWrite: 1200})
		t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })
		sticks.Lock()
		st := sticks.m[key]
		st.at = st.at.Add(-c.ago)
		sticks.m[key] = st
		sticks.Unlock()
		got, _, a, _ := affine("t", "", false, false, false, false, h, provider.Responses, again, again, cs, pl)
		if a.Why != c.why || a.Kept != (c.why == "cache") {
			t.Fatalf("%s: why %q kept %v, want %q", c.name, a.Why, a.Kept, c.why)
		}
		if want := c.next; c.why == "cache" {
			want = c.last
			if got[0].who() != want.who() {
				t.Fatalf("%s: went to %s, want %s", c.name, got[0].who(), want.who())
			}
		} else if got[0].who() != want.who() {
			t.Fatalf("%s: went to %s, want routing's %s", c.name, got[0].who(), want.who())
		}
	}
}

// A group's rules keep a ChatGPT conversation's effort through the same
// pause (keepsEffort): a lower pick then reads none of the cache back.
func TestChatGPTKeepsEffortPastFiveMinutes(t *testing.T) {
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	tr := turnRule{effort: "high", at: at, answered: at, size: 40_000, cold: chatGPTCold}
	if !keepsEffort(tr, "low", 41_000, at.Add(12*time.Minute)) {
		t.Fatal("a ChatGPT conversation 12 minutes on dropped its effort")
	}
	if keepsEffort(tr, "low", 41_000, at.Add(chatGPTCold+time.Minute)) {
		t.Fatal("kept its effort past the ChatGPT backend's window")
	}
	tr.cold = 0
	if keepsEffort(tr, "low", 41_000, at.Add(6*time.Minute)) {
		t.Fatal("another vendor's conversation kept its effort past five minutes")
	}
}
