package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// thinkingOn serves Claude thinking at length: its first events, then
// Anthropic's pings until heard is closed (or 3s), then the rest of reply.
func thinkingOn(t *testing.T, id, reply string, heard <-chan struct{}) {
	t.Helper()
	events := strings.SplitAfter(reply, "\n\n")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		io.WriteString(w, events[0]+events[1]+events[2])
		f.Flush()
		for wait := time.After(3 * time.Second); ; {
			select {
			case <-heard:
			case <-wait:
			case <-time.After(10 * time.Millisecond):
				io.WriteString(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
				f.Flush()
				continue
			}
			break
		}
		for _, ev := range events[3:] {
			io.WriteString(w, ev)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"claude-opus-5-5"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
}

// ask posts claudeCodeAsk to the gateway over HTTP and reads the reply as
// it comes, closing heard once a keepalive arrived.
func ask(t *testing.T, s *Server, heard chan struct{}) (int, string) {
	t.Helper()
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	resp, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(claudeCodeAsk))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var once sync.Once
	var body strings.Builder
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		body.WriteString(line)
		if strings.HasPrefix(line, ": keepalive") {
			once.Do(func() { close(heard) })
		}
		if err != nil {
			break
		}
	}
	return resp.StatusCode, body.String()
}

// Claude thinking for longer than the agent's idle timeout through a
// group, where its stream is held for a refusal to go to another account
// (#248): the agent heard nothing for up to four minutes, and Alma's 240s
// timeout for a reasoning model ended the turn first (#751). It is kept
// alive with comments meanwhile, which say nothing of the reasoning, and a
// refusal after them still goes to the next member, whose answer follows
// in the same stream.
func TestHeldThinkingKeepsTheAgentAlive(t *testing.T) {
	fresh(t)
	keepFast(t, keepaliveGap, 20*time.Millisecond, keepaliveLongest)
	k := keepHeldAfter
	keepHeldAfter = 30 * time.Millisecond
	t.Cleanup(func() { keepHeldAfter = k })

	heard := make(chan struct{})
	thinkingOn(t, "a", anthropicThoughtThenRefused, heard)
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/claude-opus-5-5", "b/m")
	s := New()
	code, body := ask(t, s, heard)
	alive := strings.Index(body, ": keepalive")
	if code != 200 || alive < 0 || !strings.Contains(body[alive:], "from b") || strings.Contains(body, "user wants") || strings.Contains(body, `"refusal"`) || b.n != 1 {
		t.Fatalf("%d %q (b %d)", code, body, b.n)
	}
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("one reply in the stream: %q", body)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}

	// thinking that goes on to its answer comes through whole after them
	fresh(t)
	heard = make(chan struct{})
	thinkingOn(t, "a", anthropicThoughtThenAnswered, heard)
	scriptedOn(t, "b", provider.Anthropic, &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}})
	refusalGroup(t, "a/claude-opus-5-5", "b/m")
	code, body = ask(t, New(), heard)
	alive = strings.Index(body, ": keepalive")
	if code != 200 || alive < 0 || !strings.Contains(body[alive:], `"thinking":"hm"`) || !strings.Contains(body, "from a") || strings.Contains(body, "from b") {
		t.Fatalf("answered: %d %q", code, body)
	}

	// refused with nobody left: the stream's error, as its 200 went out
	fresh(t)
	heard = make(chan struct{})
	thinkingOn(t, "a", anthropicThoughtThenRefused, heard)
	refusalGroup(t, "a/claude-opus-5-5")
	code, body = ask(t, New(), heard)
	if code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "event: error") || strings.Contains(body, "user wants") {
		t.Fatalf("refused, nobody left: %d %q", code, body)
	}
}

// A stream's last event written after the agent went away reached nobody:
// the reply isn't whole, and the request is recorded as canceled (499), not
// as a 200 done (#751, a Claude Code run stopped by Alma's timeout).
func TestClientGoneRecordedAsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHoldWriter(httptest.NewRecorder(), false)
	h.ctx = ctx
	h.Header().Set("Content-Type", "text/event-stream")
	h.Write([]byte(sse(`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m"}}`)))
	cancel()
	h.Write([]byte(sse(`event: message_stop` + "\n" + `data: {"type":"message_stop"}`)))
	if h.ended {
		t.Fatal("a stream ended after its agent left taken as whole")
	}

	// Claude Code's run stopped mid-reply, written to the holdWriter the
	// gateway gives it: relay still ends the stream it was encoding
	// (message_stop), which isn't the reply made whole
	dir := fakeClaudeCalling(t)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			b, _ := os.ReadFile(filepath.Join(dir, "hang.pid"))
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid != 0 {
				time.Sleep(200 * time.Millisecond) // its reply under way
				break
			}
		}
		cancel()
	}()
	rec := httptest.NewRecorder()
	h = newHoldWriter(rec, false)
	h.ctx = ctx
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hang on"}]}`
	var u Usage
	s.serveClaudeSubscription(h, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
	if !strings.Contains(rec.Body.String(), "thinking it over") || !strings.Contains(rec.Body.String(), "message_stop") {
		t.Fatalf("the run's reply: %q", rec.Body.String())
	}
	if h.ended {
		t.Fatal("a Claude Code run stopped as its agent left taken as a whole reply, recorded as a 200")
	}
}
