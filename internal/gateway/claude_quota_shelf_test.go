package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// quotaClaude stands in for Claude Code, keeping its session as it does
// (busyRetryClaude), and refusing every turn for the account's 5-hour
// limit while the file limited is there: the rate_limit_event, its
// synthetic error message and the error result, the refused turn's user
// line and error line written to the session all the same. resetsAt is
// the limit's reset the event says. It says where the per-run logs are
// and the file that turns the limit on.
func quotaClaude(t *testing.T, resetsAt int64) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir, logs := t.TempDir(), t.TempDir()
	limited := filepath.Join(dir, "limited")
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"u@example.com","subscriptionType":"max"}'; exit 0; fi
log=` + logs + `/$$
echo "args $*" > $log
sid=lead$$; resumed=; prev=
for a in "$@"; do
  if [ "$prev" = --resume ]; then sid=$a; resumed=1; fi
  prev=$a
done
proj="$CLAUDE_CONFIG_DIR/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
mkdir -p "$proj"
file="$proj/$sid.jsonl"
if [ -n "$resumed" ]; then
  [ -f "$file" ] || { echo "No conversation found with session ID: $sid" >&2; exit 1; }
fi
: >> "$file"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
n=0
while read -r line; do
  case "$line" in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue;;
  esac
  echo "told $line" >> $log
  n=$((n+1))
  echo '{"type":"user","uuid":"ask-'$$'-'$n'","sessionId":"'$sid'","message":{"role":"user","content":"told"}}' >> "$file"
  if [ -f ` + limited + ` ]; then
    echo '{"type":"assistant","uuid":"err-'$$'-'$n'","sessionId":"'$sid'","isApiErrorMessage":true,"error":"rate_limit","message":{"id":"e'$$'-'$n'","model":"<synthetic>","content":[{"type":"text","text":"You have hit your limit"}]}}' >> "$file"
    echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":` + strconv.FormatInt(resetsAt, 10) + `}}'
    echo '{"type":"assistant","message":{"id":"e'$$'-'$n'","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You have hit your limit · resets 9pm"}]},"error":"rate_limit"}'
    echo '{"type":"result","subtype":"success","is_error":true,"result":"You have hit your limit · resets 9pm"}'
    continue
  fi
  echo '{"type":"assistant","uuid":"line-'$$'-'$n'","sessionId":"'$sid'","message":{"id":"m'$$'-'$n'","model":"claude-sonnet-5"}}' >> "$file"
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$$'-'$n'","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}}'
  echo '{"type":"assistant","uuid":"line-'$$'-'$n'","message":{"id":"m'$$'-'$n'","model":"claude-sonnet-5","content":[{"type":"text","text":"pid '$$' turn '$n'"}]}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logs, limited
}

// quotaHarness is a busyRetryHarness whose Claude Code is refused for the
// account's limit while limited is there.
type quotaHarness struct {
	*busyRetryHarness
	limited string
	config  string
}

