package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// socket= sends a Responses provider's turns over the Responses WebSocket
// (#5781, matrix0487 on Discord): auto, on, or off for HTTP alone. One
// without a Responses URL, or a signed-in account, takes none.
func TestProviderSetSocket(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Responses: "https://relay.example/v1"}
	for _, c := range []struct{ v, want string }{{"auto", "auto"}, {"on", "on"}, {"off", ""}, {"AUTO", "auto"}, {"default", ""}} {
		if err := applyPairs(&p, []string{"socket=" + c.v}); err != nil || p.Socket != c.want {
			t.Fatalf("socket=%s: %v, got %q", c.v, err, p.Socket)
		}
	}
	p.Socket = "auto"
	if err := applyPairs(&p, []string{"socket=sometimes"}); err == nil || p.Socket != "auto" {
		t.Fatalf("socket=sometimes taken: %v, %q", err, p.Socket)
	}
	chat := provider.Provider{ID: "chat", Name: "Chat", Key: "k", Chat: "https://relay.example/v1"}
	if err := applyPairs(&chat, []string{"socket=auto"}); err == nil {
		t.Fatal("a provider without a Responses URL took socket=auto")
	}
	if err := applyPairs(&chat, []string{"socket=off"}); err != nil {
		t.Fatalf("socket=off refused: %v", err)
	}
	a := provider.Provider{ID: "codex", Name: "Codex", Account: &provider.Account{}, Responses: "https://x/v1"}
	if err := applyPairs(&a, []string{"socket=on"}); err == nil {
		t.Fatal("an account took socket=on")
	}
}
