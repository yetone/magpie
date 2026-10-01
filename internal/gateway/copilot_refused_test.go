package gateway

import (
	"encoding/json"
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

// #256: on a Copilot Student account, copilot/auto answered 400 "Copilot:
// The requested model is not supported." — Auto's session picked a model
// Copilot then refused the account (#371: claude-haiku-4.5, policy enabled
// and all). The gateway sends the request again with another model Auto
// offers, and the agent is answered.
func TestCopilotAutoRefusedThroughGateway(t *testing.T) {
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "student", "oauth_token": "gho_student371"},
	}), 0o600)

	var mu sync.Mutex
	var asked []string // "<model> <session token>"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[
			  {"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions","/v1/messages"],"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"is_chat_default":true,"is_chat_fallback":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}}]}`)
		case "/models/session":
			io.WriteString(w, `{"session_token":"auto-tok","selected_model":"claude-haiku-4.5","available_models":["claude-haiku-4.5","gpt-4.1"],"expires_at":`+
				mustString(time.Now().Add(time.Hour).Unix())+`}`)
		default:
			m := modelOf(b)
			asked = append(asked, m+" "+r.Header.Get("Copilot-Session-Token"))
			if m != "gpt-4.1" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"The requested model is not supported.","code":"model_not_supported","param":"model","type":"invalid_request_error"}}`)
				return
			}
			io.WriteString(w, `{"id":"c1","model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello from gpt-4.1"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`)
		}
	}))
	defer api.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual","access_type_sku":"free_educational_quota"}`)
	}))
	defer gh.Close()
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	defer func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser }()

	s := New()
	for range 2 {
		code, body := postAs(t, s, "", `{"model":"copilot/auto","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "hello from gpt-4.1") {
			t.Fatalf("copilot/auto: %d %s", code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// the refused model is asked once, then passed over
	if strings.Join(asked, "|") != "claude-haiku-4.5 auto-tok|gpt-4.1 auto-tok|gpt-4.1 auto-tok" {
		t.Fatalf("Copilot was asked %q", asked)
	}
}
