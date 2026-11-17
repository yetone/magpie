package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Archivemylife on X: a Responses request for a Codex account's model with
// service_tier "fast", Codex's own name for its Fast mode, reached the
// ChatGPT backend with no tier at all. Codex sends Fast as "priority"
// (ServiceTier::request_value in openai/codex), so "fast" goes as
// "priority" on every OpenAI API it can arrive on, and the route says the
// tier was asked; Claude's speed "fast" already did. Another tier, and a
// request with none, goes as before.
func TestFastTierWordReachesCodex(t *testing.T) {
	codexSignedIn(t)
	d := daybreak(t)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	const m = `"model":"codex/gpt-daybreak-blue-latest"`
	const msgs = `"messages":[{"role":"user","content":"Respond with OK."}]`
	for _, c := range []struct{ name, path, body, want string }{
		{"Responses, fast", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"fast"}`, "priority"},
		{"Responses, whole, fast", "/v1/responses", `{` + m + `,"input":"Respond with OK.","service_tier":"fast"}`, "priority"},
		{"Responses, priority", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"priority"}`, "priority"},
		{"Chat, fast", "/v1/chat/completions", `{` + m + `,"stream":true,` + msgs + `,"service_tier":"fast"}`, "priority"},
		{"Chat, whole, fast", "/v1/chat/completions", `{` + m + `,` + msgs + `,"service_tier":"fast"}`, "priority"},
		{"Messages, speed fast", "/v1/messages", `{` + m + `,"max_tokens":64,"stream":true,` + msgs + `,"speed":"fast"}`, "priority"},
		{"Responses, flex", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"flex"}`, ""},
		{"Responses, none", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true}`, ""},
		{"Chat, none", "/v1/chat/completions", `{` + m + `,"stream":true,` + msgs + `}`, ""},
	} {
		r := postFrom(t, s, "opencode", c.path, c.body)
		b := d.last(t)
		if got := gjson.GetBytes(b, "service_tier").String(); got != c.want {
			t.Errorf("%s: the backend got service_tier %q, want %q: %s", c.name, got, c.want, b)
		}
		if c.want != "" && r.Tier != "priority" && r.Tier != "fast" {
			t.Errorf("%s: the route says tier %q was asked", c.name, r.Tier)
		}
	}
}

// fastTier changes "fast" alone, and only as a top-level string; every
// other body comes back byte for byte.
func TestFastTier(t *testing.T) {
	if got := gjson.GetBytes(fastTier([]byte(`{"model":"m","service_tier":"fast","input":"hi"}`)), "service_tier").String(); got != "priority" {
		t.Errorf("fast went as %q", got)
	}
	for _, in := range []string{
		`{"model":"m","service_tier":"priority"}`,
		`{"model":"m","service_tier":"flex"}`,
		`{"model":"m","service_tier":"Fast"}`,
		`{"model":"m","metadata":{"service_tier":"fast"}}`,
		`{"model":"m","service_tier":["fast"]}`,
		`{"model":"m"}`,
		`not json`,
	} {
		if got := string(fastTier([]byte(in))); got != in {
			t.Errorf("fastTier(%s) = %s", in, got)
		}
	}
}
