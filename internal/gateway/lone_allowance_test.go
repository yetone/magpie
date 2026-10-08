package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1016: a provider with one key on — Kimi Code (China), its plan's 5
// hours 49% used and its week 45% — is weighed against nobody, but the
// routing trace still tells what it has left, so the Routing page shows
// it as it does for several; a lone subscription account the same. A key
// whose windows aren't read stays not known.
func TestLoneOneTellsWhatItHasLeft(t *testing.T) {
	now := time.Now()
	five, week := now.Add(2*time.Hour), now.Add(26*time.Hour)
	kimi := provider.Allowance{
		{Used: 45, Resets: week, Span: 7 * 24 * time.Hour},
		{Used: 49, Resets: five, Span: 5 * time.Hour},
	}
	keyWindows(t, map[string]provider.Allowance{"sk-kimi-a": kimi})
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@example.com": kimi}
	}

	p := provider.Provider{ID: "kimi-code-cn", Name: "Kimi Code (China)", Key: "sk-kimi-a", Chat: "https://api.kimi.com/coding/v1"}
	cs := []candidate{{p: p, model: "kimi-for-coding", rest: "kimi-code-cn"}}
	got, wg := weigh(p, cs, "kimi-for-coding", provider.Chat)
	if len(got) != 1 || got[0].rest != "kimi-code-cn" {
		t.Fatalf("the one key: %s", restsOf(got))
	}
	w := weighed(got[0], p, wg, false, provider.Chat)
	if w.Kind != "provider" || !w.Known || w.Used != 49 || len(w.Renews) != 2 || !w.Renews[0].Equal(week) {
		t.Fatalf("the one key's trace: %+v", w)
	}

	codex := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}
	ac := []candidate{{p: codex, model: "gpt-6-astra", rest: "codex"}}
	got, wg = weigh(codex, ac, "gpt-6-astra", provider.Responses)
	if w := weighed(got[0], codex, wg, false, provider.Responses); w.Kind != "account" || !w.Known || w.Used != 49 {
		t.Fatalf("the one account's trace: %+v", w)
	}

	plain := provider.Provider{ID: "deepseek", Key: "sk-ds", Chat: "https://api.deepseek.com/v1"}
	pc := []candidate{{p: plain, model: "deepseek-chat", rest: "deepseek"}}
	got, wg = weigh(plain, pc, "deepseek-chat", provider.Chat)
	if w := weighed(got[0], plain, wg, false, provider.Chat); w.Known || w.Learns {
		t.Fatalf("a key whose windows aren't read: %+v", w)
	}
}

// The one key, all but used up, keeps the conversation it answered: there
// is nobody it could move to, and the routing story says it was kept,
// not that it moved.
func TestLoneSpentKeyKeepsItsConversation(t *testing.T) {
	now := time.Now()
	keyWindows(t, map[string]provider.Allowance{"sk-kimi-a": {{Used: 99, Resets: now.Add(2 * time.Hour), Span: 5 * time.Hour}}})
	p := provider.Provider{ID: "kimi-code-cn", Key: "sk-kimi-a", Chat: "https://api.kimi.com/coding/v1"}
	c := candidate{p: p, model: "kimi-for-coding", rest: "kimi-code-cn"}
	cs, wg := weigh(p, []candidate{c}, "kimi-for-coding", provider.Chat)
	pl := planned{order: []Weighed{weighed(cs[0], p, wg, false, provider.Chat)}}
	if !pl.order[0].Known || pl.order[0].Used != 99 {
		t.Fatalf("the trace: %+v", pl.order[0])
	}
	h := http.Header{}
	h.Set("x-claude-code-session-id", "lone-spent-1016")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	_, _, _, key := affine("t", provider.AffinitySession, false, h, provider.Chat, body, cs, pl)
	answered(key, c, 1, 0)
	t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })
	got, _, aff, _ := affine("t", provider.AffinitySession, false, h, provider.Chat, body, cs, pl)
	if got[0].rest != "kimi-code-cn" || aff.Why == "spent" || !aff.Kept {
		t.Fatalf("kept %v, why %s", aff.Kept, aff.Why)
	}
}
