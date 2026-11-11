package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// goUnavailable is what OpenCode Go answered on 2026-11-02 to a model it
// doesn't serve (deepseek-v4.1-flash-free, Zen's), the same on its chat
// and Messages APIs, before it looked at the key.
const goUnavailable = `{"type":"error","error":{"type":"api_error","message":"Upstream request failed: Model is unavailable."}}`

type goAsk struct{ url, auth, apiKey string }

// streamFor is a stream saying text in the shape of the API path is on.
func streamFor(path, text string) string {
	if strings.HasSuffix(path, "/chat/completions") {
		return chatSSE(text, "", "")
	}
	return anthropicSSE(text)
}

// goFreeServer is a gateway whose provider go is OpenCode Go as the preset
// adds it, with a Go key, listing the models given; Go answers
// goUnavailable to every model but those in serves, and Zen's free tier
// answers zen (status, then body) to everything, a stream saying "from
// zen" when the status is 200.
func goFreeServer(t *testing.T, models, serves []string, zenStatus int, zenRefusal string) (*Server, func() []goAsk) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "go", Name: "OpenCode Go", Preset: "opencode-go", Key: "sk-go-users-own",
		Chat: provider.OpenCodeGoChat, Responses: provider.OpenCodeGoChat, Anthropic: provider.OpenCodeGoAnthropic,
		Models: models}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []goAsk
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		mu.Lock()
		asked = append(asked, goAsk{r.URL.Host + r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key")})
		mu.Unlock()
		model := ""
		for _, m := range models {
			if strings.Contains(string(b), `"model":"`+m+`"`) {
				model = m
			}
		}
		if strings.HasPrefix(r.URL.Path, "/inference/go/") {
			for _, m := range serves {
				if m == model {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
						Body: io.NopCloser(strings.NewReader(streamFor(r.URL.Path, "from go")))}, nil
				}
			}
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(goUnavailable))}, nil
		}
		if zenStatus < 300 {
			return &http.Response{StatusCode: zenStatus, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(streamFor(r.URL.Path, "from zen")))}, nil
		}
		return &http.Response{StatusCode: zenStatus, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(zenRefusal))}, nil
	})}
	return s, func() []goAsk { mu.Lock(); defer mu.Unlock(); return append([]goAsk(nil), asked...) }
}

func goClaudeAsk(s *Server, model string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"`+model+`","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}],`+claudeTools+`}`))
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// 51bit.eth's case: Claude Code asking one of Zen's free models on an
// OpenCode Go provider. Go answers "Model is unavailable"; the request is
// asked again at Zen's free tier, where OpenCode asks it, with the public
// key and never the user's Go key, and Claude Code gets Zen's reply.
func TestOpenCodeGoFreeModelAskedAtZen(t *testing.T) {
	s, asked := goFreeServer(t, []string{"deepseek-v4.1-flash-free"}, nil, 200, "")
	rec := goClaudeAsk(s, "go/deepseek-v4.1-flash-free")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from zen") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a := asked()
	// on the API Go was asked on, at Zen's free tier
	want := map[string]string{"opencode.ai/inference/go/anthropic/v1/messages": "opencode.ai/inference/anthropic/v1/messages",
		"opencode.ai/inference/go/openai/v1/chat/completions": "opencode.ai/inference/openai/v1/chat/completions"}
	if len(a) != 2 || want[a[0].url] == "" || a[1].url != want[a[0].url] {
		t.Fatalf("asked %+v", a)
	}
	z := a[1]
	if z.auth != "Bearer public" || z.apiKey != "" && z.apiKey != "public" || strings.Contains(z.auth+z.apiKey, "sk-go") {
		t.Fatalf("Zen asked with %+v", z)
	}
}

// A free model Go serves itself stays on Go, asked once, with the Go key.
func TestOpenCodeGoOwnFreeModelStaysOnGo(t *testing.T) {
	s, asked := goFreeServer(t, []string{"beam-free"}, []string{"beam-free"}, 500, `{"error":{"message":"zen asked"}}`)
	rec := goClaudeAsk(s, "go/beam-free")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from go") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if a := asked(); len(a) != 1 || !strings.HasPrefix(a[0].url, "opencode.ai/inference/go/") {
		t.Fatalf("asked %+v", a)
	}
}

// Refused at Zen too, the refusal says what both said and where the model
// is served; a paid model Go doesn't serve isn't asked at Zen at all, and
// its refusal says to pick one of Go's.
func TestOpenCodeGoUnavailableSaysWhere(t *testing.T) {
	s, asked := goFreeServer(t, []string{"cat-jam-free", "claude-opus-5"}, nil, 400,
		`{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`)
	rec := goClaudeAsk(s, "go/cat-jam-free")
	body := rec.Body.String()
	if rec.Code < 400 || !strings.Contains(body, "OpenCode Go doesn't serve cat-jam-free") || !strings.Contains(body, "OpenCode Zen provider, which needs no key") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	n := len(asked())
	rec = goClaudeAsk(s, "go/claude-opus-5")
	body = rec.Body.String()
	if rec.Code < 400 || !strings.Contains(body, "OpenCode Go doesn't serve claude-opus-5: pick a model the OpenCode Go provider lists") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, a := range asked()[n:] {
		if !strings.HasPrefix(a.url, "opencode.ai/inference/go/") {
			t.Fatalf("a paid model asked at %s", a.url)
		}
	}
}
