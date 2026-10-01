package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// A key added while conversations run doesn't take them over (#63): each
// stays with the one key that answered it so far, mid-turn.
func TestAddedKeyLeavesRunningConversations(t *testing.T) {
	fresh(t)
	v := &keyed{}
	serveOn(t, "aff", "k1", []string{"m"}, v)
	provider.SetRouting("aff", provider.Rotate)
	s := New()
	for i := range 4 {
		postAs(t, s, fmt.Sprint("s", i), `{"model":"aff/m","messages":[{"role":"user","content":"hi"}]}`)
	}
	serveOn(t, "aff", "k1", []string{"m"}, v, "k2") // the new key
	provider.SetRouting("aff", provider.Rotate)
	v.tried = nil
	for i := range 4 {
		postAs(t, s, fmt.Sprint("s", i), `{"model":"aff/m","messages":[{"role":"user","content":"hi"},`+
			`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`)
	}
	if strings.Join(v.tried, ",") != "k1,k1,k1,k1" {
		t.Fatalf("running conversations went to %v", v.tried)
	}
	// a new one may go to the new key
	v.tried = nil
	for i := range 2 {
		postAs(t, s, fmt.Sprint("new", i), `{"model":"aff/m","messages":[{"role":"user","content":"hi"}]}`)
	}
	if !strings.Contains(strings.Join(v.tried, ","), "k2") {
		t.Fatalf("new conversations went to %v", v.tried)
	}
}

// Codex reasoning another ChatGPT account sealed is refused by this one;
// the request is sent again without it rather than failing the agent.
func TestForeignReasoningIsLeftOut(t *testing.T) {
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
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      claims(map[string]any{"email": "me@example.com"}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r", "account_id": "acct-1"}}), 0o600)

	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), "sealed-elsewhere") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"The encrypted content for item rs_1 could not be verified.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},`+
		`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"sealed-elsewhere"},`+
		`{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},`+
		`{"type":"function_call_output","call_id":"c1","output":"a.go"}]}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(bodies) != 2 || strings.Contains(bodies[1], `"reasoning"`) || !strings.Contains(bodies[1], "function_call_output") {
		t.Fatalf("sent %q", bodies)
	}

	// a request at fault some other way is still the agent's to see
	bodies = nil
	code, _ = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"sealed-elsewhere"}]}]}`)
	if code != 400 || len(bodies) != 1 {
		t.Fatalf("%d after %d tries", code, len(bodies))
	}
}
