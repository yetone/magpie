package provider

import (
	"context"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// tmr11235 on X: the subscription is added from the Add sheet's ChatGPT
// tile, yet its row on the Providers page, its editor and its usage card
// said "Codex", the agent's name, so the row didn't read as the ChatGPT
// plan just added. The plan's row, card and the name a usage log gives
// the id are "ChatGPT" now; its id stays "codex", and the Sign in with
// ChatGPT row stays "ChatGPT API", apart from it.
func TestChatGPTPlanRowIsNamedChatGPT(t *testing.T) {
	home := signIn(t)
	p, ok := codexAccount(home)
	if !ok {
		t.Fatal("the Codex sign-in isn't read")
	}
	if p.ID != "codex" || p.Name != "ChatGPT" || p.Icon != "codex-color" {
		t.Fatalf("the plan's row is %q named %q (%s), want codex named ChatGPT", p.ID, p.Name, p.Icon)
	}
	if name, icon := KnownFace("codex"); name != "ChatGPT" || icon != "codex-color" {
		t.Fatalf("a usage log names codex %q (%s)", name, icon)
	}
	held := netproxy.Hold(context.Background(), errNotAsked)
	if q := codexSubscriptionUsage(held, home, "me@example.com"); q.Provider != "codex" || q.Name != "ChatGPT" {
		t.Fatalf("the plan's usage card is %q named %q", q.Provider, q.Name)
	}
	if api := siwcProvider(Login{User: "me@example.com"}); api.Name == p.Name || api.Name != "ChatGPT API" || api.Icon == p.Icon {
		t.Fatalf("Sign in with ChatGPT's row is %q (%s), the plan's %q (%s): not told apart", api.Name, api.Icon, p.Name, p.Icon)
	}
}

// A remote magpie from before still names its plan's card "Codex": shown
// here as this magpie names it.
func TestRemoteChatGPTPlanCardNamedHere(t *testing.T) {
	cards := namedRemoteCards(Provider{ID: "office", Name: "Office"}, []SubscriptionQuota{
		{Provider: "codex", Name: "Codex", User: "a@x.com", Kind: "subscription"},
		{Provider: "claude", Name: "Claude Code", User: "a@x.com", Kind: "subscription"},
	})
	if len(cards) != 2 || cards[0].Name != "ChatGPT · Office" || cards[1].Name != "Claude Code · Office" {
		t.Fatalf("remote cards named %+v", cards)
	}
}
