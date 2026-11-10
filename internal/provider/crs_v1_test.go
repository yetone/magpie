package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// crsRelay answers as claude-relay-service routes (src/app.js): OpenAI's
// chat at /openai/v1/chat/completions (and /api/v1/…), Codex's Responses
// at /openai/responses and /openai/v1/responses, the model list at
// /api/v1/models alone, and its own 404 body for anything else.
func crsRelay(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /openai/v1/chat/completions", "POST /api/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
		case "POST /openai/responses", "POST /openai/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`))
		case "GET /api/v1/models", "GET /claude/v1/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model"},{"id":"claude-sonnet-4-5-20250929","object":"model"}]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"Not Found","message":"Route ` + r.URL.Path + ` not found","timestamp":"2026-11-10T00:00:00.000Z"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func crsHome(t *testing.T) {
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
}

// Cyrus_4433 on X: a claude-relay-service added as OpenAI compatible at
// http://host:port/openai, models typed in by hand, and Test models said
// "gpt-6.1-sol: 404 · Not Found" — magpie asked …/openai/chat/completions,
// which CRS doesn't serve. The model is now asked at …/openai/v1 too, its
// answer there is the model's, and the base is saved with the /v1.
func TestCRSOpenAIBaseTestsUnderV1(t *testing.T) {
	crsHome(t)
	srv, _ := crsRelay(t)
	base := srv.URL + "/openai"
	if err := Save(Provider{ID: "my-relay", Name: "My Relay", Chat: base, Key: "cr_1x4209", Models: []string{"gpt-6.1-sol", "gpt-6-sol"}, ListOff: true}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	rs := p.TestModels(context.Background(), []string{"gpt-6.1-sol", "gpt-6-sol"})
	for _, r := range rs {
		if !r.OK {
			t.Errorf("%s: %d %s", r.Model, r.Status, r.Error)
		}
	}
	if rs[0].V1 != base+"/v1" {
		t.Errorf("v1 %q", rs[0].V1)
	}
	if q, _ := Find("my-relay"); q.Chat != base+"/v1" {
		t.Errorf("chat base %q, want %s/v1", q.Chat, base)
	}
}

// Its model list is at the root's /api/v1/models: fetched there, and the
// base given its /v1 by it, before any test.
func TestCRSOpenAIBaseListsAtAPI(t *testing.T) {
	crsHome(t)
	srv, asked := crsRelay(t)
	base := srv.URL + "/openai"
	if err := Save(Provider{ID: "my-relay", Name: "My Relay", Chat: base, Key: "cr_1x4209"}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("my-relay")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v (asked %v)", err, asked())
	}
	if len(ms) == 0 {
		t.Fatal("no models")
	}
	if q, _ := Find("my-relay"); q.Chat != base+"/v1" {
		t.Errorf("chat base %q, want %s/v1", q.Chat, base)
	}
}

// Detect at the URL as typed finds chat under its /v1 and says that base.
func TestCRSOpenAIBaseDetectsV1(t *testing.T) {
	crsHome(t)
	srv, _ := crsRelay(t)
	base := srv.URL + "/openai"
	ds, err := Provider{ID: "new-relay", Name: "New", Key: "cr_1x4209"}.Detect(context.Background(), base, "gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Protocol == Chat && (!d.OK || d.Base != base+"/v1") {
			t.Errorf("chat: ok %v base %q error %q", d.OK, d.Base, d.Error)
		}
	}
}

// A 404 that is the model's (a relay that doesn't serve it, at the right
// path) is the answer: /v1 is asked, answers 404 too, and the base stays.
func TestModel404KeepsBase(t *testing.T) {
	crsHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if strings.HasPrefix(r.URL.Path, "/relay/v1/") {
			w.Write([]byte(`{"error":"Not Found"}`))
			return
		}
		w.Write([]byte(`{"error":{"message":"model gpt-x not found"}}`))
	}))
	defer srv.Close()
	base := srv.URL + "/relay"
	if err := Save(Provider{ID: "r", Name: "R", Chat: base, Key: "k", Models: []string{"gpt-x"}, ListOff: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("r")
	r := p.TestModels(context.Background(), []string{"gpt-x"})[0]
	if r.OK || r.Status != 404 || !strings.Contains(r.Error, "gpt-x") || r.V1 != "" {
		t.Errorf("%+v", r)
	}
	if q, _ := Find("r"); q.Chat != base {
		t.Errorf("chat base changed to %q", q.Chat)
	}
}
