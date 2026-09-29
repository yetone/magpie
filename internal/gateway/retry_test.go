package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func init() { retryPause = time.Millisecond }

// scripted answers each request with the next of its replies, the last
// one over and over.
type scripted struct {
	replies []reply
	n       int
	hang    chan struct{} // when set, the first request waits on it
}

type reply struct {
	code  int
	ctype string
	body  string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	i := min(s.n, len(s.replies)-1)
	s.n++
	if s.hang != nil && i == 0 {
		<-s.hang
	}
	x := s.replies[i]
	if x.ctype == "" {
		x.ctype = "application/json"
	}
	w.Header().Set("Content-Type", x.ctype)
	if x.code != 0 {
		w.WriteHeader(x.code)
	}
	io.WriteString(w, x.body)
}

const chatOK = `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

func scriptedOn(t *testing.T, id string, proto provider.Protocol, s *scripted) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}}
	switch proto {
	case provider.Anthropic:
		p.Anthropic = up.URL
	default:
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// A provider that can't serve the model — turned away, not the request at
// fault — hands the request to the next member rather than to the agent.
func TestGroupFailsOverAnUnservedModel(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", `{"error":{"message":"model \"gpt-6-sol\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, `{"error":{"message":"invalid api key"}}`, true},
		{404, `{"error":{"message":"not found"}}`, true},
		{400, `{"error":{"message":"The model gpt-x does not exist"}}`, true},
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{400, `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`, false},
	} {
		if got := retryable(x.status, []byte(x.body)); got != x.want {
			t.Errorf("retryable(%d, %s) = %v", x.status, x.body, got)
		}
	}
}

// An error a stream begins with, before any of its content, is a failure
// the next one answers, as an error status is; one that comes after
// content has begun reaches the agent as it came.
func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	fresh(t)
	overloaded := sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","content":[],"usage":{"input_tokens":3}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	good := sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m2","role":"assistant","content":[],"usage":{"input_tokens":3}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	a := &scripted{replies: []reply{{200, "text/event-stream", overloaded}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", good}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
			strings.NewReader(`{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		return rec.Code, rec.Body.String()
	}
	code, body := send()
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "Overloaded") || strings.Contains(body, `"m1"`) {
		t.Fatalf("%d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil {
		t.Fatalf("tries: %+v", r.Tries)
	}

	// after content, the error is the agent's to see
	fresh(t)
	late := sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","content":[]}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	a = &scripted{replies: []reply{{200, "text/event-stream", late}}}
	b = &scripted{replies: []reply{{200, "text/event-stream", good}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered})
	s = New()
	if code, body := send(); code != 200 || !strings.Contains(body, "half") || !strings.Contains(body, "Overloaded") || b.n != 0 {
		t.Fatalf("late: %d %s (b %d)", code, body, b.n)
	}

	for _, x := range []struct {
		ev   string
		kind int
	}{
		{`: keep-alive`, eventLead},
		{`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, eventLead},
		{`data: {"id":"c","choices":[{"index":0,"delta":{"content":"hi"}}]}`, eventContent},
		{`data: {"error":{"message":"upstream failed"}}`, eventError},
		{`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"r"}}`, eventLead},
		{`event: codex.rate_limits` + "\n" + `data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true}}`, eventLead},
		{`event: response.failed` + "\n" + `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`, eventError},
		{`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","delta":"hi"}`, eventContent},
		{`data: [DONE]`, eventContent},
	} {
		if k, _, _ := streamEvent([]byte(x.ev)); k != x.kind {
			t.Errorf("streamEvent(%s) = %d, want %d", x.ev, k, x.kind)
		}
	}
}

// The last one left — or the only one — is tried again after a failure
// that may pass, a moment later; one that won't is the agent's at once.
func TestLastOneLeftIsTriedAgain(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{503, "", `{"error":{"message":"upstream connect error"}}`}, {502, "", `{"error":{"message":"bad gateway"}}`}, {200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 3 {
		t.Fatalf("%d %s (tried %d times)", code, body, a.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || r.Tries[0].Again == 0 || r.Tries[1].Again == 0 || r.Tries[2].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}

	fresh(t)
	a = &scripted{replies: []reply{{503, "", `{"error":{"message":"down"}}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	if code, _ := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 503 || a.n != 1+lastRetries {
		t.Fatalf("gave up with %d after %d tries", code, a.n)
	}

	fresh(t)
	a = &scripted{replies: []reply{{400, "", `{"error":{"message":"messages: field required"}}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	if code, _ := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 400 || a.n != 1 {
		t.Fatalf("a request at fault: %d after %d tries", code, a.n)
	}
}

// An agent that goes away mid-request isn't a provider failing: nobody
// rests for it and nobody else is asked.
func TestCanceledRequestRestsNobody(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "", chatOK}}, hang: make(chan struct{})}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered})
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel(); time.Sleep(50 * time.Millisecond); close(a.hang) }()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx))
	r := s.trace.routes[len(s.trace.routes)-1]
	if b.n != 0 || len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil || s.resting("a") {
		t.Fatalf("b tried %d, tries %+v", b.n, r.Tries)
	}
}
