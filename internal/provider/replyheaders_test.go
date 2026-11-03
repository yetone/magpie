package provider

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

// #4145: the list is kept canonical and once each, never with a header
// magpie doesn't pass on; matching ignores case.
func TestReplyHeadersList(t *testing.T) {
	got := cleanReplyHeaders([]string{" x-oneapi-request-id ", "X-OneAPI-Request-ID", "set-cookie", "content-length", "access-control-allow-origin", "x-magpie-rest", "bad name", "", "x-trace"})
	if want := []string{"X-Oneapi-Request-Id", "X-Trace"}; !slices.Equal(got, want) {
		t.Errorf("cleaned %q, want %q", got, want)
	}
	for _, bad := range [][]string{{"Set-Cookie"}, {"Content-Encoding"}, {"Transfer-Encoding"}, {"Authorization"}, {"Proxy-Authenticate"}, {"X-Magpie-Note"}, {"a b"}, {"a:b"}} {
		if ReplyHeadersOK(bad) == "" {
			t.Errorf("%q passed as OK", bad)
		}
	}
	if msg := ReplyHeadersOK([]string{"x-oneapi-request-id", " ", "X-Trace"}); msg != "" {
		t.Errorf("OK list refused: %s", msg)
	}
	p := Provider{ReplyHeaders: []string{"x-trace"}}
	h := http.Header{"X-Request-Id": {"r1"}, "x-trace": {"t1"}, "Set-Cookie": {"s"}, "X-Other": {"o"}}
	out := p.PassedReplyHeaders(h)
	if len(out) != 2 || out.Get("X-Request-Id") != "r1" || out.Get("X-Trace") != "t1" {
		t.Errorf("passed %v", out)
	}
	// a list saved by hand with a denied name still passes nothing of it
	p.ReplyHeaders = []string{"Set-Cookie"}
	if out := p.PassedReplyHeaders(h); out.Get("Set-Cookie") != "" {
		t.Errorf("passed %v", out)
	}
	if (Provider{}).PassedReplyHeaders(http.Header{"X-Other": {"o"}}) != nil {
		t.Error("passed headers nobody listed")
	}
}

// A provider's request headers to pass are kept canonical and once each,
// never one that carries magpie's key or the connection's own (3jRzfF52Qx
// on X).
func TestRequestHeadersList(t *testing.T) {
	got := cleanRequestHeaders([]string{" x-tenant ", "X-TENANT", "authorization", "x-api-key", "host", "content-length", "x-magpie-agent", "bad name", "", "x-trace"})
	if want := []string{"X-Tenant", "X-Trace"}; !slices.Equal(got, want) {
		t.Errorf("cleaned %q, want %q", got, want)
	}
	for _, bad := range [][]string{{"Authorization"}, {"x-api-key"}, {"X-Goog-Api-Key"}, {"api-key"}, {"Cookie"}, {"Host"}, {"Content-Length"},
		{"Connection"}, {"Transfer-Encoding"}, {"Proxy-Authorization"}, {"Accept-Encoding"}, {"X-Magpie-Session"}, {"a b"}} {
		if RequestHeadersOK(bad) == "" {
			t.Errorf("%q passed as OK", bad)
		}
	}
	if msg := RequestHeadersOK([]string{"x-tenant", " ", "User-Agent", "Anthropic-Beta"}); msg != "" {
		t.Errorf("OK list refused: %s", msg)
	}
}

// Sign passes on what the provider lists, the agent's over the fixed
// Header, to that provider alone, and never magpie's key, even listed in a
// file written by hand (3jRzfF52Qx on X).
func TestSignPassesClientHeaders(t *testing.T) {
	in := http.Header{"X-Tenant": {"t1"}, "Authorization": {"Bearer magpie-key"}, "X-Api-Key": {"magpie-key"}, "x-route": {"r1"}}
	p := Provider{ID: "fake", Key: "sk-up", Chat: "https://up.example/v1", Headers: map[string]string{"X-Route": "fixed", "X-Only": "o"},
		PassHeaders: []string{"X-Tenant", "Authorization", "X-Api-Key", "X-Route"}}
	sign := func(p Provider, ctx context.Context) http.Header {
		req, _ := http.NewRequest("POST", "https://up.example/v1/chat/completions", nil)
		if err := p.Sign(ctx, req, Chat, nil); err != nil {
			t.Fatal(err)
		}
		return req.Header
	}
	h := sign(p, WithClientHeaders(context.Background(), "fake", in))
	if h.Get("X-Tenant") != "t1" || h.Get("X-Route") != "r1" || h.Get("X-Only") != "o" || len(h.Values("X-Route")) != 1 {
		t.Errorf("sent %v", h)
	}
	if h.Get("Authorization") != "Bearer sk-up" || h.Get("X-Api-Key") == "magpie-key" {
		t.Errorf("magpie's key passed on: %v", h)
	}
	// the agent's headers carried for another provider
	h = sign(p, WithClientHeaders(context.Background(), "other", in))
	if h.Get("X-Tenant") != "" || h.Get("X-Route") != "fixed" {
		t.Errorf("another's request passed on %v", h)
	}
	// none sent: the fixed Header goes
	h = sign(p, context.Background())
	if h.Get("X-Route") != "fixed" || h.Get("X-Tenant") != "" {
		t.Errorf("with nothing sent %v", h)
	}
}
