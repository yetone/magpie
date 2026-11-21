package provider

import (
	"context"
	"net/http"
	"strings"
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
	off.Socket = SocketOff
	if ok, _ := turn(off.Via(context.Background()), "https://relay.example/v1/responses"); ok {
		t.Error("a provider set off went over the socket")
	}
	if ok, _ := turn(off.Via(ctx), "https://relay.example/v1/responses"); ok {
		t.Error("a provider set off took the one around it")
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
	for v, want := range map[string]string{"auto": "auto", " On ": "on", "off": "off", "no": "off", "": "", "default": "", "maybe": ""} {
		if got := normalize(Provider{ID: "x", Socket: v}).Socket; got != want {
			t.Errorf("%q kept as %q, want %q", v, got, want)
		}
	}
}

// KawaiiZapic on X: OpenAI's own API takes the Responses WebSocket, as
// Codex's built-in OpenAI provider speaks it, so with Socket unset its
// turns go there (auto); set off they don't, and any other host, a
// signed-in account or plain http stays HTTP alone unless set.
func TestSocketByDefaultOnOpenAI(t *testing.T) {
	turn := func(p Provider, u string) (bool, bool) {
		t.Helper()
		req, err := http.NewRequestWithContext(p.Via(context.Background()), "POST", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return SocketTurn(req)
	}
	oai := Provider{ID: "openai", Key: "sk", Responses: "https://api.openai.com/v1"}
	if got := oai.SocketMode(); got != SocketAuto {
		t.Errorf("unset on api.openai.com: %q, want auto", got)
	}
	if ok, shun := turn(oai, "https://api.openai.com/v1/responses"); !ok || !shun {
		t.Errorf("unset on api.openai.com: %v %v, want a turn that shuns", ok, shun)
	}
	off := oai
	off.Socket = SocketOff
	if ok, _ := turn(off, "https://api.openai.com/v1/responses"); ok || off.SocketMode() != "" {
		t.Error("set off, OpenAI's turns went over the socket")
	}
	on := oai
	on.Socket = SocketOn
	if ok, shun := turn(on, "https://api.openai.com/v1/responses"); !ok || shun {
		t.Errorf("set on: %v %v", ok, shun)
	}
	for _, p := range []Provider{
		{ID: "relay", Key: "k", Responses: "https://relay.example/v1"},
		{ID: "lookalike", Key: "k", Responses: "https://api.openai.com.example/v1"},
	} {
		if p.SocketMode() != SocketAuto {
			t.Errorf("%s: %q by default, want auto", p.ID, p.SocketMode())
		}
		u := strings.TrimSuffix(p.Responses, "/") + "/responses"
		req, err := http.NewRequestWithContext(p.Via(context.Background()), "POST", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ok, shun := SocketTurn(req); !ok || !shun || !SocketRelay(req) {
			t.Errorf("%s: %v %v relay %v, want a relay's turn that shuns", p.ID, ok, shun, SocketRelay(req))
		}
		p.Socket = SocketOn
		if req, _ := http.NewRequestWithContext(p.Via(context.Background()), "POST", u, nil); SocketRelay(req) {
			t.Errorf("%s set on: held to a relay's rules", p.ID)
		}
	}
	if req, _ := http.NewRequestWithContext(oai.Via(context.Background()), "POST", "https://api.openai.com/v1/responses", nil); SocketRelay(req) {
		t.Error("OpenAI's own API held to a relay's rules")
	}
	for _, p := range []Provider{
		{ID: "plain", Key: "k", Responses: "http://relay.example/v1"},
		{ID: "acct", Responses: "https://api.openai.com/v1", Account: &Account{}},
		{ID: "remote", Key: "k", Responses: "https://magpie.example/v1", Preset: RemoteMagpiePreset},
	} {
		if p.SocketMode() != "" || p.SocketByDefault() {
			t.Errorf("%s: %q by default, want HTTP alone", p.ID, p.SocketMode())
		}
	}
}
