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
)

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

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("Content-Type", "application/json")

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

func TestClaudePassthroughCountTokens(t *testing.T) {
	var gotPath, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"input_tokens":12}`)
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")

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

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")

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

func TestClaudePassthroughMagpieModel(t *testing.T) {
	// A request with sk-ant-oat token asking for a Magpie-served model (provider/model)
	// must NOT pass through to Anthropic; it must be handled by Magpie's serve logic.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Anthropic upstream should not be called for magpie model")
	}))
	defer up.Close()

	was := provider.ClaudeBase
	provider.ClaudeBase = up.URL
	defer func() { provider.ClaudeBase = was }()

	s := New()
	rec := httptest.NewRecorder()
	reqBody := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-test-token")
	req.Header.Set("Content-Type", "application/json")

	s.Handler().ServeHTTP(rec, req)

	// Since fake/m1 is not configured in this test, it should be turned away or fail locally in Magpie,
	// but crucially NOT reach the upstream ClaudeBase server.
	if rec.Code == 200 {
		t.Fatalf("unexpected success for fake/m1")
	}
}
