package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Axel Burks on Discord: a gateway in front of several Azure accounts
// routes a conversation by a session id in a header. ${sessionId} and
// ${threadId} in a provider's Headers are the conversation's, as in its
// extraBody; Test and Detect send their own test session, the same one in
// the header as in the body; a model list sends an id of its own. A
// literal placeholder never goes out.
func TestHeadersTakeTheConversation(t *testing.T) {
	detectHome(t)
	const model = "gpt-test"
	var mu sync.Mutex
	type seen struct{ path, session, route, plain, key string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"prompt_cache_key"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		got = append(got, seen{r.Method + " " + r.URL.Path, r.Header.Get("X-Session-Id"), r.Header.Get("X-Route"), r.Header.Get("X-Plain"), in.Key})
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			io.WriteString(w, `{"data":[{"id":"`+model+`"}]}`)
		case "POST /v1/responses":
			io.WriteString(w, `{"object":"response","status":"completed","output":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "azgw", Key: "sk-gw", Responses: srv.URL + "/v1", Models: []string{model},
		Headers:   map[string]string{"X-Session-Id": "${sessionId}", "X-Route": "s=${sessionId};t=${threadId}", "X-Plain": "fixed"},
		ExtraBody: ExtraBody{"prompt_cache_key": json.RawMessage(`"${sessionId}"`)}}
	ctx := context.Background()
	if rs := p.Test(ctx); len(rs) != 1 || !rs[0].OK {
		t.Fatalf("Test: %+v", rs)
	}
	if rs := p.TestModels(ctx, []string{model}); len(rs) != 1 || !rs[0].OK {
		t.Fatalf("TestModels: %+v", rs)
	}
	if rs, err := p.Detect(ctx, srv.URL, model); err != nil || len(rs) != len(detectProtocols) || !rs[1].Result.OK {
		t.Fatalf("Detect: %v, %+v", err, rs)
	}
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	asked, listed := 0, 0
	tests := map[string]bool{}
	for _, s := range got {
		if strings.Contains(s.session+s.route, "${") || s.plain != "fixed" {
			t.Errorf("%s sent %+v", s.path, s)
		}
		switch s.path {
		case "POST /v1/responses":
			asked++
			if !strings.HasPrefix(s.session, "magpie-test-") || s.session != s.key || s.route != "s="+s.key+";t="+s.key {
				t.Errorf("a test's header isn't its body's session: %+v", s)
			}
			tests[s.session] = true
		case "GET /v1/models":
			listed++
			if !strings.HasPrefix(s.session, "magpie-") || strings.HasPrefix(s.session, "magpie-test-") || s.route != "s="+s.session+";t="+s.session {
				t.Errorf("a list's header: %+v", s)
			}
		}
	}
	if asked != 3 || len(tests) != 3 || listed == 0 {
		t.Errorf("asked %d on %d sessions, listed %d: %+v", asked, len(tests), listed, got)
	}
}

// An id is the agent's: in a header it goes without CR, LF or any other
// control character, which would split the header or fail the request.
// ${threadId} is the session's where no thread is named; a request that
// is no turn of a conversation gets an id of its own; headers without a
// placeholder go as written.
func TestSentHeaders(t *testing.T) {
	p := Provider{Headers: map[string]string{"X-S": "${sessionId}", "X-T": "${threadId}", "X-P": "a${b}"}}
	ctx := WithConversation(context.Background(), Conversation{Session: "s-1\r\nX-Evil: 1", Thread: "t\t\x00\x7f-2"})
	h := p.SentHeaders(ctx)
	if h["X-S"] != "s-1X-Evil: 1" || h["X-T"] != "t-2" || h["X-P"] != "a${b}" {
		t.Errorf("sent %q", h)
	}
	if p.Headers["X-S"] != "${sessionId}" {
		t.Errorf("the saved headers changed: %q", p.Headers)
	}
	h = p.SentHeaders(WithConversation(context.Background(), Conversation{Session: "s-2"}))
	if h["X-S"] != "s-2" || h["X-T"] != "s-2" {
		t.Errorf("no thread: %q", h)
	}
	a, b := p.SentHeaders(context.Background()), p.SentHeaders(context.Background())
	if !strings.HasPrefix(a["X-S"], "magpie-") || len(a["X-S"]) != len("magpie-")+12 || a["X-T"] != a["X-S"] || a["X-S"] == b["X-S"] {
		t.Errorf("own ids %q %q", a, b)
	}
	plain := Provider{Headers: map[string]string{"X-P": "v"}}
	if h := plain.SentHeaders(ctx); h["X-P"] != "v" || len(h) != 1 {
		t.Errorf("plain %q", h)
	}

	// signed and sent: the request goes, its header clean
	var heard http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { heard = r.Header }))
	defer srv.Close()
	q := Provider{ID: "x", Key: "k", Chat: srv.URL + "/v1", Headers: p.Headers}
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/chat/completions", strings.NewReader("{}"))
	if err := q.Sign(ctx, req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sent: %v", err)
	}
	res.Body.Close()
	if heard.Get("X-S") != "s-1X-Evil: 1" || heard.Get("X-Evil") != "" {
		t.Errorf("heard %q", heard)
	}
}

// ${threadId} in extraBody is the thread's, else the session's.
func TestExtraBodyThread(t *testing.T) {
	p := Provider{ExtraBody: ExtraBody{"metadata": json.RawMessage(`{"s":"${sessionId}","t":"${threadId}"}`)}}
	out := string(p.WithExtraBody([]byte(`{"model":"m"}`), Conversation{Session: "root", Thread: "worker"}))
	if !strings.Contains(out, `"s":"root"`) || !strings.Contains(out, `"t":"worker"`) {
		t.Errorf("%s", out)
	}
	out = string(p.WithExtraBody([]byte(`{"model":"m"}`), Conversation{Session: "root"}))
	if !strings.Contains(out, `"t":"root"`) {
		t.Errorf("%s", out)
	}
	if p.AsksThread(context.Background()) == false || (Provider{Headers: map[string]string{"a": "${sessionId}"}}).AsksThread(context.Background()) {
		t.Error("AsksThread")
	}
}
