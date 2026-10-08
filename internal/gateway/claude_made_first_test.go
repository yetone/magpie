package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeMadeFirst signs Claude Code in to a@example.com, with
// b@example.com added in magpie beside it, as nil_1024 had them, and puts
// a Claude Code in PATH that answers as the account whose sign-in it reads
// at each turn, from the config directory it runs in, as Claude Code reads
// its keychain again every half minute: b's 5 hours are spent, a's not;
// with bRefuses b's turns are turned away as Claude Code does, out of
// quota, else answered.
func claudeMadeFirst(t *testing.T, bRefuses bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": tok + "-refresh",
			"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-a")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "b@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-b"),
			"profile": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"}},
	}), 0o600)

	bin := t.TempDir()
	refuses := ""
	if bRefuses {
		refuses = "yes"
	}
	session := time.Now().UTC().Add(3 * time.Hour).Format("Jan 2 at 3:04pm")
	reset := strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10)
	script := `#!/bin/sh
case "$1" in auth) exit 1;; esac
creds="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json"
if [ "$2" = "/usage" ]; then
  used=10
  grep -q tok-b "$creds" && used=100
  echo '{"type":"result","is_error":false,"result":"Current session: '$used'% used · resets ` + session + ` (UTC)"}'
  exit 0
fi
n=0
while read -r line; do
  n=$((n+1))
  tok=$(grep -o 'tok-[a-z]*' "$creds" | head -1)
  if [ "$tok" = "tok-b" ]; then
    echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","utilization":1,"resetsAt":` + reset + `}}'
    if [ -n "` + refuses + `" ]; then
      echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You'"'"'ve hit your limit"}]},"error":"rate_limit"}'
      echo '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your limit"}'
      continue
    fi
  else
    echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.1,"resetsAt":` + reset + `}}'
  fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'$tok' turn '$n'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(bin, "claude"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}

// nil_1024: Claude Code signed in to A, B added in magpie and made first
// (Claude Code's own sign-in moved to B), B's 5 hours spent. A went on
// being asked in the Claude Code kept for its conversation, in Claude
// Code's own home, which by then read B's sign-in: A's turns were B's,
// refused as spent, and what Claude Code said of B's 5 hours was kept as
// A's, A shown as spent though nobody had used it. A's next turn goes to
// a Claude Code of A's own, on A's sign-in, and A's 5 hours are A's.
func TestClaudeMadeFirstKeepsTheOtherAccountItsOwn(t *testing.T) {
	claudeMadeFirst(t, false)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	ask := func(p provider.Provider, msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var res struct {
			Content []struct{ Text string } `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if len(res.Content) == 0 {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return res.Content[0].Text
	}
	msg := func(role, text string) string { return `{"role":"` + role + `","content":` + strconv.Quote(text) + `}` }

	// Claude Code's own sign-in, A
	a := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}
	first := ask(a, `[`+msg("user", "hi")+`]`+"")
	if first != "tok-a turn 1" {
		t.Fatalf("first turn: %q", first)
	}

	// B made first: Claude Code is signed in to B, A is saved beside it
	if err := provider.SwitchLogin("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	b := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "b@example.com"}}
	var saved *provider.Provider
	for _, q := range b.AlsoOn() {
		if q.Account.User == "a@example.com" {
			saved = &q
		}
	}
	if saved == nil {
		t.Fatalf("A isn't on beside B: %+v", provider.Logins("claude"))
	}

	// A's conversation goes on
	second := ask(*saved, `[`+msg("user", "hi")+`,`+msg("assistant", first)+`,`+msg("user", "and?")+`]`)
	if second != "tok-a turn 1" {
		t.Errorf("A's next turn was answered %q: not by a Claude Code on A's own sign-in", second)
	}
	// B, Claude Code's own now, is asked as itself
	if got := ask(b, `[`+msg("user", "hello")+`]`); got != "tok-b turn 1" {
		t.Errorf("B: %q", got)
	}
	usage := provider.LoginUsage(context.Background(), "claude")
	for user, want := range map[string]float64{"a@example.com": 10, "b@example.com": 100} {
		q := usage[user]
		if len(q.Windows) == 0 {
			t.Fatalf("%s's usage: %+v", user, q)
		}
		for _, w := range q.Windows {
			if w.Name == "5 hours" && w.Used != want {
				t.Errorf("%s's 5 hours: %v%% used, want %v %+v", user, w.Used, want, q)
			}
		}
	}
}

// nil_1024, through the gateway, routing Smart: B made first and spent,
// A's conversation goes on on A, answered, where it was turned away as B
// (and the agent, retrying, gave up: 499).
func TestClaudeMadeFirstSpentGoesToTheOther(t *testing.T) {
	claudeMadeFirst(t, true)
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	if p, err := provider.Find("claude"); err != nil || p.Routing != "" {
		t.Fatalf("routing %q (want Smart, the default): %v", p.Routing, err)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	h := s.Handler()
	send := func(msgs string) (int, string) {
		t.Helper()
		body := `{"model":"claude/claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		done := make(chan struct{})
		rec := httptest.NewRecorder()
		go func() {
			defer close(done)
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
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
	msg := func(role, text string) string { return `{"role":"` + role + `","content":` + strconv.Quote(text) + `}` }

	code, first := send(`[` + msg("user", "hi") + `]`)
	if code != 200 || first != "tok-a turn 1" {
		t.Fatalf("first turn: %d %q", code, first)
	}
	if err := provider.SwitchLogin("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	code, second := send(`[` + msg("user", "hi") + `,` + msg("assistant", first) + `,` + msg("user", "and?") + `]`)
	if code != 200 || !strings.HasPrefix(second, "tok-a turn ") {
		t.Fatalf("A's conversation with B made first and spent: %d %q", code, second)
	}
	q := provider.LoginUsage(context.Background(), "claude")["a@example.com"]
	for _, w := range q.Windows {
		if w.Name == "5 hours" && w.Used != 10 {
			t.Fatalf("A's 5 hours: %v%% used, want 10 %+v", w.Used, q)
		}
	}
}
