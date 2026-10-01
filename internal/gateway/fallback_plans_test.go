package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Free ChatGPT account behind a Plus one: when the Plus one is out of
// quota, a model the Free plan lacks is not sent there to fail with a 400;
// one it has is.
func TestSubscriptionAccountSkippedForModelItLacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("plus@example.com", "acct-plus")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "free@example.com", "on": true, "seen": time.Now(), "auth": auth("free@example.com", "acct-free")},
	}), 0o600)

	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		if strings.HasSuffix(r.URL.Path, "/models") {
			models := `{"slug":"gpt-5.6-luna","visibility":"list"}`
			if acct == "acct-plus" {
				models += `,{"slug":"gpt-5.5","visibility":"list"}`
			}
			io.WriteString(w, `{"models":[`+models+`]}`)
			return
		}
		tried = append(tried, acct)
		if acct == "acct-plus" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"You've hit your usage limit"}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.6-luna"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	code, _ := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code != 429 || strings.Join(tried, ",") != "acct-plus" {
		t.Fatalf("gpt-5.5: status %d, tried %v", code, tried)
	}
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	tried = nil
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.6-luna","input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") || strings.Join(tried, ",") != "acct-plus,acct-free" {
		t.Fatalf("gpt-5.6-luna: status %d, tried %v: %s", code, tried, body)
	}
}
