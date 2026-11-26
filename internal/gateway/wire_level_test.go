package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// loggedAt fails t unless s's last try, and the last usage record, say
// went: the effort the upstream was sent, not the one asked.
func loggedAt(t *testing.T, s *Server, what, went string) {
	t.Helper()
	r := lastRoute(s)
	if len(r.Tries) == 0 || r.Tries[len(r.Tries)-1].Effort != went {
		t.Errorf("%s: route asked %q, tries %+v; want went %q", what, r.Effort, r.Tries, went)
	}
	recs := usage.Load(time.Time{})
	if len(recs) == 0 || recs[len(recs)-1].Effort != went {
		var got string
		if len(recs) > 0 {
			got = recs[len(recs)-1].Effort
		}
		t.Errorf("%s: usage logged %q, want %q", what, got, went)
	}
}

// #7400: Gemini's builders say a level of their own — a Gemini 3 Pro asked
// medium thinks at high, an effort above a model's levels comes down to
// its highest — and the Routing page and the usage log said the effort
// asked while another went. They say the level in the body Gemini got; a
// Gemini 2.5, sent a budget, keeps the effort asked.
func TestGeminiLevelLoggedAsSent(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"google-vertex":{"models":{
	"gemini-4-neon":{"id":"gemini-4-neon","reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh"]}]},
	"gemini-4-argon":{"id":"gemini-4-argon","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-2.5-flash":{"id":"gemini-2.5-flash","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	var mu sync.Mutex
	var level string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		level = gjson.GetBytes(b, "generationConfig.thinkingConfig.thinkingLevel").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, vertexPong)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "gv", Name: "Google", Key: "k", Catalog: "google-vertex",
		Gemini: up.URL + "/v1beta", Models: []string{"gemini-4-neon", "gemini-4-argon", "gemini-2.5-flash", "gemini-3-pro-preview"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, c := range []struct {
		path, model, effort string
		sent, went          string // sent: the thinkingLevel Gemini got, "" for a budget
	}{
		{"/v1/chat/completions", "gemini-3-pro-preview", "medium", "high", "high"},
		{"/v1/responses", "gemini-3-pro-preview", "medium", "high", "high"},
		{"/v1/chat/completions", "gemini-4-argon", "xhigh", "high", "high"},
		{"/v1/chat/completions", "gemini-4-neon", "xhigh", "xhigh", "xhigh"},
		{"/v1/chat/completions", "gemini-4-argon", "low", "low", "low"},
		{"/v1/chat/completions", "gemini-2.5-flash", "high", "", "high"},
	} {
		body := `{"model":"gv/` + c.model + `","stream":true,"reasoning_effort":"` + c.effort + `","messages":[{"role":"user","content":"hi"}]}`
		if c.path == "/v1/responses" {
			body = `{"model":"gv/` + c.model + `","stream":true,"reasoning":{"effort":"` + c.effort + `"},"input":"hi"}`
		}
		what := c.path + " " + c.model + " at " + c.effort
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d: %s", what, rec.Code, rec.Body.String())
			continue
		}
		mu.Lock()
		sent := level
		mu.Unlock()
		if sent != c.sent {
			t.Errorf("%s: sent level %q, want %q", what, sent, c.sent)
		}
		loggedAt(t, s, what, c.went)
	}
}

