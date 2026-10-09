package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestClaudePassthroughOffByDefault(t *testing.T) {
	// Off by default: an OAuth request must NOT pass through to Anthropic upstream.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called when passthrough is off")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: false}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	// Since passthrough is off and no magpie provider is configured, it fails locally
	if rec.Code == 200 {
		t.Fatalf("unexpected success when passthrough is off")
	}
}

func TestClaudePassthroughNativeModel(t *testing.T) {
	var gotPath, gotAuth, gotBeta string
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"hello from anthropic"}],"model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Errorf("got path %q, want /v1/messages", gotPath)
	}
	if gotAuth != "Bearer sk-ant-oat01-test-token" {
		t.Errorf("got auth %q, want Bearer sk-ant-oat01-test-token", gotAuth)
	}
	if gotBeta != "claude-code-20250219" {
		t.Errorf("got beta %q, want claude-code-20250219", gotBeta)
	}
	if string(gotBody) != reqBody {
		t.Errorf("got body %q, want %q", string(gotBody), reqBody)
	}
	if !strings.Contains(rec.Body.String(), "hello from anthropic") {
		t.Errorf("expected response from upstream, got %s", rec.Body.String())
	}
}

func TestClaudePassthroughNonClaudeModel(t *testing.T) {
	// A bare model id that is not in the Claude family (e.g. gpt-5.5) must NOT pass through to Anthropic.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called for non-Claude model gpt-5.5")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	// Since gpt-5.5 is not a Claude family model, it must be handled by Magpie and not sent to Anthropic
	if rec.Code == 200 {
		t.Fatalf("unexpected success for gpt-5.5 on unconfigured provider")
	}
}

func TestClaudePassthroughMagpieModel(t *testing.T) {
	// A request asking for a Magpie-served model (provider/model) must NOT pass through to Anthropic.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called for magpie model")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	if rec.Code == 200 {
		t.Fatalf("unexpected success for fake/m1 on unconfigured provider")
	}
}

func TestClaudePassthroughNoRedaction(t *testing.T) {
	// When redaction is enabled in Magpie, passthrough requests to the user's
	// own Claude subscription must NOT have secrets rewritten or masked.
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5-5"}`)
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{
		ClaudePassthrough: true,
		Redact:            true,
		RedactPersonal:    true,
	}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"my secret is sk-ant-api03-12345678901234567890 and phone 13812345678"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if strings.Contains(string(gotBody), "{{API_KEY_") || strings.Contains(string(gotBody), "{{PHONE_") {
		t.Errorf("passthrough request was unexpectedly redacted: %s", string(gotBody))
	}
	if !strings.Contains(string(gotBody), "sk-ant-api03-12345678901234567890") {
		t.Errorf("passthrough body lost original content: %s", string(gotBody))
	}
}

func TestClaudePassthroughCountTokens(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"input_tokens":12}`)
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{
		ClaudePassthrough: true,
		Redact:            true,
		RedactPersonal:    true,
	}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"my key sk-ant-api03-abcdefghij1234567890"}]}`
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Errorf("got path %q, want /v1/messages/count_tokens", gotPath)
	}
	if gotAuth != "Bearer sk-ant-oat01-test-token" {
		t.Errorf("got auth %q, want Bearer sk-ant-oat01-test-token", gotAuth)
	}
	if strings.Contains(string(gotBody), "{{API_KEY_") {
		t.Errorf("count_tokens was unexpectedly redacted: %s", string(gotBody))
	}
	if !strings.Contains(rec.Body.String(), `"input_tokens":12`) {
		t.Errorf("expected response from upstream, got %s", rec.Body.String())
	}
}

func TestClaudePassthroughModelsList(t *testing.T) {
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"models":{"claude-sonnet-5":{"id":"claude-sonnet-5","name":"Claude Sonnet 5"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}

	foundSonnet := false
	for _, m := range res.Data {
		if strings.HasPrefix(m.ID, "claude-sonnet") {
			foundSonnet = true
			break
		}
	}
	if !foundSonnet {
		t.Errorf("expected claude-sonnet in /v1/models response when asked with sk-ant-oat token")
	}
}

func TestClaudePassthroughRestrictedToLocalClaude(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called for non-Claude agent or remote request")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()

	// 1. User-Agent from OpenCode
	{
		rec := httptest.NewRecorder()
		reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "opencode/1.18.34")
		req.RemoteAddr = "127.0.0.1:54321"

		s.Handler().ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("expected non-200 for OpenCode User-Agent")
		}
	}

	// 2. User-Agent from Pi
	{
		rec := httptest.NewRecorder()
		reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "pi-coding-agent")
		req.RemoteAddr = "127.0.0.1:54321"

		s.Handler().ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("expected non-200 for Pi User-Agent")
		}
	}

	// 3. Remote request (non-loopback)
	{
		rec := httptest.NewRecorder()
		reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "claude-code/2.1.293")
		req.RemoteAddr = "192.168.1.100:54321"

		s.Handler().ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("expected non-200 for remote request")
		}
	}

	// 4. Remote request to /v1/models should not include anthropic models
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
		req.Header.Set("User-Agent", "claude-code/2.1.293")
		req.RemoteAddr = "192.168.1.100:54321"

		s.Handler().ServeHTTP(rec, req)
		if rec.Code == 200 {
			var res struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err == nil {
				for _, m := range res.Data {
					if strings.HasPrefix(m.ID, "claude-") {
						t.Errorf("remote /v1/models should not merge claude catalog models: %s", m.ID)
					}
				}
			}
		}
	}
}

func TestClaudePassthroughTierStandInBypassesPassthrough(t *testing.T) {
	// When claudeTierStandIn returns a different model, the request must NOT pass through
	// directly to Anthropic so that the user's tier routing in Magpie is preserved.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called when model is mapped to a stand-in")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	wasStandIn := StandIn
	StandIn = func(agent, asked string) string {
		if agent == "claude" && asked == "claude-haiku-4-5" {
			return "fake/m1" // mapped to another model
		}
		return asked
	}
	defer func() { StandIn = wasStandIn }()

	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.1.293")
	req.RemoteAddr = "127.0.0.1:54321"

	s.Handler().ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatalf("unexpected success on stand-in without configured provider")
	}
}
