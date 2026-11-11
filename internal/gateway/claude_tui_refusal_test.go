package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
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
// answers every turn with that refusal, after hold (a shell command);
// as claude -p it answers "ok", or runs print first when it is given.
// (ggbdpq's harness, #4503.)
// tuiThirdPartyText is the refusal's result text the harness prints.
var tuiThirdPartyText = tuiThirdPartyRefusal

// tuiThirdPartyTeamRefusal is pidal20's, a Team workspace's (#5645).
const tuiThirdPartyTeamRefusal = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 400 Third-party apps now draw from extra usage, not plan limits. Ask your workspace admin to add more and keep going.","api_error_status":400}`

func tuiThirdPartyHarness(t *testing.T, hold, print string) (*resumeHarness, string) {
	t.Helper()
	start := `if [ -n "$MG_TEST_TUI" ]; then
  echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
  while read -r line; do
    case $line in *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
      continue ;;
    esac
    ` + hold + `
    echo '` + tuiThirdPartyText + `'
    exit 1
  done
  exit 0
fi
` + print
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

// piSystem is Pi 1.1.0's own system prompt, as the reporter's Pi in Cindy
// sends it, its working directory dir.
func piSystem(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "workdir", "pi-1.1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	var first struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &req); err != nil || len(req.Messages) == 0 {
		t.Fatal(err)
	}
	if err := json.Unmarshal(req.Messages[0], &first); err != nil {
		t.Fatal(err)
	}
	sys := first.Content
	if !strings.Contains(sys, "operating inside pi") || !strings.Contains(sys, "/Users/me/work/proj/My Proj") {
		t.Fatalf("not Pi's prompt: %.200s", sys)
	}
	return strings.ReplaceAll(sys, "/Users/me/work/proj/My Proj", dir)
}

// askThirdParty asks h's Claude subscription a first turn under system,
// on claude-opus-5-5 as the reporter's group/auto did.
func askThirdParty(t *testing.T, h *resumeHarness, system string, stream bool) (int, string, string, Usage) {
	t.Helper()
	sys, _ := json.Marshal(system)
	s := ""
	if stream {
		s = `"stream":true,`
	}
	body := `{"model":"claude-opus-5-5","max_tokens":100,` + s + `"system":` + string(sys) + `,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	var u Usage
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-opus-5-5", []byte(body), &u)
	return code, msg, rec.Body.String(), u
}

// A turn Anthropic refuses in the interactive mode as a third-party app's,
// with nothing of its reply said (#4503; Raven on Discord: Pi in Cindy,
// group/auto to a Claude subscription, every turn refused), is asked again
// once as claude -p, which is served such prompts, and the client has its
// answer: no error, and nothing to turn off in Settings. Before, the
// client had the 400 and was told to turn the interactive mode off for
// every request.
func TestClaudeTUIThirdPartyRefusalRedoneAsPrint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			h, started := tuiThirdPartyHarness(t, "", "")
			code, msg, body, u := askThirdParty(t, h, piSystem(t, "/Users/raven/cindy"), stream)
			if code != 200 || !strings.Contains(body, "ok") {
				t.Fatalf("%d %.300s %.400s", code, msg, body)
			}
			if strings.Contains(body, thirdPartyWords) || u.ErrType != "" {
				t.Fatalf("the client, or its usage row, has the refusal: %q %s", u.ErrType, body)
			}
			if n := helperStarts(started); n != 1 {
				t.Fatalf("the interactive mode started %d times, want once", n)
			}
			if runs := h.runs(); len(runs) != 2 {
				t.Fatalf("%d runs, want two: the interactive mode's, then claude -p", len(runs))
			}
			if claudeTUIOn() == false || settings.Load().ClaudePrintMode {
				t.Fatal("the user's setting changed")
			}
		})
	}
}

// The next turns of the same agent's prompt run as claude -p from the
// start, in another conversation of it too (Pi's working directory comes
// after its own part); another agent's prompt is still tried in the
// interactive mode first.
func TestClaudeTUIThirdPartyPromptRemembered(t *testing.T) {
	h, started := tuiThirdPartyHarness(t, "", "")
	if code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/Users/raven/cindy"), false); code != 200 {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/Users/raven/other"), true); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the interactive mode started %d times, want once: the prompt it refused runs as -p", n)
	}
	if runs := len(h.runs()); runs != 3 {
		t.Fatalf("%d runs, want 3", runs)
	}
	if code, msg, _, _ := askThirdParty(t, h, "You are a careful reviewer.", false); code != 200 {
		t.Fatalf("%d %.300s", code, msg)
	}
	if n := helperStarts(started); n != 2 {
		t.Fatalf("the interactive mode started %d times, want twice: another prompt is tried there first", n)
	}
}

// The redo is once: claude -p too refusing the turn the same way, the
// client has that refusal, saying it ran as -p, and no third run starts.
func TestClaudeTUIThirdPartyRefusalRedoneOnce(t *testing.T) {
	print := `if [ -z "$MG_TEST_TUI" ]; then
  echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
  while read -r line; do
    case $line in *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
      continue ;;
    esac
    echo '` + tuiThirdPartyText + `'
    exit 1
  done
  exit 0
fi
`
	h, started := tuiThirdPartyHarness(t, "", print)
	code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/w"), false)
	if code != 400 || !strings.Contains(msg, thirdPartyWords) || !strings.Contains(body, "run as claude -p") {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the interactive mode started %d times, want once", n)
	}
	if runs := len(h.runs()); runs != 2 {
		t.Fatalf("%d runs, want two: the interactive mode's, then claude -p", runs)
	}
	if retryable(code, []byte(body)) {
		t.Fatalf("the refusal is asked of the next account: %s", body)
	}
	switch f := failure(code, []byte(body)); f {
	case failCredit, failQuota, failRate, failAuth:
		t.Fatalf("the refusal reads as the account's (%s): %s", f, body)
	}
}

// A turn whose reply had begun reaching the client before the refusal is
// not asked again: the client has the reply's start already. Its agent's
// next turn runs as claude -p.
func TestClaudeTUIThirdPartyRefusalAfterStartNotRedone(t *testing.T) {
	heard, every := bridgeHeardAfter, keepaliveEvery
	bridgeHeardAfter, keepaliveEvery = 0, 20*time.Millisecond
	t.Cleanup(func() { bridgeHeardAfter, keepaliveEvery = heard, every })
	h, started := tuiThirdPartyHarness(t, "sleep 1", "")
	code, _, body, _ := askThirdParty(t, h, piSystem(t, "/w"), true)
	if code != 200 || !strings.Contains(body, "message_start") || !strings.Contains(body, thirdPartyWords) {
		t.Fatalf("%d %.400s", code, body)
	}
	if runs := len(h.runs()); runs != 1 {
		t.Fatalf("%d runs, want the interactive one alone", runs)
	}
	if code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/w"), false); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the interactive mode started %d times, want once", n)
	}
}

// A turn that said some of its reply before the refusal is not asked
// again either, whole or streamed: what it said may have been acted on,
// and asked again it would be said and counted twice.
func TestClaudeTUIThirdPartyRefusalAfterWordsNotRedone(t *testing.T) {
	said := `echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}}'`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			h, started := tuiThirdPartyHarness(t, said, "")
			code, _, body, _ := askThirdParty(t, h, piSystem(t, "/w"), stream)
			if !strings.Contains(body, thirdPartyWords) || !stream && code != 400 {
				t.Fatalf("%d %.400s", code, body)
			}
			if runs := len(h.runs()); runs != 1 {
				t.Fatalf("%d runs, want the interactive one alone", runs)
			}
			if code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/w2"), false); code != 200 || !strings.Contains(body, "ok") {
				t.Fatalf("%d %.300s %.300s", code, msg, body)
			}
			if n := helperStarts(started); n != 1 {
				t.Fatalf("the interactive mode started %d times, want once", n)
			}
		})
	}
}

