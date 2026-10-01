package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request one magpie passes on to another computer's (a Remote magpie
// provider) is recorded there as the agent's that made it, by the computer
// it came through, not as "magpie"'s (Jorben on Discord). The names go in
// headers of magpie's own, which a vendor never sees, and which a request
// that isn't magpie's can't use to pass itself off as another agent.
func TestRemoteMagpieAgent(t *testing.T) {
	var mu sync.Mutex
	var leaked []string // magpie's own headers as the vendor saw them
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
				leaked = append(leaked, k)
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`data: {"type":"message_stop"}`))
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// the record the remote magpie (the one that asked the vendor) kept of
	// the request this one passed on
	there := func(n int) usage.Record {
		t.Helper()
		for range 100 {
			var rs []usage.Record
			for _, r := range usage.Load(time.Time{}) {
				if r.Provider == "fake" {
					rs = append(rs, r)
				}
			}
			if len(rs) >= n {
				return rs[n-1]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no record %d of the remote's", n)
		return usage.Record{}
	}
	send := func(path, body string, h map[string]string) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// an agent known by its token, on Chat; one known by its User-Agent,
	// on Messages; each a request this magpie passes on
	send("/v1/chat/completions", `{"model":"office/fake/m1","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + TokenFor("alma")})
	if r := there(1); r.Agent != "alma" || r.Via == "" {
		t.Errorf("remote recorded the token's agent as %q via %q; want alma, via this computer", r.Agent, r.Via)
	}
	send("/v1/messages", `{"model":"office/fake/m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"User-Agent": "opencode/1.2.3"})
	if r := there(2); r.Agent != "opencode" || r.Via == "" {
		t.Errorf("remote recorded the User-Agent's agent as %q via %q; want opencode", r.Agent, r.Via)
	}
	// this computer's own record of it is the agent's, not passed on
	for _, r := range usage.Load(time.Time{}) {
		if r.Provider == "office" && r.Via != "" {
			t.Errorf("the request was made here, not passed on: %+v", r)
		}
	}

	// a request that isn't a magpie's names no one but itself
	send("/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"User-Agent": "curl/8.0", AgentHeader: "codex", ViaHeader: "elsewhere"})
	if r := there(3); r.Agent != "curl" || r.Via != "" {
		t.Errorf("a client's own %s was taken: %q via %q", AgentHeader, r.Agent, r.Via)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Errorf("the vendor was sent magpie's headers: %v", leaked)
	}
}

// A record kept for an agent on another computer, passed on by its magpie,
// says nothing of whether this computer's agent of the name reached magpie.
func TestLastSeenNotVia(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usage.Append(usage.Record{Agent: "remote-only-agent", Provider: "fake", Via: "office", Status: 200})
	if !usage.LastSeen("remote-only-agent").IsZero() {
		t.Error("an agent on another computer counted as this one's")
	}
}
