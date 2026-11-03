package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// packinggoods on Discord: a request still answering on a remote magpie's
// claude-opus-5-5 had Routing's context window card say the model's window
// isn't known, though its list gives it: the window was set on the route
// only once the request was done. A request in flight is in the window of
// the model its try went to, from the moment its prompt is read, on the
// gateway's routes and on a Codex turn through magpie's Codex backend
// alike; done, it keeps it.
func TestLivePromptHasItsWindow(t *testing.T) {
	// held: an upstream that starts its reply and holds it until let go,
	// so the request is in flight while the trace is read
	held := func(t *testing.T, start string) (http.HandlerFunc, func()) {
		release := make(chan struct{})
		var once sync.Once
		let := func() { once.Do(func() { close(release) }) }
		return func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, start)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}, let
	}
	// inFlight sends req and returns the route's prompt once it has been
	// read while the request is still answering, then the route as done
	inFlight := func(t *testing.T, s *Server, req *http.Request, let func()) (live, done *Prompt) {
		t.Helper()
		rec := httptest.NewRecorder()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			s.Handler().ServeHTTP(rec, req)
		}()
		t.Cleanup(func() { let(); <-finished })
		route := func() (Route, bool) {
			for _, r := range s.Trace(t.Context(), 0, 0).Routes {
				return r, true
			}
			return Route{}, false
		}
		// wait on the state: the prompt read, and the try gone out
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if r, ok := route(); ok && r.Prompt != nil && len(r.Tries) > 0 {
				if r.Done {
					t.Fatalf("done before it was let go: %+v", r)
				}
				if r.Prompt.Window > 0 || time.Now().After(deadline) {
					live = r.Prompt
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("the request never showed in flight with its prompt")
			}
		}
		let()
		<-finished
		r, _ := route()
		return live, r.Prompt
	}

	t.Run("remote magpie", func(t *testing.T) {
		fresh(t)
		h, let := held(t, `data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"o"}}]}`+"\n\n")
		up := httptest.NewServer(h)
		t.Cleanup(up.Close)
		t.Cleanup(let) // before the server's Close, which waits on it
		if err := provider.Save(provider.Provider{ID: "remote-magpie", Name: "Remote", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-x", Chat: up.URL}); err != nil {
			t.Fatal(err)
		}
		// the other magpie's /v1/models, which says the window
		if err := catalog.SaveLive("remote-magpie", up.URL+"/v1", []catalog.Model{{ID: "claude/claude-opus-5-5", Context: 1000000}}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"remote-magpie/claude/claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"refactor the parser"}]}`))
		live, done := inFlight(t, New(), req, let)
		if live == nil || live.Window != 1000000 {
			t.Errorf("in flight: prompt %+v, want the window 1000000", live)
		}
		if done == nil || done.Window != 1000000 {
			t.Errorf("done: prompt %+v, want the window 1000000", done)
		}
	})

	t.Run("codex backend", func(t *testing.T) {
		h, let := held(t, `data: {"type":"response.created","response":{"id":"r1"}}`+"\n\n")
		chatgpt(t, h)
		t.Cleanup(let)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		catalog.Reset()
		t.Cleanup(catalog.Reset)
		codexChatGPTAuth(t, "plus@example.com")
		if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "models_cache.json"), []byte(`{"etag":"\"e1\"","client_version":"0.159.0","models":[
		  {"slug":"gpt-5.5","display_name":"GPT-5.5","priority":1,"visibility":"list","context_window":272000,"max_context_window":272000}]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"refactor the parser"}]}]}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		live, done := inFlight(t, New(), req, let)
		if live == nil || live.Window != 272000 {
			t.Errorf("in flight: prompt %+v, want Codex's window 272000", live)
		}
		if done == nil || done.Window != 272000 {
			t.Errorf("done: prompt %+v, want Codex's window 272000", done)
		}
	})
}
