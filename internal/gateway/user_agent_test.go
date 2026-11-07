package gateway

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// uaVendor is a key+URL vendor on every API magpie forwards to one: it
// keeps the User-Agent each path was asked with, and answers each as its
// API does.
type uaVendor struct {
	mu     sync.Mutex
	got    map[string]string
	leaked []string // magpie's ClientUAHeader, which no vendor is sent
}

func (v *uaVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	v.mu.Lock()
	v.got[r.URL.Path] = r.Header.Get("User-Agent")
	if h := r.Header.Get(ClientUAHeader); h != "" {
		v.leaked = append(v.leaked, r.URL.Path+": "+h)
	}
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch p := r.URL.Path; {
	case strings.HasSuffix(p, "/count_tokens"):
		io.WriteString(w, `{"input_tokens":5}`)
	case p == "/v1/messages":
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	case p == "/v1/chat/completions":
		io.WriteString(w, `{"id":"c","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	case p == "/v1/responses":
		io.WriteString(w, `{"id":"r1","object":"response","status":"completed","model":"m1","output":[{"type":"message","id":"msg1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	case strings.HasSuffix(p, ":streamGenerateContent"):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`+"\n\n")
	case p == "/v1/embeddings":
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"model":"embed-1","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	case p == "/v1/images/generations":
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	default:
		w.WriteHeader(404)
	}
}

func (v *uaVendor) sent(path string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ua, ok := v.got[path]
	delete(v.got, path)
	return ua, ok
}

// uaRoutes are the routes a request reaches a key+URL provider by: what an
// agent posts magpie, and the path the vendor is asked at.
var uaRoutes = []struct{ name, path, body, upstream string }{
	{"anthropic", "/v1/messages", `{"model":"ua/m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
	{"count_tokens", "/v1/messages/count_tokens", `{"model":"ua/m1","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages/count_tokens"},
	{"chat", "/v1/chat/completions", `{"model":"ua/m1","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	{"responses", "/v1/responses", `{"model":"ua/m1","input":"hi"}`, "/v1/responses"},
	{"gemini", "/v1beta/models/ug/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "/v1beta/models/m1:streamGenerateContent"},
	{"embeddings", "/v1/embeddings", `{"model":"ua/embed-1","input":"hi"}`, "/v1/embeddings"},
	{"images", "/v1/images/generations", `{"model":"ua/gpt-image-1","prompt":"a magpie"}`, "/v1/images/generations"},
}

// uaSetup saves a key+URL provider "ua" on every API, at a uaVendor, with
// pass for whether it sends the agent's User-Agent (MagpieUserAgent off,
// the default) and headers for the user's own, and "ug" the
// same on Gemini's API alone.
func uaSetup(t *testing.T, pass bool, headers map[string]string) *uaVendor {
	t.Helper()
	fresh(t)
	v := &uaVendor{got: map[string]string{}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "ua", Name: "UA", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL, Gemini: up.URL + "/v1beta",
		Models: []string{"m1", "embed-1", "gpt-image-1"}, MagpieUserAgent: !pass, Headers: headers}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ua", up.URL+"/v1", []catalog.Model{{ID: "m1"}, {ID: "embed-1"}, {ID: "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	// Gemini's own wire, which a provider on Chat too isn't asked on
	g := provider.Provider{ID: "ug", Name: "UG", Key: "k", Gemini: up.URL + "/v1beta", Models: []string{"m1"}, MagpieUserAgent: !pass, Headers: headers}
	if err := provider.Save(g); err != nil {
		t.Fatal(err)
	}
	return v
}

// askAs posts body to path as an agent whose User-Agent is ua ("" sends
// none).
func askAs(t *testing.T, s *Server, path, body, ua string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("User-Agent")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body)
	}
}

// A key+URL provider is asked with the User-Agent the agent sent (#1851,
// nb5p on Discord), on every route that reaches it: a coding plan that
// serves only agents it names knows them by it, and a gateway of the
// user's own behind magpie sees which agent asked. Set to go as magpie, it
// is asked as magpie; so it is when the agent sent none, and
// on magpie's own requests (its classifier's, the image tool's), which no
// agent sent. A User-Agent the user set in the provider's headers stands
// over both.
func TestUserAgentPassedOn(t *testing.T) {
	const pi = "pi-coding-agent/0.9.1 (darwin; arm64)"
	magpie := "magpie/" + Version
	for _, tt := range []struct {
		name    string
		pass    bool
		headers map[string]string
		ua      string // what the agent sent
		want    string
	}{
		{"as magpie", false, nil, pi, magpie},
		{"default", true, nil, pi, pi},
		{"default, the agent sent none", true, nil, "", magpie},
		{"default, magpie's classifier", true, nil, RouterAgent, magpie},
		{"default, magpie's image tool", true, nil, "claude-code/2.1.0 " + DrawAgent, magpie},
		{"default, the user's own header", true, map[string]string{"User-Agent": "mine/1"}, pi, "mine/1"},
		{"as magpie, the user's own header", false, map[string]string{"User-Agent": "mine/1"}, pi, "mine/1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := uaSetup(t, tt.pass, tt.headers)
			s := New()
			for _, r := range uaRoutes {
				askAs(t, s, r.path, r.body, tt.ua)
				got, ok := v.sent(r.upstream)
				if !ok {
					t.Fatalf("%s: the vendor wasn't asked at %s", r.name, r.upstream)
				}
				if got != tt.want {
					t.Errorf("%s: the vendor was asked as %q, want %q", r.name, got, tt.want)
				}
			}
		})
	}
}

// Claude Code's and Codex's own headers still go on to a key+URL provider
// as before (#179, #1758), whether it passes the agent's User-Agent or
// not: the passing on is the same User-Agent.
func TestUserAgentClaudeCodeAsBefore(t *testing.T) {
	const cc = "claude-cli/2.1.0 (external, cli)"
	for _, pass := range []bool{false, true} {
		v := uaSetup(t, pass, nil)
		askAs(t, New(), "/v1/messages", uaRoutes[0].body, cc)
		if got, _ := v.sent("/v1/messages"); got != cc {
			t.Errorf("pass %v: Claude Code's request went as %q", pass, got)
		}
	}
}

// Which providers pass it on: a key+URL one unless set to go as magpie, and
// not a signed-in
// account (ZCode's Coding Plan signs without a User-Agent of its own, and
// goes as magpie), a plugin's, a vendor's own client's gateway (OpenCode,
// Cline, Kilo) or another magpie, each of which goes as the client it
// signs as.
func TestUserAgentFor(t *testing.T) {
	const pi = "pi/1"
	magpie := "magpie/" + Version
	for _, tt := range []struct {
		name string
		p    provider.Provider
		want string
	}{
		{"key", provider.Provider{Chat: "https://api.example.com/v1"}, pi},
		{"key, as magpie", provider.Provider{Chat: "https://api.example.com/v1", MagpieUserAgent: true}, magpie},
		{"account", provider.Provider{Chat: "https://api.z.ai/api/coding/paas/v4", Account: &provider.Account{Agent: "zcode"}}, magpie},
		{"opencode", provider.Provider{Chat: "https://opencode.ai/zen/v1"}, magpie},
		{"cline", provider.Provider{Preset: "clinepass", Chat: "https://api.cline.bot/api/v1"}, magpie},
		{"kilo", provider.Provider{Preset: "kilo", Chat: "https://api.kilo.ai/api/gateway"}, magpie},
		{"remote magpie", provider.Provider{Preset: provider.RemoteMagpiePreset, Chat: "http://10.0.0.2:3425/v1"}, magpie},
	} {
		if got := userAgentFor(tt.p, pi); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	// an account's own request headers are its signing's: stampUA leaves
	// them as they are
	h := http.Header{}
	stampUA(t.Context(), provider.Provider{Account: &provider.Account{Agent: "zcode"}}, h)
	if h.Get("User-Agent") != "" {
		t.Errorf("an account's request was stamped %q", h.Get("User-Agent"))
	}
}

// Through another computer's magpie (a Remote magpie provider), the vendor
// at the far end is asked with the agent's User-Agent too, not the near
// magpie's (nb5p on Discord): the near one hands it over in a header of
// magpie's own, which the far one takes only from a magpie, and which no
// vendor is sent. An agent that writes that header itself is asked as it
// is.
func TestRemoteMagpieRelaysUserAgent(t *testing.T) {
	const pi = "pi-coding-agent/0.9.1 (darwin; arm64)"
	v := uaSetup(t, true, nil)
	far := httptest.NewServer(New().Handler())
	t.Cleanup(far.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(far.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, r := range uaRoutes {
		if r.name == "gemini" {
			continue
		}
		body := strings.Replace(r.body, `"ua/`, `"`+id+`/ua/`, 1)
		askAs(t, s, r.path, body, pi)
		if got, ok := v.sent(r.upstream); !ok || got != pi {
			t.Errorf("%s: the far vendor was asked as %q (asked: %v), want the agent's", r.name, got, ok)
		}
	}
	// an agent that sets magpie's header itself, straight to the far one
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", far.URL+"/v1/chat/completions", strings.NewReader(uaRoutes[2].body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "curl/8.0")
	req.Header.Set(ClientUAHeader, pi)
	s.Handler().ServeHTTP(rec, req)
	if got, _ := v.sent("/v1/chat/completions"); rec.Code != 200 || got != "curl/8.0" {
		t.Errorf("a client's own %s was taken: %d, asked as %q", ClientUAHeader, rec.Code, got)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.leaked) > 0 {
		t.Errorf("a vendor was sent %s: %v", ClientUAHeader, v.leaked)
	}
}
