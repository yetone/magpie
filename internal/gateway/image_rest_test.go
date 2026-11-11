package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #6160: a ChatGPT account's image allowance shows on its card as the time
// the refusal that rested it said, and as unknown before any refusal; the
// chat windows around it are as they were.
func TestCodexImageRowIsTheRefusalsResetTime(t *testing.T) {
	user := "Image.Lane@example.com"
	c := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: codexDrawer}
	t.Cleanup(func() { clearRest(c.restID()) })
	card := func() provider.SubscriptionQuota {
		in := []provider.SubscriptionQuota{{Provider: "codex", User: user, Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 40}, {Name: "7 days", Used: 10}}}}
		out := provider.WithImageLane(in, time.Now())
		if len(in[0].Windows) != 2 {
			t.Fatalf("the cached card was written to: %d windows", len(in[0].Windows))
		}
		return out[0]
	}
	row := func(q provider.SubscriptionQuota) provider.QuotaWindow {
		if len(q.Windows) != 3 || q.Windows[0].Name != "5 hours" || q.Windows[1].Name != "7 days" {
			t.Fatalf("windows = %+v", q.Windows)
		}
		return q.Windows[2]
	}
	w := row(card())
	if w.Lane != provider.ImageLane || !w.Text || !w.Aside || w.ResetsAt != nil || w.Used != 0 || w.Display != "Unknown" {
		t.Fatalf("before any refusal the row = %+v, want unknown with no time", w)
	}
	at := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(int(at.Unix())) + `}}`)
	restDrawer(c, http.StatusTooManyRequests, string(body))
	w = row(card())
	if w.ResetsAt == nil || w.ResetsAt.Sub(at).Abs() > 2*time.Second || w.Display != "Used up" || w.Used != 0 || !w.Text {
		t.Fatalf("after the refusal the row = %+v, want used up until %v and no share", w, at)
	}
	// another account's refusal is not this one's
	other := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "other@example.com"}}, model: codexDrawer}
	t.Cleanup(func() { clearRest(other.restID()) })
	restDrawer(other, http.StatusTooManyRequests, string(body))
	clearRest(c.restID())
	if w = row(card()); w.ResetsAt != nil {
		t.Fatalf("a rest lifted still shows a time: %+v", w)
	}
	// a chat model's rest on the account is not the image allowance
	chat := candidate{p: c.p, model: "gpt-5"}
	t.Cleanup(func() { clearRest(chat.restID()) })
	restDrawer(chat, http.StatusTooManyRequests, string(body))
	if w = row(card()); w.ResetsAt != nil {
		t.Fatalf("a chat model's rest shows as the image row: %+v", w)
	}
}
