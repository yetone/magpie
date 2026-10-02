package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A ClinePass free model's test is answered as an agent's request to it is
// (ARNO on Discord: 右击检测 cline-free/… 没有响应: 500 · empty response
// content, while agents were answered). The fake Cline API does what the
// real one did: a free model that thinks first, asked for 16 tokens without
// a stream, has nothing to say and is answered 500; asked as Cline's
// clients ask — streamed, with room — it thinks, then answers.
func TestClineModelTestStreams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	var mu sync.Mutex
	var got []map[string]any
	var heads []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" {
			http.NotFound(rw, r)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		got, heads = append(got, in), append(heads, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("X-CLIENT-TYPE") != "cline-desktop" {
			rw.WriteHeader(http.StatusForbidden)
			_, _ = rw.Write([]byte(`{"error":{"message":"only available via Cline product surfaces"}}`))
			return
		}
		room, _ := in["max_tokens"].(float64)
		if in["stream"] != true || room < 256 {
			rw.WriteHeader(http.StatusInternalServerError)
			_, _ = rw.Write([]byte(`{"error":{"message":"empty response content"}}`))
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning":"The user says hi."}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Hello!"}}]}`, `[DONE]`} {
			fmt.Fprintf(rw, "data: %s\n\n", d)
		}
	}))
	defer srv.Close()
	p, err := FromPreset("clinepass")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key, p.Chat = "cline", "clp_key", srv.URL+"/api/v1"
	p.Models = []string{"cline-free/deepseek-v4.1-flash"}
	rs := p.TestModels(context.Background(), []string{"cline-free/deepseek-v4.1-flash", "cline-pass/glm-5.3"})
	for _, r := range rs {
		if !r.OK {
			t.Errorf("%s: %+v", r.Model, r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("requests %v", got)
	}
	for i, in := range got {
		if in["stream"] != true || in["max_tokens"] != float64(1024) {
			t.Errorf("request %v", in)
		}
		if heads[i].Get("Authorization") != "Bearer clp_key" || heads[i].Get("User-Agent") != "Cline/"+ClineVersion {
			t.Errorf("headers %v", heads[i])
		}
	}
}

// A streamed test is read to the model's first word: a stream answered 200
// that then fails says what failed, one that thinks or answers is
// answered, and a request that doesn't stream is taken at its status, as
// before.
func TestProbeReadsTheStream(t *testing.T) {
	streams := map[string][]string{
		"fails":     {`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, `{"error":{"message":"empty response content","code":500}}`},
		"thinks":    {`{"choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`, `{"error":{"message":"late"}}`},
		"says":      {`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`, `[DONE]`},
		"anthropic": {`{"type":"message_start","message":{}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		"responses": {`{"type":"response.created","response":{}}`, `{"type":"response.failed","response":{"error":{"code":"server_error","message":"model fell over"}}}`},
		"quiet":     {`{"choices":[{"index":0,"delta":{}}]}`, `[DONE]`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		for _, d := range streams[strings.TrimPrefix(r.URL.Path, "/")] {
			fmt.Fprintf(rw, "data: %s\n\n", d)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL}
	want := map[string]string{
		"fails": "empty response content", "thinks": "", "says": "", "quiet": "",
		"anthropic": "Overloaded", "responses": "model fell over",
	}
	for name, msg := range want {
		r := probe(context.Background(), p, Chat, srv.URL+"/"+name, []byte(`{"model":"m","stream":true}`), "m", testWait)
		if r.OK != (msg == "") || r.Error != msg {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := probe(context.Background(), p, Chat, srv.URL+"/fails", []byte(`{"model":"m"}`), "m", testWait); !r.OK {
		t.Errorf("not streamed: %+v", r)
	}
}

// Which providers' models can each be tested, and why not (ARNO on
// Discord: 有些provider里的模型可以右击检测，有些却不可以): a key's, and a
// sign-in's sent requests at an endpoint (Claude, Codex, Copilot), can; a
// classifier can't, nor a sign-in reached only through its agent's own API.
func TestModelTest(t *testing.T) {
	for _, c := range []struct {
		p    Provider
		want string
	}{
		{Provider{ID: "relay", Chat: "https://relay.test/v1"}, ""},
		{Provider{ID: "claude", Anthropic: "https://api.anthropic.com", Account: &Account{Agent: "claude"}}, ""},
		{Provider{ID: "codex", Responses: CodexBase, Account: &Account{Agent: "codex"}}, ""},
		{Provider{ID: "copilot", Chat: "https://api.githubcopilot.com", Account: &Account{Agent: "copilot"}}, ""},
		{Provider{ID: "kiro", Key: "k", Account: &Account{Agent: "kiro"}}, "own-api"},
		{Provider{ID: "cursor", Account: &Account{Agent: "cursor"}}, "own-api"},
		{Provider{ID: "jev", Decide: "https://decide.test/v1"}, "decide"},
	} {
		if got := c.p.ModelTest(); got != c.want {
			t.Errorf("%s: %q, want %q", c.p.ID, got, c.want)
		}
	}
	r := Provider{ID: "jev", Decide: "https://decide.test/v1", Chat: "https://decide.test/v1"}.TestModels(context.Background(), []string{"jev-latest"})[0]
	if r.OK || r.Error == "" {
		t.Errorf("a classifier's model was tested: %+v", r)
	}
}