// #7400 on a relay serving Gemini under a host of its own: the level the
// relay was sent (geminiLevelBody's, an effort above the model's highest
// at its highest) is the one logged, relayed on the client's own API or
// translated; a Claude model on the same relay keeps the effort asked.
func TestGeminiRelayLevelLoggedAsSent(t *testing.T) {
	up, bodies := levelRelay(t)
	addr := strings.TrimPrefix(up.URL, "http://")
	for _, c := range []struct {
		name, upstream, model, path, body, went string
	}{
		{"messages effort max relayed to Anthropic", "anth", "gemini-4-argon", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "high"},
		{"chat xhigh relayed to Chat", "chat", "gemini-4-argon", "/v1/chat/completions",
			`{"reasoning_effort":"xhigh"}`, "high"},
		{"chat xhigh translated to Anthropic", "anth", "gemini-4-argon", "/v1/chat/completions",
			`{"reasoning_effort":"xhigh"}`, "high"},
		{"chat medium translated to Anthropic", "anth", "gemini-4-argon", "/v1/chat/completions",
			`{"reasoning_effort":"medium"}`, "medium"},
		{"Claude max relayed to Anthropic", "anth", "claude-sonnet-4-5", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "max"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			p := provider.Provider{ID: "peta", Name: "peta", Key: "k", Models: []string{c.model}}
			if c.upstream == "anth" {
				p.Anthropic = "http://api.peta.example.com"
			} else {
				p.Chat = "http://api.peta.example.com/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}}
			body := `{"model":"peta/` + c.model + `","messages":[{"role":"user","content":"hi"}],` + strings.TrimPrefix(c.body, "{")
			*bodies = nil
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
			if rec.Code != 200 || len(*bodies) == 0 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			sent, _ := json.Marshal((*bodies)[len(*bodies)-1])
			if got := gjson.GetBytes(sent, "output_config.effort").String() + gjson.GetBytes(sent, "reasoning_effort").String(); got != c.went {
				t.Errorf("sent %s, want the level %q", sent, c.went)
			}
			loggedAt(t, s, c.name, c.went)
		})
	}
}

// #7400 on Gemini's own OpenAI-compatible API: a client asking xhigh to
// see its thoughts is sent thinking_config's level high, the highest that
// API is sent, and the log says high, not the xhigh asked.
func TestGeminiCompatLevelLoggedAsSent(t *testing.T) {
	f := &fake{t: t, reply: chatHi}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	fresh(t)
	// a loopback host serving a gemini model is Gemini's compatible API
	// (geminiCompat), as a local proxy in front of it is
	if err := provider.Save(provider.Provider{ID: "gc", Name: "Gemini", Key: "k", Chat: up.URL + "/v1", Models: []string{"gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"model":"gc/gemini-3.8-flash","stream":true,"input":"hi","reasoning":{"effort":"xhigh","summary":"auto"}}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := gjson.GetBytes(f.got, "extra_body.google.thinking_config.thinking_level").String(); got != "high" {
		t.Fatalf("sent %s, want thinking_level high", f.got)
	}
	loggedAt(t, s, "xhigh to Gemini's compatible API", "high")
}

// geminiWireLevel reads the level where each API's body puts it, and no
// level from a body that names a budget, thinking off, or a word that is
// not one.
func TestGeminiWireLevel(t *testing.T) {
	// a Gemini 3 Pro asked medium goes to Code Assist at high
	r, err := parse(provider.Chat, []byte(`{"model":"gemini-3-pro-preview","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ca := buildCodeAssist(r, "gemini-3-pro-preview", "gemini")
	for _, c := range []struct {
		to   provider.Protocol
		body string
		want string // "" for none
	}{
		{provider.Gemini, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`, "high"},
		{provider.Gemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":2000}}}`, ""},
		{provider.CodeAssist, string(ca), "high"},
		{provider.Chat, `{"reasoning_effort":"xhigh","extra_body":{"google":{"thinking_config":{"thinking_level":"high"}}}}`, "high"},
		{provider.Chat, `{"extra_body":{"google":{"thinkingConfig":{"thinkingLevel":"low"}}}}`, "low"},
		{provider.Chat, `{"extra_body":{"google":{"thinking_config":{"thinking_budget":2000}}}}`, ""},
		{provider.Chat, `{"reasoning_effort":"medium"}`, "medium"},
		{provider.Anthropic, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`, "high"},
		{provider.Anthropic, `{"thinking":{"type":"disabled"},"output_config":{"effort":"low"}}`, ""},
		{provider.Anthropic, `{"output_config":{"effort":"ludicrous"}}`, ""},
		{provider.Responses, `{"reasoning":{"effort":"high"}}`, ""},
	} {
		got, ok := geminiWireLevel(c.to, []byte(c.body))
		if (c.want == "") == ok || got != c.want && ok {
			t.Errorf("%s %s: %q %v, want %q", c.to, c.body, got, ok, c.want)
		}
	}
}
