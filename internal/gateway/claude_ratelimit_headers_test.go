package gateway

import (
	"context"
	"io"
	"net/http"
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

// A reply a Claude subscription account answered says that account's
// allowance as Anthropic says it to Claude Code, in its
// anthropic-ratelimit-unified-* headers: a Claude Code signed in to
// claude.ai reads them into its status line's rate_limits, where
// claude-hud shows the 5-hour and weekly figures (#1257). The run's agent
// tells them in its rate_limit_event, which comes before the reply's first
// event, so the reply says this turn's.
func TestClaudeSubscriptionReplySaysTheAllowance(t *testing.T) {
	five := time.Now().Add(3 * time.Hour).Unix()
	week := time.Now().Add(4 * 24 * time.Hour).Unix()
	user := "hud-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.com"
	limits := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.42,"resetsAt":` + strconv.FormatInt(five, 10) +
		`,"unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":` + strconv.FormatInt(five, 10) + `},"seven_day":{"utilization":0.17,"resetsAt":` + strconv.FormatInt(week, 10) + `}}}}`
	answer := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":3,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
	ask := func(name, owner string, stream bool, lines []string) *httptest.ResponseRecorder {
		t.Helper()
		s := New()
		start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			run := &subscriptionRun{bridge: s.subscription, owner: owner, token: "hud", pending: map[string]chan mcpToolResult{}}
			events := run.attach()
			go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			return run, events, nil
		}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, name, "claude-sonnet-5", owner, []byte(body), &u, start); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		return rec
	}
	says := func(rec *httptest.ResponseRecorder, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %q, want %q (headers %v)", k, got, v, rec.Header())
			}
		}
	}
	owner := "claude\x00" + user
	told := map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.42",
		"anthropic-ratelimit-unified-5h-reset":       strconv.FormatInt(five, 10),
		"anthropic-ratelimit-unified-7d-utilization": "0.17",
		"anthropic-ratelimit-unified-7d-reset":       strconv.FormatInt(week, 10),
	}
	for _, stream := range []bool{true, false} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			// the turn tells it
			says(ask("Claude Code", owner, stream, append([]string{limits}, answer...)), told)
			// a turn that tells nothing says what is kept
			says(ask("Claude Code", owner, stream, answer), told)
		})
	}
	// another account's reply says nothing of this one's
	if rec := ask("Claude Code", "claude\x00other-"+user, true, answer); rec.Header().Get("anthropic-ratelimit-unified-5h-utilization") != "" {
		t.Errorf("another account's reply says %v", rec.Header())
	}
	// another agent's run (Codex's, Cursor's) has no Claude allowance to say
	if rec := ask("Agent", owner, true, answer); rec.Header().Get("anthropic-ratelimit-unified-5h-utilization") != "" {
		t.Errorf("another agent's reply says %v", rec.Header())
	}
}

// Through the gateway as it runs, to a Claude Code signed in to the
// account and answering as Claude Code does, streamed or not: the reply's
// headers carry what its rate_limit_event said, past the writer that holds
// a reply while another account may still take over.
func TestClaudeAllowanceHeadersThroughTheGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-me", "refreshToken": "r-me", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "pro"}}), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "hud@example.com", "accountUuid": "u-hud"}}), 0o600)
	five := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
	week := strconv.FormatInt(time.Now().Add(5*24*time.Hour).Unix(), 10)
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"hud@example.com","subscriptionType":"pro"}'; exit 0; fi
while read -r line; do
  echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.63,"resetsAt":`+five+`},"seven_day":{"utilization":0.08,"resetsAt":`+week+`}}}}'
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
done
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, stream := range []string{"true", "false"} {
		res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":`+stream+`,"messages":[{"role":"user","content":"ping `+stream+`"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), "pong") {
			t.Fatalf("stream=%s: %d %s", stream, res.StatusCode, b)
		}
		for k, v := range map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "0.63",
			"anthropic-ratelimit-unified-5h-reset":       five,
			"anthropic-ratelimit-unified-7d-utilization": "0.08",
			"anthropic-ratelimit-unified-7d-reset":       week,
		} {
			if got := res.Header.Get(k); got != v {
				t.Errorf("stream=%s: %s %q, want %q (headers %v)", stream, k, got, v, res.Header)
			}
		}
	}
}
