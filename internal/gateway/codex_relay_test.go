package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// relayUp is a fake ChatGPT backend for the relay: what it was sent.
type relayUp struct {
	mu   sync.Mutex
	got  []relayGot
	srv  *httptest.Server
	code int
}

type relayGot struct {
	path, auth, body, encoding string
}

func newRelayUp(t *testing.T) *relayUp {
	t.Helper()
	up := &relayUp{code: 200}
	up.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.got = append(up.got, relayGot{r.URL.Path, r.Header.Get("Authorization"), string(b), r.Header.Get("Content-Encoding")})
		up.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(up.code)
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	t.Cleanup(up.srv.Close)
	was := provider.CodexBase
	provider.CodexBase = up.srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return up
}

func (up *relayUp) sent() []relayGot {
	up.mu.Lock()
	defer up.mu.Unlock()
	return append([]relayGot(nil), up.got...)
}

func relayDo(h http.Handler, method, host, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A Codex thread open when magpie quit keeps sending its turns to the
// gateway's address (idkatbro on X: 503 "Unknown error" until magpie was
// opened again). The relay passes one on Codex's own model to the ChatGPT
// backend with the caller's own sign-in, and its reply comes back whole.
func TestCodexRelayPassesOwnTurnOn(t *testing.T) {
	fresh(t)
	up := newRelayUp(t)
	h := CodexRelay()
	rec := relayDo(h, "POST", "127.0.0.1:3425", CodexPath+"/responses", `{"model":"gpt-5.5","input":"hi","stream":true}`,
		map[string]string{"Authorization": "Bearer chatgpt-token", "Content-Type": "application/json"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := up.sent()
	if len(got) != 1 || got[0].path != "/backend-api/codex/responses" || got[0].auth != "Bearer chatgpt-token" {
		t.Fatalf("upstream was sent %+v", got)
	}
	// the model list Codex asks for, and localhost by name, go on too
	rec = relayDo(h, "GET", "localhost:3425", CodexPath+"/models?client_version=0.162.1", "", map[string]string{"Authorization": "Bearer chatgpt-token"})
	if rec.Code != 200 || len(up.sent()) != 2 || up.sent()[1].path != "/backend-api/codex/models" {
		t.Fatalf("%d %+v", rec.Code, up.sent())
	}
}

// What only the running magpie serves is refused with a reason Codex
// shows at once (a 4xx; a 5xx it retries five times over), and nothing
// goes to OpenAI: a magpie model, a turn on one of
// magpie's accounts, and one masking would have masked, never sent
// unmasked.
func TestCodexRelayRefusesWhatNeedsMagpie(t *testing.T) {
	fresh(t)
	up := newRelayUp(t)
	h := CodexRelay()
	auth := map[string]string{"Authorization": "Bearer chatgpt-token"}
	for _, c := range []struct {
		name, body string
		hdr        map[string]string
		mask       bool
		want       string
	}{
		{"magpie model", `{"model":"openrouter/kimi-k2","input":"hi"}`, auth, false, "one of magpie's models"},
		{"magpie account", `{"model":"gpt-5.5","input":"hi"}`, map[string]string{"Authorization": "Bearer t", AccountHeader: "a@b.c"}, false, "magpie's Codex accounts"},
		{"masking", `{"model":"gpt-5.5","input":"DB_PASSWORD=hunter2"}`, auth, true, "not sent unmasked"},
	} {
		if err := settings.Save(settings.Settings{Redact: c.mask}); err != nil {
			t.Fatal(err)
		}
		rec := relayDo(h, "POST", "127.0.0.1:3425", CodexPath+"/responses", c.body, c.hdr)
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusBadRequest || !strings.Contains(e.Error.Message, c.want) || !strings.Contains(e.Error.Message, "magpie isn't running") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if got := up.sent(); len(got) != 0 {
		t.Fatalf("OpenAI was sent %+v", got)
	}
}

// Only Codex on this computer is served: a page (any Origin, null too)
// and a name rebound to 127.0.0.1 are turned away before anything goes
// on, as corsGuard turns them from the gateway.
func TestCodexRelayOnlyThisComputer(t *testing.T) {
	fresh(t)
	up := newRelayUp(t)
	h := CodexRelay()
	body := `{"model":"gpt-5.5","input":"hi"}`
	for _, c := range []struct{ host, origin string }{
		{"127.0.0.1:3425", "null"},
		{"127.0.0.1:3425", "https://evil.example"},
		{"127.0.0.1:3425", "chrome-extension://abcdefghijklmnopabcdefghijklmnop"},
		{"evil.example:3425", ""},
		{"magpie.localhost:3425", ""},
	} {
		hdr := map[string]string{"Authorization": "Bearer chatgpt-token"}
		if c.origin != "" {
			hdr["Origin"] = c.origin
		}
		if rec := relayDo(h, "POST", c.host, CodexPath+"/responses", body, hdr); rec.Code != 403 || !strings.Contains(rec.Body.String(), "Open magpie") {
			t.Errorf("%+v: %d %s", c, rec.Code, rec.Body.String())
		}
	}
	if got := up.sent(); len(got) != 0 {
		t.Fatalf("OpenAI was sent %+v", got)
	}
	if rec := relayDo(h, "POST", "[::1]:3425", CodexPath+"/responses", body, map[string]string{"Authorization": "Bearer t"}); rec.Code != 200 {
		t.Fatalf("::1: %d %s", rec.Code, rec.Body.String())
	}
}

// Anything but Codex's turns says magpie isn't running, readably; and
// the relay's answer at "/" is never taken for magpie's (ServedBy), so
// the next magpie starting still serves the gateway. An OpenAI that
// can't be reached is said too, never an empty body Codex shows as
// "Unknown error".
func TestCodexRelayIsNotMagpie(t *testing.T) {
	fresh(t)
	newRelayUp(t)
	h := CodexRelay()
	for _, p := range []string{"/", "/v1/models", "/v1/chat/completions"} {
		rec := relayDo(h, "GET", "127.0.0.1:3425", p, "", nil)
		b := rec.Body.Bytes()
		var info struct {
			Name string `json:"name"`
		}
		json.Unmarshal(b, &info)
		if rec.Code != 503 || info.Name == "magpie" || bytes.Contains(b, []byte(`"magpie"`)) || !strings.Contains(string(b), "magpie isn't running") {
			t.Errorf("%s: %d %s", p, rec.Code, b)
		}
	}
	provider.CodexBase = "http://127.0.0.1:1/backend-api/codex"
	rec := relayDo(h, "POST", "127.0.0.1:3425", CodexPath+"/responses", `{"model":"gpt-5.5","input":"hi"}`, map[string]string{"Authorization": "Bearer t"})
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "OpenAI can't be reached") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// a WebSocket is sent to plain HTTP, as the gateway does
	if rec := relayDo(h, "GET", "127.0.0.1:3425", CodexPath+"/responses", "", map[string]string{"Upgrade": "websocket"}); rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("websocket: %d", rec.Code)
	}
}

// A browser extension calling the gateway's address while magpie is quit
// and the relay holds the port (jipairamen on X, a Chrome translation
// extension) is told magpie isn't running and to open it, as any caller
// is, not that the relay serves Codex alone; and nothing goes on to
// OpenAI, a key or not.
func TestCodexRelayTellsExtensionToOpenMagpie(t *testing.T) {
	fresh(t)
	up := newRelayUp(t)
	h := CodexRelay()
	for _, origin := range []string{"chrome-extension://abcdefghijklmnopabcdefghijklmnop", "moz-extension://1234", "https://evil.example", "null"} {
		for _, p := range []string{"/v1/chat/completions", "/chat/completions", "/v1/responses", "/v1/models"} {
			for _, auth := range []string{"", "Bearer sk-magpie-key"} {
				hdr := map[string]string{"Origin": origin, "Content-Type": "application/json"}
				if auth != "" {
					hdr["Authorization"] = auth
				}
				rec := relayDo(h, "POST", "127.0.0.1:3425", p, `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, hdr)
				b := rec.Body.String()
				if rec.Code != 503 || !strings.Contains(b, "magpie isn't running") || !strings.Contains(b, "Open magpie") || strings.Contains(b, "serves Codex on this computer only") {
					t.Errorf("%s %s %q: %d %s", origin, p, auth, rec.Code, b)
				}
			}
		}
	}
	if got := up.sent(); len(got) != 0 {
		t.Fatalf("OpenAI was sent %+v", got)
	}
}
