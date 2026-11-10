package provider

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A Vertex AI provider given an API key is asked as express mode is: the
// key in x-goog-api-key, at the global endpoint with no project, Gemini
// models alone, and no Google credentials read or token minted. Its test
// reaches the fake Vertex AI.
func TestVertexExpressKey(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	if err := Save(Provider{ID: "vertex-express", Name: "Vertex express", Preset: VertexPreset, Key: "  AQ.Ab8RN6-express  "}); err != nil {
		t.Fatalf("a key and no project: %v", err)
	}
	p, err := Find("vertex-express")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "AQ.Ab8RN6-express" || !p.Ready() || !p.VertexExpress() || !p.On() {
		t.Fatalf("key %q, ready %v, express %v", p.Key, p.Ready(), p.VertexExpress())
	}
	if got := p.Base(Gemini); got != f.url+"/vertex/v1" {
		t.Fatalf("base %s", got)
	}
	if p.Base(Chat) != "" || p.Base(Anthropic) != "" || !slices.Equal(p.Speaks(), []Protocol{Gemini}) {
		t.Fatalf("express serves Gemini alone: chat %q, anthropic %q, speaks %v", p.Base(Chat), p.Base(Anthropic), p.Speaks())
	}
	for _, m := range p.Exposed() {
		if VertexPartner(m.ID) || VertexClaude(m.ID) {
			t.Errorf("express lists %s", m.ID)
		}
	}
	if len(p.Exposed()) == 0 {
		t.Fatal("no models")
	}

	req, _ := http.NewRequest(http.MethodPost, p.Base(Gemini)+VertexPath("gemini-3.5-flash"), strings.NewReader("{}"))
	if err := p.Sign(context.Background(), req, Gemini, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-goog-api-key") != "AQ.Ab8RN6-express" || req.Header.Get("Authorization") != "" || req.URL.Query().Get("key") != "" {
		t.Fatalf("signed with %v, %s", req.Header, req.URL)
	}
	if want := f.url + "/vertex/v1/publishers/google/models/gemini-3.5-flash:streamGenerateContent?alt=sse"; req.URL.String() != want {
		t.Fatalf("asked at %s", req.URL)
	}

	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK || rs[0].Protocol != Gemini {
		t.Fatalf("test: %+v", rs)
	}
	calls := f.vertexCalls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].path, "/vertex/v1/publishers/google/models/") || !strings.HasSuffix(calls[0].path, ":generateContent") ||
		calls[0].header.Get("x-goog-api-key") != "AQ.Ab8RN6-express" || calls[0].header.Get("Authorization") != "" {
		t.Fatalf("test calls %+v", calls)
	}
	if n := len(f.tokenCalls()); n != 0 {
		t.Fatalf("%d Google tokens minted for a key", n)
	}
}

// A key with a project is asked at that project's address, signed by the
// key; a base URL takes the place of Google's host, kept without its /v1,
// for both; and the key goes nowhere but where the provider is asked.
func TestVertexExpressBaseURL(t *testing.T) {
	vertexHome(t)
	newVertexGoogle(t)
	ctx := context.Background()
	sign := func(p Provider) *http.Request {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, p.Base(Gemini)+VertexPath("gemini-2.5-pro"), strings.NewReader("{}"))
		if err := p.Sign(ctx, req, Gemini, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		return req
	}
	if err := Save(Provider{ID: "vertex-proxy", Name: "Vertex proxy", Preset: VertexPreset, Key: "AQ.Ab8RN6-proxy",
		Vertex: &Vertex{BaseURL: " proxy.example.com/v1/ "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("vertex-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if p.Vertex.BaseURL != "https://proxy.example.com" {
		t.Fatalf("kept %q", p.Vertex.BaseURL)
	}
	if got := sign(*p).URL.String(); got != "https://proxy.example.com/v1/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
		t.Fatalf("asked at %s", got)
	}
	// with a project: its address, on the proxy, signed by the key
	q := *p
	q.Vertex = &Vertex{Project: "acme-vertex", Location: "us-central1", BaseURL: "https://proxy.example.com"}
	r := sign(q)
	if r.URL.String() != "https://proxy.example.com/v1/projects/acme-vertex/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse" ||
		r.Header.Get("x-goog-api-key") != "AQ.Ab8RN6-proxy" || q.VertexExpress() {
		t.Fatalf("%s %v", r.URL, r.Header)
	}
	// the key is not sent to an address that isn't the provider's
	req, _ := http.NewRequest(http.MethodPost, "https://evil.example.com/v1/publishers/google/models/x:generateContent", strings.NewReader("{}"))
	if err := p.Sign(ctx, req, Gemini, nil); err == nil || req.Header.Get("x-goog-api-key") != "" {
		t.Fatalf("signed for another host: %v %v", err, req.Header)
	}
	// without the key the proxy is not asked: Google's token would go there
	nokey := *p
	nokey.Key = ""
	if nokey.Base(Gemini) != "" || nokey.Ready() {
		t.Fatalf("a base URL with no key: %q, ready %v", nokey.Base(Gemini), nokey.Ready())
	}
}
