package gateway

import (
	"encoding/json"
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

// keptOut is what claude-limits.json keeps for user: until when the
// switch holds the account out, and why.
func keptOut(t *testing.T, user string) (time.Time, string, bool) {
	t.Helper()
	var m map[string]struct {
		Until time.Time `json:"until"`
		Said  string    `json:"said"`
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "claude-limits.json"))
	if err != nil {
		return time.Time{}, "", false
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("claude-limits.json: %v", err)
	}
	s, ok := m[strings.ToLower(user)]
	return s.Until, s.Said, ok
}

// A Claude account the gateway rests whole for its allowance is out for
// the switch as well, till the same time: the switch moved Claude Code
// onto an account the gateway's bridge had just been refused on and set
// aside. One model's limit (Fable's) rests that model only, and the
// account is not held out; nor is it for a rate limit, a moment's.
func TestClaudeAccountRestKeptOutForTheSwitch(t *testing.T) {
	fresh(t)
	s := &Server{}
	acct := func(user, model string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: user}}, model: model, rest: "claude"}
	}
	body := func(msg string) []byte {
		return []byte(`{"type":"error","error":{"type":"rate_limit_error","message":` + strconv.Quote(msg) + `}}`)
	}
	resets := time.Now().Add(49 * time.Hour).Truncate(time.Second)

	// the week's refusal, its reset in the header the bridge sets from
	// the rate_limit_event's resetsAt
	week := acct("week@example.com", "claude-opus-5-5")
	h := http.Header{resetsHeader: {strconv.FormatInt(resets.Unix(), 10)}}
	r := s.restAfter(week, http.StatusTooManyRequests, h, body("You've hit your weekly limit · resets Nov 12 at 2pm (Asia/Shanghai)"))
	if r.Key != week.restKey() {
		t.Fatalf("rest %+v, want the account's", r)
	}
	until, said, ok := keptOut(t, "week@example.com")
	if !ok || !until.Equal(resets) || !until.Equal(r.Until) {
		t.Fatalf("kept out %v till %v, want till the rest's %v (%v)", ok, until, r.Until, resets)
	}
	if !strings.Contains(said, "weekly limit") {
		t.Fatalf("kept as %q, not Anthropic's words", said)
	}

	// Fable's own limit: the model rests, the account is not out
	fable := acct("fable@example.com", "claude-fable-5-1")
	if r := s.restAfter(fable, http.StatusTooManyRequests, http.Header{}, body("Claude Code: You've reached your Fable limit. Run /usage-credits to continue or switch models with /model.")); r.Key != fable.restID() {
		t.Fatalf("Fable's limit rested %+v, want the model", r)
	}
	if _, _, ok := keptOut(t, "fable@example.com"); ok {
		t.Fatal("Fable's own limit held the whole account out")
	}

	// a rate limit, with quota left, is a moment's
	rate := acct("rate@example.com", "claude-opus-5-5")
	s.restAfter(rate, http.StatusTooManyRequests, http.Header{"Retry-After": {"20"}}, body("Rate limited. Please try again later."))
	if _, _, ok := keptOut(t, "rate@example.com"); ok {
		t.Fatal("a rate limit held the account out")
	}

	// a refusal naming no reset is held as long as the gateway rests it,
	// and never cuts short what Anthropic said before
	guess := acct("week@example.com", "claude-sonnet-5-5")
	s.restAfter(guess, http.StatusTooManyRequests, http.Header{}, body("You've hit your limit"))
	if until, _, _ := keptOut(t, "week@example.com"); !until.Equal(resets) {
		t.Fatalf("a guessed rest moved the kept reset to %v, from %v", until, resets)
	}
	// nor what the switch heard elsewhere (Claude Code's own session, read
	// for its refusal) while the gateway had no rest of its own
	heard := acct("heard@example.com", "claude-sonnet-5-5")
	provider.NoteClaudeOut("heard@example.com", resets, "You've hit your weekly limit", true)
	s.restAfter(heard, http.StatusTooManyRequests, http.Header{}, body("You've hit your limit"))
	if until, _, _ := keptOut(t, "heard@example.com"); !until.Equal(resets) {
		t.Fatalf("a guessed rest moved the reset the switch heard to %v, from %v", until, resets)
	}
	none := acct("none@example.com", "claude-sonnet-5-5")
	r = s.restAfter(none, http.StatusTooManyRequests, http.Header{}, body("You've hit your limit"))
	if until, _, ok := keptOut(t, "none@example.com"); !ok || !until.Equal(r.Until) {
		t.Fatalf("kept %v till %v, want till the rest's %v", ok, until, r.Until)
	}
}

// refusingClaude puts in PATH a Claude Code signed in to a@example.com
// whose every turn is refused for the account's week, as Claude Code 2.1
// tells it in -p (the rate_limit_info of the owner's 11-10 refusal, ids
// redacted), the reset at resets.
func refusingClaude(t *testing.T, resets time.Time) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	fresh(t)
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-a", "refreshToken": "r", "expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}}), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}}), 0o600)
	r := strconv.FormatInt(resets.Unix(), 10)
	shanghai := time.FixedZone("CST", 8*3600)
	words := "You'\"'\"'ve hit your weekly limit · resets " + resets.In(shanghai).Format("Jan 2 at 3pm") + " (Asia/Shanghai)"
	bin := t.TempDir()
	testenv.Program(t, filepath.Join(bin, "claude"), `#!/bin/sh
case "$1" in auth) exit 1;; esac
if [ "$2" = "/usage" ]; then exit 1; fi
while read -r line; do
  echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":`+r+`,"rateLimitType":"seven_day","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false},"uuid":"u","session_id":"s"}'
  echo '{"type":"assistant","message":{"id":"m","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"`+words+`"}]},"error":"rate_limit"}'
  echo '{"type":"result","subtype":"success","is_error":true,"result":"`+words+`"}'
done
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}

// The bridge's own refusal, streamed or not: the gateway rests the
// account till Anthropic's reset, and the switch holds it out till then.
func TestClaudeBridgeRefusalKeptOut(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			resets := time.Now().Add(49 * time.Hour).Truncate(time.Hour)
			refusingClaude(t, resets)
			s := New()
			t.Cleanup(s.subscription.abortAll)
			body := `{"model":"claude/claude-sonnet-5","max_tokens":100,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"hi"}]}`
			done := make(chan struct{})
			rec := httptest.NewRecorder()
			go func() {
				defer close(done)
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("no answer")
			}
			until, said, ok := keptOut(t, "a@example.com")
			if !ok || !until.Equal(resets) {
				c := candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}}
				rested, _ := restingOf(c)
				t.Fatalf("kept out %v till %v (%q), want till %v; rest %+v; reply %d %s", ok, until, said, resets, rested, rec.Code, rec.Body)
			}
			c := candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}}
			if rested, ok := restingOf(c); !ok || !rested.Until.Equal(until) {
				t.Fatalf("the gateway rests the account till %v (%v), the switch holds it out till %v", rested.Until, ok, until)
			}
		})
	}
}
