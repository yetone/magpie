package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dshTransport is how dsh's pi-ai adapter tells a failure it retries as a
// dropped connection (classifyPiAiError's TRANSPORT); anything it can't
// place is PI_AI_ERROR, which it never retries.
var dshTransport = regexp.MustCompile(`(?i)\b(?:network|connection|socket|fetch)\b|\bECONN[A-Z]+\b|\bterminated\b|premature close|stream ended (?:before|without)\b`)

// A Chat upstream whose connection drops mid-reply under several sessions
// at once, as OpenCode Go's did for dsh: every session on the connection
// that dropped got "OpenCode Go: unexpected EOF", which dsh took for an
// error it can't retry, and the session's turn failed (#470). Each is told
// it lost the connection, which it retries.
func TestStreamCutSaysConnection(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		body := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}` + "\n\n"
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(body)+100)
		io.WriteString(buf, body)
		buf.Flush()
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	severed(t, anthropicStart, anthropicText) // up/m, spoken to in Messages: the reply translated

	cases := []struct{ model, name string }{{"go/m", "OpenCode Go"}, {"up/m", "UP"}}
	var wg sync.WaitGroup
	got := make([]string, 6)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, got[i] = post(t, "/v1/chat/completions", `{"model":"`+cases[i%2].model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		}(i)
	}
	wg.Wait()
	for i, body := range got {
		c := cases[i%2]
		var msg string
		for _, ev := range events(body) {
			if e, ok := ev["error"].(map[string]any); ok {
				msg, _ = e["message"].(string)
			}
		}
		if !strings.HasPrefix(msg, c.name+": ") || !strings.Contains(msg, "unexpected EOF") {
			t.Fatalf("%s: a stream cut mid-reply ended without an error naming the provider and the cause: %q in %s", c.model, msg, body)
		}
		if !dshTransport.MatchString(msg) {
			t.Fatalf("%s: a dropped connection is told as %q, which dsh doesn't retry", c.model, msg)
		}
	}
}

// One cut before anything was said is magpie's to try again: the session
// gets the reply of the try after, never the dropped connection.
func TestStreamCutBeforeContentTriedAgain(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	tries := 0
	whole := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		tries++
		first := tries == 1
		mu.Unlock()
		if !first {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, whole)
			return
		}
		conn, buf, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		lead := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n"
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(lead)+100)
		io.WriteString(buf, lead)
		buf.Flush()
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	_, body := post(t, "/v1/chat/completions", `{"model":"go/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	mu.Lock()
	defer mu.Unlock()
	if tries != 2 || !strings.Contains(body, "hello") || strings.Contains(body, "EOF") {
		t.Fatalf("a stream cut before its content wasn't tried again (%d tries): %s", tries, body)
	}
}
