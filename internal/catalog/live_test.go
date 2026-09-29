package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointAPIs(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"/messages"}, "anthropic"},
		{[]string{"/v1/messages", "/chat/completions", "ws:/responses", "/responses"}, "anthropic,chat,responses"},
		{[]string{"/v1/chat/completions/", "/chat/completions"}, "chat"},
		{[]string{"/embeddings"}, ""},
		{nil, ""},
	} {
		if got := strings.Join(EndpointAPIs(c.in), ","); got != c.want {
			t.Errorf("%v: %q", c.in, got)
		}
	}
}

// A list that tells each model's context window (Command Code, OpenRouter)
// has it kept; an odd value is only left out.
func TestFetchContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"object":"list","data":[{"id":"claude-sonnet-5","name":"Claude Sonnet 5","context_length":1000000,"supported_endpoints":["/messages"]},{"id":"kimi-k3","context_length":"lots"},{"id":"glm-5"}]}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range ms {
		got[m.ID] = m.Context
	}
	if len(got) != 3 || got["claude-sonnet-5"] != 1000000 || got["kimi-k3"] != 0 || got["glm-5"] != 0 {
		t.Errorf("contexts %v", got)
	}
}

// A base with a version in its path (Ark's /api/plan/v3, tcdw's report) is
// asked for its list only as written: no /v1 is put on it, and a failure
// names the URL asked and what the vendor said. A base without one is
// still looked around for its /v1.
func TestFetchAtVersionedBase(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path == "/v1/models" || r.URL.Path == "/api/plan/v3/v1/models" {
			rw.Write([]byte(`{"data":[{"id":"doubao-seed-2.1-pro"}]}`))
			return
		}
		rw.WriteHeader(http.StatusUnauthorized)
		rw.Write([]byte(`{"error":{"code":"AuthenticationError","message":"The API key doesn't exist.","type":"Unauthorized"}}`))
	}))
	defer srv.Close()

	_, _, err := FetchAt(context.Background(), srv.URL+"/api/plan/v3/", "k", false, nil)
	if err == nil {
		t.Fatal("listed from under a /v1 the base doesn't have")
	}
	if strings.Join(asked, " ") != "/api/plan/v3/models" {
		t.Errorf("asked %v", asked)
	}
	if msg := err.Error(); !strings.Contains(msg, srv.URL+"/api/plan/v3/models: 401") || !strings.Contains(msg, "The API key doesn't exist.") || strings.Contains(msg, "/v3/v1/") {
		t.Errorf("error: %s", msg)
	}

	if _, at, err := FetchAt(context.Background(), srv.URL, "k", false, nil); err != nil || at != srv.URL+"/v1/models" {
		t.Errorf("bare host: %q %v", at, err)
	}
	// an Anthropic base is the root its /v1 goes after
	if _, at, err := FetchAt(context.Background(), srv.URL+"/api/plan/v3", "k", true, nil); err != nil || at != srv.URL+"/api/plan/v3/v1/models" {
		t.Errorf("anthropic: %q %v", at, err)
	}

	for u, want := range map[string]bool{
		"https://ark.cn-beijing.volces.com/api/plan/v3":           true,
		"https://open.bigmodel.cn/api/paas/v4":                    true,
		"https://generativelanguage.googleapis.com/v1beta/openai": true,
		"https://api.openai.com/v1":                               true,
		"https://openrouter.ai/api":                               false,
		"https://relay.example.com":                               false,
		"https://api.stepfun.ai/step_plan":                        false,
		"https://vault.example.com/v":                             false,
	} {
		if Versioned(u) != want {
			t.Errorf("Versioned(%q) = %v", u, !want)
		}
	}
}
