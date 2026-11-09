package provider

import (
	"context"
	"net/http"
	"testing"
)

// #5781: a provider's turns go over the Responses WebSocket only at its
// own Responses URL, only with Socket set, and a nested provider without
// it never takes the one around it.
func TestSocketTurn(t *testing.T) {
	turn := func(ctx context.Context, u string) (bool, bool) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "POST", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return SocketTurn(req)
	}
	relay := Provider{ID: "relay", Key: "k", Responses: "https://relay.example/v1/", Socket: SocketAuto}
	ctx := relay.Via(context.Background())
	if ok, shun := turn(ctx, "https://relay.example/v1/responses"); !ok || !shun {
		t.Errorf("auto: %v %v, want a turn that shuns", ok, shun)
	}
	for _, u := range []string{"https://relay.example/v1/chat/completions", "https://relay.example/v1/responses/compact",
		"https://other.example/v1/responses", "https://relay.example/v2/responses"} {
		if ok, _ := turn(ctx, u); ok {
			t.Errorf("%s went over the socket", u)
		}
	}
	on := relay
	on.Socket = SocketOn
	if ok, shun := turn(on.Via(context.Background()), "https://RELAY.example/v1/responses"); !ok || shun {
		t.Errorf("on: %v %v, want a turn that never shuns", ok, shun)
	}
	off := relay
	off.Socket = ""
	if ok, _ := turn(off.Via(context.Background()), "https://relay.example/v1/responses"); ok {
		t.Error("a provider without Socket went over the socket")
	}
	if ok, _ := turn(off.Via(ctx), "https://relay.example/v1/responses"); ok {
		t.Error("a provider without Socket took the one around it")
	}
	acct := relay
	acct.Account = &Account{}
	if ok, _ := turn(acct.Via(context.Background()), "https://relay.example/v1/responses"); ok {
		t.Error("an account went over the socket")
	}
	// chatgpt.com's Codex backend always may, and shuns
	if ok, shun := turn(context.Background(), CodexBase+"/responses"); !ok || !shun {
		t.Errorf("chatgpt.com: %v %v", ok, shun)
	}
}

func TestSocketKeptAsSet(t *testing.T) {
	for v, want := range map[string]string{"auto": "auto", " On ": "on", "off": "", "": "", "maybe": ""} {
		if got := normalize(Provider{ID: "x", Socket: v}).Socket; got != want {
			t.Errorf("%q kept as %q, want %q", v, got, want)
		}
	}
}
