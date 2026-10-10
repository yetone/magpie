package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// pacedMessages is an Anthropic Messages upstream that says its reply's
// first event at once, then a word every pace, for the client to be seen
// reading as they come rather than at the reply's end. It notes when its
// first bytes went out and each path it was asked on.
type pacedMessages struct {
	pace  time.Duration
	mu    sync.Mutex
	paths []string
	first time.Time // when the first event was written, zero until then
	n     int       // requests answered
}

func (p *pacedMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.paths = append(p.paths, r.URL.Path)
	p.n++
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	w.(http.Flusher).Flush()
	p.mu.Lock()
	if p.first.IsZero() {
		p.first = time.Now()
	}
	p.mu.Unlock()
	for _, s := range []string{"counting ", "the ", "moments ", "as ", "they ", "come"} {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(p.pace):
		}
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\""+s+"\"}}\n\n")
		w.(http.Flusher).Flush()
	}
	io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":6}}\n\n"+
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	w.(http.Flusher).Flush()
}

// A Chat client's reply translated from Anthropic's Messages must stream
// as the upstream sends it: its first chunk reaches the client as the
// upstream's first event does, and the words that follow as they are
// written, not the whole reply at its end. A reporter's OpenAI-format
// client on Z.ai's GLM saw exactly that — every event of a reply in one
// burst once the stream ended (measured out to be the vendor's, not the
// gateway's, but the translation's pacing is worth a lock either way),
// while the same upstream's Messages replies streamed fine, so both a
// vendor's model (GLM) and a Claude model, which goes to Messages from
// Chat wherever its provider has that URL, are paced here.
func TestChatFromAnthropicStreamsAsItComes(t *testing.T) {
	for _, c := range []struct {
		name     string
		model    string
		withChat bool // a Chat URL beside the Anthropic one: the Claude model still goes to Messages
	}{
		{"glm", "glm-5.3", false},
		{"claude", "claude-sonnet-4-6", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			pace := 200 * time.Millisecond
			up := &pacedMessages{pace: pace}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			p := provider.Provider{ID: "z", Name: "Z", Key: "k", Models: []string{c.model}, Anthropic: srv.URL}
			if c.withChat {
				p.Chat = srv.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			gw := httptest.NewServer(New().Handler())
			t.Cleanup(gw.Close)

			began := time.Now()
			resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
				strings.NewReader(`{"model":"z/`+c.model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("%d %s", resp.StatusCode, b)
			}
			var events []time.Duration // when each data line reached the client
			var body strings.Builder
			rd := bufio.NewReader(resp.Body)
			for {
				line, err := rd.ReadString('\n')
				if strings.HasPrefix(line, "data:") {
					events = append(events, time.Since(began))
				}
				body.WriteString(line)
				if err != nil {
					break
				}
			}
			if len(events) == 0 {
				t.Fatalf("no events: %s", body.String())
			}
			up.mu.Lock()
			paths, _, n := append([]string(nil), up.paths...), up.first, up.n
			up.mu.Unlock()
			if n != 1 || len(paths) != 1 || paths[0] != "/v1/messages" {
				t.Fatalf("asked %v (of %d requests), want one on /v1/messages", paths, n)
			}
			total := events[len(events)-1]
			firstChunk := events[0]
			if firstChunk > 700*time.Millisecond {
				t.Fatalf("the client's first chunk came at %v, after the paced words began — the reply was held: %s", firstChunk, body.String())
			}
			if total < 4*pace {
				t.Fatalf("the stream ran %v, under its four pauses: %s", total, body.String())
			}
			gaps := 0
			for i := 1; i < len(events); i++ {
				if events[i]-events[i-1] > 120*time.Millisecond {
					gaps++
				}
			}
			if gaps < 2 {
				t.Fatalf("%d of the five 200ms pauses reached the client: %s", gaps, body.String())
			}
			if !strings.Contains(body.String(), `"counting "`) || !strings.Contains(body.String(), `"come"`) || !strings.Contains(body.String(), "[DONE]") {
				t.Fatalf("the reply didn't arrive whole: %s", body.String())
			}
		})
	}
}
