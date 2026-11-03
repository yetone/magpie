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
)

// heldThinker is an Anthropic vendor that says its first thought and then
// says nothing more until the agent has heard it (heard closed), or until
// giveUp passes. A gateway that holds the thinking until the reply ends
// lets the agent hear it only after giveUp, all at once.
type heldThinker struct {
	ct     []string // the reply's Content-Type: none when empty
	heard  chan struct{}
	giveUp time.Duration
	mu     sync.Mutex
	waited bool // the vendor gave up waiting: nothing reached the agent
}

func (v *heldThinker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	w.Header()["Content-Type"] = v.ct
	f := w.(http.Flusher)
	say := func(evs ...string) {
		for _, ev := range evs {
			io.WriteString(w, ev+"\n\n")
		}
		f.Flush()
	}
	say(glmThinksSlowly[0], glmThinksSlowly[1], glmThinksSlowly[2])
	select {
	case <-v.heard:
	case <-time.After(v.giveUp):
		v.mu.Lock()
		v.waited = true
		v.mu.Unlock()
	case <-r.Context().Done():
		return
	}
	say(glmThinksSlowly[3:]...)
}

// uk.jent on Discord: MiniMax Code's plugin, served as Anthropic Messages
// to Grok Build, showed nothing for minutes and then the whole reply at
// once, each thinking block "Thought for 0.0s". A plugin's thinking reaches
// the agent while the vendor is still thinking, on every caller's shape.
func TestPluginThinkingStreamsAsItComes(t *testing.T) {
	t.Setenv("FAKE_MINIMAX", "1")
	tools := `,"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]`
	for _, ct := range []string{"text/event-stream", "application/json", "text/plain; charset=utf-8", "", "application/octet-stream"} {
		for _, ask := range []struct{ name, path, body, thought string }{
			{"messages", "/v1/messages", `{"model":"MODEL","max_tokens":32000,"stream":true,"thinking":{"type":"enabled","budget_tokens":10000},"messages":[{"role":"user","content":"hi"}]}`, `"thinking_delta"`},
			{"messages-tools", "/v1/messages", `{"model":"MODEL","max_tokens":32000,"stream":true,"thinking":{"type":"enabled","budget_tokens":10000},"messages":[{"role":"user","content":"hi"}]` + tools + `}`, `"thinking_delta"`},
			{"chat", "/v1/chat/completions", `{"model":"MODEL","stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, `first thought`},
			{"responses", "/v1/responses", `{"model":"MODEL","stream":true,"reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`, `first thought`},
		} {
			t.Run(ask.name+"/"+ct, func(t *testing.T) {
				up := &heldThinker{heard: make(chan struct{}), giveUp: 10 * time.Second}
				if ct != "" {
					up.ct = []string{ct}
				}
				pid := besideFake(t, "minimax-code", up)
				gw := httptest.NewServer(New().Handler())
				t.Cleanup(gw.Close)
				req, _ := http.NewRequest("POST", gw.URL+ask.path, strings.NewReader(strings.Replace(ask.body, "MODEL", pid+"/MiniMax-M3.1-Flash", 1)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("anthropic-version", "2023-06-01")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				if res.StatusCode != 200 {
					b, _ := io.ReadAll(res.Body)
					t.Fatalf("%d %s", res.StatusCode, b)
				}
				var all strings.Builder
				rd := bufio.NewReader(res.Body)
				heard := false
				for {
					ln, err := rd.ReadString('\n')
					all.WriteString(ln)
					if !heard && strings.Contains(ln, ask.thought) {
						heard = true
						close(up.heard)
					}
					if err != nil {
						break
					}
				}
				up.mu.Lock()
				waited := up.waited
				up.mu.Unlock()
				if !heard {
					t.Fatalf("the thought never reached the agent:\n%s", all.String())
				}
				if waited {
					t.Fatalf("the first thought reached the agent only once the vendor had finished:\n%s", all.String())
				}
			})
		}
	}
}
