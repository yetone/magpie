package gateway

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A request the interactive mode refuses is refused the same on every
// account: it is a 400 no account rests for and the next isn't asked,
// where a 502 was asked of every account in turn and rested some (#4778).
func TestClaudeTUIRefusalIsNoAccountsFault(t *testing.T) {
	h, _ := tuiHelperHarness(t, "")
	body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"http://127.0.0.1:1/a.png"}},{"type":"text","text":"what is it?"}]}]}`
	var u Usage
	rec := httptest.NewRecorder()
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	if code != 400 || !strings.Contains(msg, "couldn't fetch the image") {
		t.Fatalf("%d %.300s", code, msg)
	}
	if retryable(code, rec.Body.Bytes()) {
		t.Fatalf("the refusal is asked of the next account: %s", rec.Body.String())
	}
}

// tuiThirdPartyRefusal is the 400 an interactive Claude Code answers a
// turn Anthropic counts as a third-party app's, in the words isjiajia01
// and sounfury reported (#4503).
const tuiThirdPartyRefusal = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 400 Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going.","api_error_status":400}`

// tuiThirdPartyHarness is tuiHelperHarness whose interactive Claude Code
// answers every turn with that refusal; as claude -p it answers "ok".
// (ggbdpq's harness, #4503.)
func tuiThirdPartyHarness(t *testing.T) (*resumeHarness, string) {
	t.Helper()
	start := `if [ -n "$MG_TEST_TUI" ]; then
  echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
  while read -r line; do
    case $line in *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
      continue ;;
    esac
    echo '` + tuiThirdPartyRefusal + `'
    exit 1
  done
  exit 0
fi
`
	h := newScriptHarness(t, "ok", start)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	started := filepath.Join(t.TempDir(), "started")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
echo start >> '`+started+`'
MG_TEST_TUI=1
export MG_TEST_TUI
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	return h, started
}

// A turn Anthropic refuses in the interactive mode as a third-party app's
// (#4503) reaches the client as a 400 that says what happened, that asking
// again may draw from extra usage, and where to turn the interactive mode
// off. magpie asks nothing again on its own: no claude -p run starts, which
// the plan counts at about twice and which may draw from extra usage (the
// owner's call), and no account rests for it or is asked next.
func TestClaudeTUIThirdPartyRefusalSaysWhy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			h, started := tuiThirdPartyHarness(t)
			s := ""
			if stream {
				s = `"stream":true,`
			}
			body := `{"model":"claude-sonnet-5","max_tokens":100,` + s + `"system":"You are pi, a coding agent harness.","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
			rec := httptest.NewRecorder()
			var u Usage
			code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
			if code != 400 {
				t.Fatalf("%d %.400s", code, msg)
			}
			for _, w := range []string{thirdPartyWords, "interactive mode with your agent's own system prompt", "may draw from the Claude account's extra usage", "doesn't ask again on its own", "Settings › Models › Replies"} {
				if !strings.Contains(msg, w) || !strings.Contains(rec.Body.String(), w) {
					t.Fatalf("the client isn't told %q: %s", w, rec.Body.String())
				}
			}
			if n := helperStarts(started); n != 1 {
				t.Fatalf("the interactive mode started %d times, want once", n)
			}
			if runs := h.runs(); len(runs) != 1 {
				t.Fatalf("%d runs, want the interactive one alone: nothing is asked again as claude -p", len(runs))
			}
			if retryable(code, rec.Body.Bytes()) {
				t.Fatalf("the refusal is asked of the next account: %s", rec.Body.String())
			}
			switch f := failure(code, rec.Body.Bytes()); f {
			case failCredit, failQuota, failRate, failAuth:
				t.Fatalf("the refusal reads as the account's (%s): %s", f, rec.Body.String())
			}
		})
	}
}

// The same refusal of a claude -p run says so without telling the user to
// turn the interactive mode off, which it already is.
func TestClaudeThirdPartyRefusalOfPrint(t *testing.T) {
	msg := thirdPartyRefused("Claude Code", &Request{Model: "m"}, 400, "API Error: 400 Third-party apps now draw from your extra usage, not your plan limits.")
	if !strings.Contains(msg, "run as claude -p") || strings.Contains(msg, "turn off") {
		t.Fatal(msg)
	}
	if got := thirdPartyRefused("Claude Code", &Request{interactive: true}, 502, "other"); got != "other" {
		t.Fatal(got)
	}
}
