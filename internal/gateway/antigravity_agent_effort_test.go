package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Antigravity's app lists the gateway's models by id and has no level of
// its own to pick (MDX-Tom, #4838): with an effort picked for it in
// magpie, its requests for one of Antigravity's families of levels go out
// as the variant at that level, told that level; with none picked they go
// as before, and another agent's requests never take its pick.
func TestAntigravityAgentEffort(t *testing.T) {
	fresh(t)
	logins := []map[string]any{{
		"agent": "antigravity", "user": "a@example.com", "on": true,
		"auth": map[string]any{"access_token": "a@example.com", "refresh_token": "ref-a", "project": "p1",
			"expiry_date": time.Now().Add(time.Hour).UnixMilli()},
	}}
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	// the family as Antigravity lists it: High first, then Low
	list := `{"models":{
		"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
		"gemini-3.8-flash-low":{"displayName":"Gemini 3.8 Flash (Low)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}}},
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":["gemini-3.8-flash-high","gemini-3.8-flash-low"]}]}]}`
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
			body = "version: 2.9.1\n"
		case strings.Contains(r.URL.Path, ":loadCodeAssist"):
			body = `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"p1"}`
		case strings.Contains(r.URL.Path, ":fetchAvailableModels"):
			body = list
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })
	p, _ := provider.Find("antigravity")
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []byte
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = b
		mu.Unlock()
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(rec, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
		return rec.Result(), nil
	})}
	// the id the envelope went out under and the level it was told
	asked := func(agent string) (string, string) {
		t.Helper()
		postFrom(t, s, agent, "/v1/chat/completions", `{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
		mu.Lock()
		defer mu.Unlock()
		var env struct {
			Model   string `json:"model"`
			Request struct {
				GenerationConfig struct {
					ThinkingConfig struct {
						ThinkingLevel string `json:"thinkingLevel"`
					} `json:"thinkingConfig"`
				} `json:"generationConfig"`
			} `json:"request"`
		}
		if err := json.Unmarshal(sent, &env); err != nil {
			t.Fatalf("%v: %s", err, sent)
		}
		return env.Model, env.Request.GenerationConfig.ThinkingConfig.ThinkingLevel
	}

	base, baseLevel := asked("antigravity")
	if base == "" {
		t.Fatal("no request went out")
	}
	for _, c := range []struct{ pick, model, level string }{
		{"high", "gemini-3.8-flash-high", "high"},
		{"low", "gemini-3.8-flash-low", "low"},
	} {
		if err := provider.SetAgentEffort("antigravity", c.pick); err != nil {
			t.Fatal(err)
		}
		if m, l := asked("antigravity"); m != c.model || l != c.level {
			t.Errorf("picked %s: went as %s at %q, want %s at %q", c.pick, m, l, c.model, c.level)
		}
		// another agent's request goes as it asks
		if m, l := asked("opencode"); m != base || l != baseLevel {
			t.Errorf("picked %s for Antigravity, opencode's went as %s at %q, want %s at %q", c.pick, m, l, base, baseLevel)
		}
	}
	if err := provider.SetAgentEffort("antigravity", ""); err != nil {
		t.Fatal(err)
	}
	if m, l := asked("antigravity"); m != base || l != baseLevel {
		t.Errorf("taken off: went as %s at %q, want %s at %q", m, l, base, baseLevel)
	}
}