func newQuotaHarness(t *testing.T, mode string, resetsAt int64) *quotaHarness {
	logs, limited := quotaClaude(t, resetsAt)
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	config := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(config, 0o755)
	os.WriteFile(filepath.Join(config, ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok", "refreshToken": "r", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"}}), 0o600)
	if mode != "print" {
		claudeTUIRuns = true
		t.Cleanup(func() { claudeTUIRuns = false })
		helper := filepath.Join(t.TempDir(), "magpie")
		testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift; claude=$1; shift
FAKE_TUI=interactive exec "$claude" "$@"
`)
		claudeTUIHelper = helper
		t.Cleanup(func() { claudeTUIHelper = "" })
	}
	h := &quotaHarness{busyRetryHarness: &busyRetryHarness{rewindHarness: &rewindHarness{t: t, s: New(),
		p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u@example.com"}}}, logs: logs},
		limited: limited, config: config}
	t.Cleanup(func() { h.s.subscription.abortAll() })
	return h
}

// try asks msgs and says the status and the answer's text or error.
func (h *quotaHarness) try(msgs string) (int, string) {
	body := h.body(msgs)
	rec := httptest.NewRecorder()
	var u Usage
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	if code != 200 {
		return code, msg
	}
	return code, rec.Body.String()
}

// shelved is the bridge's one saved session, nil with none or more.
func (h *quotaHarness) shelved() *savedSession {
	b := h.s.subscription
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.shelf) != 1 {
		return nil
	}
	for _, s := range b.shelf {
		return s
	}
	return nil
}

// sessionFile reads the session id's file under Claude Code's projects.
func (h *quotaHarness) sessionFile(id string) string {
	var out string
	filepath.WalkDir(filepath.Join(h.config, "projects"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(path) == id+".jsonl" {
			b, _ := os.ReadFile(path)
			out = string(b)
		}
		return nil
	})
	return out
}

// A turn refused for the account's 5-hour limit, before any of its reply,
// leaves the conversation's session on the shelf, cut before the refused
// turn, until the limit resets and an hour more; the turn asked again
// after the reset, after a restart too, resumes it in a Claude Code told
// that turn alone (#6292, recih: the run ended with the 429, its session
// went with it, and the turn asked after the reset was told the whole
// conversation, 354,900 tokens written to the cache again). Asked again
// while still limited, the session is kept again, cut where it was.
func TestClaudeTurnRefusedForTheLimitKeepsItsSessionTillTheReset(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) { quotaKeepsSession(t, mode) })
	}
}

func quotaKeepsSession(t *testing.T, mode string) {
	reset := time.Now().Add(3 * time.Hour)
	h := newQuotaHarness(t, mode, reset.Unix())
	logs := keepLog(t)
	first := `the conversation so far, told at length ALPHA`
	code, answer := h.try(`[` + rmsg("user", first) + `]`)
	if code != 200 || !strings.Contains(answer, "turn 1") {
		t.Fatalf("the first turn: %d %s", code, answer)
	}
	said := strings.TrimSpace(answer[strings.Index(answer, "pid "):])
	said = said[:strings.Index(said, `"`)]
	conv := `[` + rmsg("user", first) + `,` + rmsg("assistant", said) + `,` + rmsg("user", "and run the tests BETA") + `]`

	os.WriteFile(h.limited, nil, 0o600)
	for try := 1; try <= 2; try++ {
		before := time.Now()
		if code, msg := h.try(conv); code != 429 {
			t.Fatalf("try %d while limited: %d %s\nmagpie log:\n%s", try, code, msg, logs.String())
		}
		s := h.shelved()
		if s == nil {
			t.Fatalf("try %d: no session kept for the turn asked again after the reset\nmagpie log:\n%s", try, logs.String())
		}
		// the reset the rate_limit_event said, an hour over
		if want := reset.Add(quotaShelfMargin); s.until.Before(want.Add(-time.Minute)) || s.until.After(want.Add(time.Minute)) {
			t.Fatalf("try %d: kept until %v, want about %v (asked at %v)", try, s.until, want, before)
		}
		// as the conversation stood before the refused turn: its first
		// turn whole, nothing of the refused one
		lines := h.sessionFile(s.session)
		if n := strings.Count(lines, "\n"); n != 2 || !strings.Contains(lines, `"type":"assistant","uuid":"line-`) ||
			strings.Contains(lines, "rate_limit") || strings.Contains(lines, "<synthetic>") {
			t.Fatalf("try %d: the kept session %s isn't the conversation before the refused turn:\n%s", try, s.session, lines)
		}
	}
	kept := h.shelved().session

	// magpie restarts while the account waits for its reset
	h.s.subscription.abortAll()
	if b, err := os.ReadFile(shelfPath()); err != nil || !strings.Contains(string(b), kept) || !strings.Contains(string(b), `"until"`) {
		t.Fatalf("the kept session isn't saved for the next start: %v %s", err, b)
	}
	h.s = New()

	os.Remove(h.limited)
	before := h.toldBy()
	code, answer = h.try(conv)
	if code != 200 || !strings.Contains(answer, "turn 1") {
		t.Fatalf("the turn after the reset: %d %s\nmagpie log:\n%s", code, answer, logs.String())
	}
	run := h.runSince(before)
	if !strings.Contains(run, "--resume "+kept) {
		t.Fatalf("the turn after the reset didn't resume the kept session %s:\n%s\nmagpie log:\n%s", kept, run, logs.String())
	}
	if strings.Contains(run, "ALPHA") || !strings.Contains(run, "BETA") {
		t.Fatalf("the turn after the reset was told more than the turn:\n%s", run)
	}
}

// runSince is the log of the run that started after before.
func (h *quotaHarness) runSince(before map[string]string) string {
	h.t.Helper()
	for pid, log := range h.toldBy() {
		if _, ok := before[pid]; !ok {
			return log
		}
	}
	h.t.Fatal("no new run started")
	return ""
}

// How long a session kept past a refused turn waits: the reset the
// refusal names, else the account's used-up windows' latest, else five
// hours; an hour over, no less than a run waits and no more than
// longestQuota.
func TestQuotaShelfFor(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	now := time.Now()
	owner := "claude\x00quota-shelf-user"
	if got := quotaShelfFor(owner, "You've hit your limit", now); got != quotaShelfUnsaid+quotaShelfMargin {
		t.Fatalf("unsaid: %v", got)
	}
	if got := quotaShelfFor(owner, fmt.Sprintf("usage limit reached|%d", now.Add(2*time.Hour).Unix()), now); got < 3*time.Hour-time.Minute || got > 3*time.Hour+time.Minute {
		t.Fatalf("said in the refusal: %v", got)
	}
	provider.NoteClaudeLimits("quota-shelf-user", []provider.ClaudeLimit{
		{Kind: "five_hour", Used: 1, ResetsAt: now.Add(90 * time.Minute).Unix()},
		{Kind: "seven_day", Used: 0.4, ResetsAt: now.Add(100 * time.Hour).Unix()},
	})
	if got := quotaShelfFor(owner, "You've hit your limit · resets 9pm", now); got < 150*time.Minute-time.Minute || got > 150*time.Minute+time.Minute {
		t.Fatalf("the account's used-up window: %v", got)
	}
	if got := quotaShelfFor(owner, fmt.Sprintf("usage limit reached|%d", now.Add(30*24*time.Hour).Unix()), now); got != quotaShelfLongest {
		t.Fatalf("bound: %v", got)
	}
	if got := quotaShelfFor(owner, fmt.Sprintf("usage limit reached|%d", now.Add(time.Minute).Unix()), now); got < idleLongest {
		t.Fatalf("less than a run waits: %v", got)
	}
}
