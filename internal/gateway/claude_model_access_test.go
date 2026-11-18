package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeMythosAccounts signs Claude Code in to a@example.com, with
// b@example.com on beside it, and puts a Claude Code in PATH that answers
// as the account whose sign-in it reads, except Claude Mythos 5.2 on A,
// which it turns away as Claude Code does for a model the account may not
// call: Anthropic's 404, word for word. With bOffers, B's Claude Code was
// told of Mythos 5.2 (additionalModelOptionsCache) and A's of nothing
// beyond models.dev's list, as Claude Code keeps them; without it neither
// says. asks is the file each turn's account and model are written to.
func claudeMythosAccounts(t *testing.T, bOffers bool) (asks string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	noRests()
	t.Cleanup(noRests)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic": {"id": "anthropic", "name": "Anthropic", "npm": "@ai-sdk/anthropic",
 "env": ["ANTHROPIC_API_KEY"], "models": {"claude-sonnet-5": {"id": "claude-sonnet-5", "name": "Claude Sonnet 5",
 "release_date": "2026-05-01", "limit": {"context": 200000, "output": 64000},
 "modalities": {"input": ["text"], "output": ["text"]}}}}}`), 0o600)
	os.WriteFile(catalog.LabsPath(), []byte(`{}`), 0o600)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": tok + "-refresh",
			"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}}
	}
	state := func(email, uuid string, offers bool) []byte {
		opts := []any{}
		if offers {
			opts = append(opts, map[string]any{"value": "claude-mythos-5-2[1m]", "label": "Mythos 5.2", "description": "Restricted research model"})
		}
		return mustJSON(map[string]any{
			"oauthAccount":                map[string]any{"emailAddress": email, "accountUuid": uuid},
			"additionalModelOptionsCache": opts, "additionalModelOptionsAnsweredAt": time.Now().UnixMilli(),
			"modelAccessCache": []any{}, "additionalModelCostsCache": map[string]any{},
		})
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-a")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), state("a@example.com", "u-a", false), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "b@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-b"),
			"profile": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"}},
	}), 0o600)
	// B's own config directory, as magpie names it (claudeAccountDir)
	sum := sha256.Sum256([]byte("b@example.com"))
	bdir := filepath.Join(filepath.Dir(provider.Path()), "claude-accounts", hex.EncodeToString(sum[:])[:16])
	os.MkdirAll(bdir, 0o700)
	os.WriteFile(filepath.Join(bdir, ".claude.json"), state("b@example.com", "u-b", bOffers), 0o600)

	bin := t.TempDir()
	asks = filepath.Join(bin, "asks")
	notFound := "There'\"'\"'s an issue with the selected model ($model). It may not exist or you may not have access to it. Run --model to pick a different model."
	script := `#!/bin/sh
case "$1" in auth) exit 1;; esac
creds="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json"
model=
while [ $# -gt 0 ]; do
  [ "$1" = "--model" ] && model="$2"
  shift
done
model=${model%\[1m\]}
n=0
while read -r line; do
  n=$((n+1))
  tok=$(grep -o 'tok-[a-z]*' "$creds" | head -1)
  echo "$tok $model" >> ` + asks + `
  if [ "$tok" = "tok-a" ] && [ "$model" = "claude-mythos-5-2" ]; then
    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"` + notFound + `"}]},"error":"model_not_found","request_id":"req_1","is_api_error_message":true,"api_error_status":404,"api_error_type":"not_found_error"}'
    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":404,"api_error_type":"not_found_error","result":"` + notFound + `"}'
    continue
  fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"'$model'","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'$tok' '$model'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(bin, "claude"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	return asks
}

func claudeAsk(t *testing.T, s *Server, model string) (int, string) {
	t.Helper()
	h := s.Handler()
	body := `{"model":"claude/` + model + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("no answer")
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 {
		return rec.Code, rec.Body.String()
	}
	return rec.Code, res.Content[0].Text
}

func asksOf(t *testing.T, path, ask string) int {
	t.Helper()
	b, _ := os.ReadFile(path)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if l == ask {
			n++
		}
	}
	return n
}

// ZacharyZhang on X: of several Claude accounts one has Claude Mythos 5.2.
// Its Claude Code was told so, the others' of nothing beyond models.dev's
// list: Mythos 5.2 goes to that account, and the others are never asked
// it (they would answer 404 "may not have access to it").
func TestClaudeGatedModelGoesToTheAccountThatHasIt(t *testing.T) {
	asks := claudeMythosAccounts(t, true)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	for i := range 2 {
		if code, got := claudeAsk(t, s, "claude-mythos-5-2"); code != 200 || got != "tok-b claude-mythos-5-2" {
			t.Fatalf("Mythos 5.2, ask %d: %d %q", i+1, code, got)
		}
	}
	if n := asksOf(t, asks, "tok-a claude-mythos-5-2"); n != 0 {
		t.Errorf("A, whose Claude Code offers no Mythos 5.2, was asked it %d times", n)
	}
	if code, got := claudeAsk(t, s, "claude-sonnet-5"); code != 200 || got != "tok-a claude-sonnet-5" {
		t.Errorf("Sonnet 5 left A, the first account: %d %q", code, got)
	}
}

// When no Claude Code has said which account has the model, the one that
// answers 404 "may not have access to it" rests for that model alone: its
// other models stay on it, and the model isn't asked of it again at once.
func TestClaudeModelNoAccessRestsTheModelOnly(t *testing.T) {
	asks := claudeMythosAccounts(t, false)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	if code, got := claudeAsk(t, s, "claude-mythos-5-2"); code != 200 || got != "tok-b claude-mythos-5-2" {
		t.Fatalf("Mythos 5.2: %d %q", code, got)
	}
	if n := asksOf(t, asks, "tok-a claude-mythos-5-2"); n != 1 {
		t.Fatalf("A asked Mythos 5.2 %d times, want 1 (neither account's Claude Code said)", n)
	}
	if code, got := claudeAsk(t, s, "claude-sonnet-5"); code != 200 || got != "tok-a claude-sonnet-5" {
		t.Errorf("A rested for every model over Mythos 5.2's 404: Sonnet 5 answered %d %q", code, got)
	}
	if code, got := claudeAsk(t, s, "claude-mythos-5-2"); code != 200 || got != "tok-b claude-mythos-5-2" {
		t.Fatalf("Mythos 5.2 again: %d %q", code, got)
	}
	if n := asksOf(t, asks, "tok-a claude-mythos-5-2"); n != 1 {
		t.Errorf("A asked Mythos 5.2 again while it rests: %d asks", n)
	}
}

// Anthropic's 404 for a model the account may not call, as the gateway
// writes Claude Code's words and as the API answers a key, rests the
// model alone; a 404 of another kind that says "may not exist" doesn't.
func TestAnthropicModelNoAccessIsTheModels(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{"type":"not_found_error","message":"There's an issue with the selected model (claude-mythos-5-2). It may not exist or you may not have access to it. Run --model to pick a different model."}}`,
		`{"type":"error","error":{"type":"not_found_error","message":"model: claude-mythos-5-2"},"request_id":"req_011"}`,
	} {
		if !modelRefused(404, []byte(body)) {
			t.Errorf("not the model's: %s", body)
		}
	}
	for _, body := range []string{
		`{"error":{"code":404,"message":"Permission 'aiplatform.endpoints.predict' denied on resource (or it may not exist).","status":"NOT_FOUND"}}`,
		`{"type":"error","error":{"type":"not_found_error","message":"Not found"}}`,
	} {
		if modelRefused(404, []byte(body)) {
			t.Errorf("read as the model's: %s", body)
		}
	}
}