// With the interactive mode turned off, every run is claude -p as before:
// nothing is remembered or asked again.
func TestClaudeThirdPartyRefusalPrintModeAlone(t *testing.T) {
	h, started := tuiThirdPartyHarness(t, "", "")
	printMode(t, true)
	code, msg, body, _ := askThirdParty(t, h, piSystem(t, "/w"), false)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if n := helperStarts(started); n != 0 {
		t.Fatalf("the interactive mode started %d times with it turned off", n)
	}
}

// The same refusal of a claude -p run says so without telling the user to
// turn the interactive mode off, which it already is; one of the
// interactive mode whose reply had begun says the next turns run as -p.
func TestClaudeThirdPartyRefusalOfPrint(t *testing.T) {
	msg := thirdPartyRefused("Claude Code", &Request{Model: "m"}, 400, "API Error: 400 Third-party apps now draw from your extra usage, not your plan limits.")
	if !strings.Contains(msg, "run as claude -p") || strings.Contains(msg, "turn off") {
		t.Fatal(msg)
	}
	if got := thirdPartyRefused("Claude Code", &Request{interactive: true}, 502, "other"); got != "other" {
		t.Fatal(got)
	}
	msg = thirdPartyRefused("Claude Code", &Request{Model: "m", interactive: true}, 400, "API Error: 400 Third-party apps now draw from your extra usage, not your plan limits.")
	if !strings.Contains(msg, "next turns as claude -p") || strings.Contains(msg, "Settings") {
		t.Fatal(msg)
	}
}

// A Team workspace's refusal has no "your" (#5645, pidal20 on X): it is
// redone as claude -p like the Pro/Max one, and a print run's own refusal
// is told with what happened.
func TestClaudeTUIThirdPartyTeamWording(t *testing.T) {
	tuiThirdPartyText = tuiThirdPartyTeamRefusal
	t.Cleanup(func() { tuiThirdPartyText = tuiThirdPartyRefusal })
	h, started := tuiThirdPartyHarness(t, "", "")
	code, msg, body, u := askThirdParty(t, h, piSystem(t, "/w"), false)
	if code != 200 || !strings.Contains(body, "ok") || u.ErrType != "" {
		t.Fatalf("%d %.300s %.400s", code, msg, body)
	}
	if n := helperStarts(started); n != 1 || len(h.runs()) != 2 {
		t.Fatalf("started %d times, %d runs, want 1 and 2", n, len(h.runs()))
	}
	team := "API Error: 400 Third-party apps now draw from extra usage, not plan limits. Ask your workspace admin to add more and keep going."
	if got := thirdPartyRefused("Claude Code", &Request{Model: "m"}, 400, team); !strings.Contains(got, "run as claude -p") {
		t.Fatal(got)
	}
	if got := thirdPartyRefused("Claude Code", &Request{Model: "m", interactive: true}, 400, team); !strings.Contains(got, "next turns as claude -p") {
		t.Fatal(got)
	}
}
