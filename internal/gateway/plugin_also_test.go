package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// alsoUp is an upstream that answers chat completions, Responses and
// Anthropic's messages, as Open Design's amr-link does for every model
// (plugins#888), and keeps the path each request came to.
type alsoUp struct {
	mu    sync.Mutex
	paths []string
}

func (u *alsoUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages"):
		io.WriteString(w, pluginMessagesReply)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`))
	default:
		io.WriteString(w, chatSSE("ok", "", ""))
	}
}

func (u *alsoUp) last(t *testing.T) string {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.paths) == 0 {
		t.Fatal("the plugin asked its upstream nothing")
	}
	return u.paths[len(u.paths)-1]
}

// A plugin's model its vendor serves on other APIs too (m.also: Open
// Design's amr-link serves every model on all three) takes a client's
// request on any of them as it was sent: OpenDesign's Anthropic request to
// a model declared on chat completions went upstream as chat completions
// (gaubeebangeel on X), where amr-link serves /messages as well. A model
// that says nothing more is still translated to its own API, and a request
// on an API the model isn't served on goes to its own one.
func TestPluginModelServedOnMoreAPIsKeepsTheClientAPI(t *testing.T) {
	t.Setenv("FAKE_ALSO", "1")
	up := &alsoUp{}
	pid := besideFake(t, "alsoco", up)
	anthropic := func(model string) string {
		return `{"model":"` + pid + "/" + model + `","max_tokens":64,"stream":true,"system":"You are a designer.","messages":[{"role":"user","content":"make a poster"}]}`
	}
	for _, c := range []struct {
		path, body, want string
	}{
		{"/v1/messages", anthropic("fake-also"), "/messages"},
		{"/v1/responses", `{"model":"` + pid + `/fake-also","stream":true,"input":"hi"}`, "/responses"},
		{"/v1/chat/completions", `{"model":"` + pid + `/fake-also","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/chat/completions"},
		// a model on chat completions alone is translated to it, as before
		{"/v1/messages", anthropic("fake-1"), "/chat/completions"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
		if c.path == "/v1/messages" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", c.path, c.body, rec.Code, rec.Body)
		}
		if got := up.last(t); !strings.HasSuffix(got, c.want) {
			t.Errorf("%s %.80s reached the upstream at %s, want …%s", c.path, c.body, got, c.want)
		}
	}
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	// translated, it goes to the model's own API first
	if to := New().usable(*p, "fake-also"); len(to) != 3 || to[0] != provider.Chat {
		t.Errorf("usable %v, want chat completions first of the three", to)
	}
	if apis := p.ListedAPIs("fake-also"); len(apis) != 3 || apis[0] != provider.Chat {
		t.Errorf("listed %v, want chat completions first of the three", apis)
	}
}
