package gateway

import (
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// jjt on Discord: the ChatGPT backend says "auto" in response.created and
// "default" in response.completed whether priority ran or not (a Plus
// account's Fast turn on gpt-6.1-sol decoded at ~200 tok/s, one without at
// ~110, both "default"). The served tier is kept unknown there, on a
// routed try and on Codex's own sign-in relayed, never as "default". The
// events are jjt's real-priority.sse's, cut to the fields read.
func TestChatGPTBackendTierUnknown(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil, func(string) (string, string) { return "auto", "default" })
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	last := func() usage.Record {
		t.Helper()
		recs := usage.Load(time.Time{})
		if len(recs) == 0 {
			t.Fatal("no usage")
		}
		return recs[len(recs)-1]
	}

	for _, tier := range []string{"priority", ""} {
		ask := `{"model":"codex/gpt-6-luna","input":"ping"}`
		if tier != "" {
			ask = `{"model":"codex/gpt-6-luna","input":"ping","service_tier":"` + tier + `"}`
		}
		r := postFrom(t, s, "omp", "/v1/responses", ask)
		tried()
		if sr := sessionRouteOf(r); sr.TierServed != "" || len(sr.Tries) == 0 || sr.Tries[len(sr.Tries)-1].TierServed != "" {
			t.Errorf("%q route: %+v", tier, sr)
		}
		if rec := last(); rec.TierSent != tier || rec.TierServed != "" {
			t.Errorf("%q usage: sent %q served %q", tier, rec.TierSent, rec.TierServed)
		}
	}

	// Codex's own sign-in, relayed as it sent it
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-luna","stream":true,"input":"ping","service_tier":"priority"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-plus")
	req.Header.Set("User-Agent", "codex_cli_rs/0.160.1")
	s.Handler().ServeHTTP(rec, req)
	tried()
	if got := last(); rec.Code != 200 || got.TierSent != "priority" || got.TierServed != "" {
		t.Errorf("codex relay: %d sent %q served %q", rec.Code, got.TierSent, got.TierServed)
	}
}

// OpenAI's API means its service_tier: "default" and "priority" are kept
// as it said, from a provider at api.openai.com and from Codex signed in
// with an API key, relayed there.
func TestOpenAIAPITierKept(t *testing.T) {
	f := &fake{t: t}
	up := setup(t, provider.Responses, f)
	f.reply = "" // set per request below
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: "https://api.openai.com/v1"}); err != nil {
		t.Fatal(err)
	}
	to, _ := url.Parse(up.URL)
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.openai.com" {
			t.Errorf("request to %s", r.URL)
		}
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	reply := func(tier string) string {
		return sse(
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"r1","object":"response","status":"in_progress","model":"m1","usage":null,"service_tier":"auto"}}`,
			`data: {"type":"response.output_text.delta","delta":"ok"}`,
			`data: {"type":"response.completed","sequence_number":9,"response":{"id":"r1","object":"response","status":"completed","model":"m1","usage":{"input_tokens":1,"output_tokens":1},"service_tier":"`+cmp.Or(tier, "default")+`"}}`)
	}
	last := func() usage.Record {
		t.Helper()
		recs := usage.Load(time.Time{})
		if len(recs) == 0 {
			t.Fatal("no usage")
		}
		return recs[len(recs)-1]
	}
	for _, c := range []struct{ asked, want string }{{"priority", "priority"}, {"", "default"}} {
		f.reply = reply(c.asked)
		ask := map[string]any{"model": "fake/m1", "input": "ping", "stream": true}
		if c.asked != "" {
			ask["service_tier"] = c.asked
		}
		b, _ := json.Marshal(ask)
		r := postFrom(t, s, "omp", "/v1/responses", string(b))
		if sr := sessionRouteOf(r); sr.TierServed != c.want {
			t.Errorf("%q route: %+v", c.asked, sr)
		}
		if rec := last(); rec.TierServed != c.want {
			t.Errorf("%q usage: served %q, want %q", c.asked, rec.TierServed, c.want)
		}

		// Codex with an API key, relayed to OpenAI's API
		f.reply = reply(c.asked)
		ask = map[string]any{"model": "m1", "input": "ping", "stream": true}
		if c.asked != "" {
			ask["service_tier"] = c.asked
		}
		b, _ = json.Marshal(ask)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer sk-test")
		req.Header.Set("User-Agent", "codex_cli_rs/0.160.1")
		s.Handler().ServeHTTP(rec, req)
		if got := last(); rec.Code != 200 || got.TierServed != c.want {
			body, _ := io.ReadAll(rec.Body)
			t.Errorf("%q codex api key: %d served %q, want %q: %s", c.asked, rec.Code, got.TierServed, c.want, body)
		}
	}
}

// Anotiaww on X: a Codex /fast turn (service_tier "priority") on Codex's
// own sign-in showed nothing of it on the Routing page. The relayed
// route, and its try, says the tier the body went out at, the backend's
// "auto"/"default" left unknown; a turn without /fast says none.
func TestCodexRelayRouteSaysTier(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","service_tier":"auto"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","service_tier":"default","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	s := New()
	for i, tier := range []string{"priority", ""} {
		body := `{"model":"gpt-5.5","stream":true,"input":"hi"}`
		if tier != "" {
			body = `{"model":"gpt-5.5","stream":true,"input":"hi","service_tier":"` + tier + `"}`
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%q: %d %s", tier, rec.Code, rec.Body)
		}
		st := s.Trace(t.Context(), 0, 0)
		if len(st.Routes) != i+1 {
			t.Fatalf("%q routes: %+v", tier, st.Routes)
		}
		r := st.Routes[len(st.Routes)-1] // in the order they came
		if r.Order[0].Who != "Codex's own sign-in" || r.Tier != tier || len(r.Tries) != 1 || !r.Tries[0].Done || r.Tries[0].Tier != tier || r.Tries[0].TierServed != "" {
			t.Errorf("%q route: tier %q order %+v tries %+v", tier, r.Tier, r.Order, r.Tries)
		}
		if sr := sessionRouteOf(r); sr.Tier != tier || sr.AskedTier != tier || sr.TierServed != "" {
			t.Errorf("%q session route: %+v", tier, sr)
		}
	}
}
