package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// A relay given without its /v1 lists its models under /v1 only; fetching
// them sets the base right, so chat goes to /v1/chat/completions too. One
// that answers at the base as written is left alone.
func TestFetchAddsMissingV1(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	v1only := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-5.5"}]}`))
	}))
	defer v1only.Close()
	both := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"gpt-5.5"}]}`))
	}))
	defer both.Close()

	for _, p := range []Provider{
		{ID: "relay", Name: "Relay", Chat: v1only.URL + "/", Responses: v1only.URL, Anthropic: v1only.URL, Key: "sk-x"},
		{ID: "root", Name: "Root", Chat: both.URL, Key: "sk-y"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"relay", "root"} {
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	p, _ := Find("relay")
	if p.Chat != v1only.URL+"/v1" || p.Responses != v1only.URL+"/v1" {
		t.Errorf("relay: chat %q responses %q, want …/v1", p.Chat, p.Responses)
	}
	if p.Anthropic != v1only.URL {
		t.Errorf("relay: anthropic base changed to %q; it has no /v1", p.Anthropic)
	}
	if q, _ := Find("root"); q.Chat != both.URL {
		t.Errorf("root: chat changed to %q", q.Chat)
	}
}

// tcdw's report: a custom provider at Ark's Agent Plan, …/api/plan/v3,
// was asked for …/api/plan/v3/v1/models and shown only that URL's
// failure. Its base is now asked as written, a failure names it and says
// the ids can be typed in, and the base is never given a /v1 — not even by
// an endpoint that would answer under one.
func TestArkBaseKeepsItsPath(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/api/plan/v3/chat/completions":
			w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		case "/api/plan/v3/v1/models": // a lenient router
			w.Write([]byte(`{"data":[{"id":"doubao-seed-2.1-pro"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	base := srv.URL + "/api/plan/v3"
	if err := Save(Provider{ID: "ark", Name: "Ark", Chat: base, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("ark")
	_, err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("listed from …/v3/v1/models")
	}
	if msg := err.Error(); !strings.Contains(msg, base+"/models: 404") || strings.Contains(msg, "/v3/v1/") || !strings.Contains(msg, "by hand") {
		t.Errorf("error: %s", msg)
	}
	if strings.Join(asked, " ") != "/api/plan/v3/models" {
		t.Errorf("asked %v", asked)
	}
	if q, _ := Find("ark"); q.Chat != base {
		t.Errorf("chat base changed to %q", q.Chat)
	}
	// even told /v1/models answered, a versioned base is left as it is
	if got := p.fixV1(base, base+"/v1/models"); got != base {
		t.Errorf("fixV1: %q", got)
	}
	// the ids typed in are what agents see
	p.Models = []string{"doubao-seed-2.1-pro"}
	if ms := p.Exposed(); len(ms) != 1 || ms[0].ID != "doubao-seed-2.1-pro" {
		t.Errorf("exposed %+v", ms)
	}
}
