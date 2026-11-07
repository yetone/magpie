package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// guardClaude stands in for an interactive Claude Code whose tap puts the
// guard file's safeguards in each request (claudetui's tap.js): it says
// it saw a request to put them in (<file>.live) unless FAKE_NO_TAP is
// set, and logs the file as each of its requests would carry it. A turn
// saying CALL3 calls the client's read tool three times, one call a
// request, before it answers. With FAKE_REFUSE_EFFORT set, a request the
// file asks another effort of is refused as Anthropic refuses an effort
// the model doesn't take. Its log is returned.
func guardClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("the script makes its MCP calls with curl")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	log := filepath.Join(dir, "claude.log")
	script := `#!/bin/sh
echo "args $*" >> ` + log + `
cb=$(cat "$(printf '%s\n' "$@" | grep -o '"claude-mcp-helper","[^"]*"' | head -1 | sed 's/.*","//; s/"$//')")
me=$$
sent() { echo "sent $(cat "$MAGPIE_CLAUDE_GUARD" 2>/dev/null)" >> ` + log + `; }
start() { echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$1'","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'; }
stop() {
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"'$1'"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
}
while read -r line; do
  case "$line" in *control_request*)
    id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue;;
  esac
  printf 'told %s\n' "$line" >> ` + log + `
  if [ -n "$MAGPIE_CLAUDE_GUARD" ] && [ -z "$FAKE_NO_TAP" ]; then : > "$MAGPIE_CLAUDE_GUARD.live"; fi
  if [ -n "$FAKE_REFUSE_EFFORT" ] && grep -q '"effort"' "$MAGPIE_CLAUDE_GUARD" 2>/dev/null; then
    sent
    echo '` + tuiEffortRefusal + `'
    continue
  fi
  case "$line" in *CALL3*) ;; *)
    sent; start 0
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$me'"}}}'
    stop end_turn
    echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
    continue;;
  esac
  for k in 1 2 3; do
    sent; start $k
    echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_G'$me'_'$k'","name":"mcp__magpie__read"}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_stop","index":0}}'
    stop tool_use
    got=$(curl --noproxy '*' -sS -X POST -H 'Content-Type: application/json' --data '{"tool_call_id":"toolu_G'$me'_'$k'","name":"read","arguments":{}}' "$cb" 2>> ` + log + `)
    echo "got $got" >> ` + log + `
  done
  sent; start 4
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$me' done"}}}'
  stop end_turn
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	return log
}

// guardsAt is auto mode's safeguards as Claude Code sends them with tool
// batch n: the turns before (prior_turn_context) grow by one each batch,
// as a real auto-mode tool loop's do, and deny is the rules' deny list.
func guardsAt(n int, deny string) string {
	var prior []string
	for i := 0; i <= n; i++ {
		prior = append(prior, fmt.Sprintf(`{"tool_use_ids":["toolu_01Capture%03d"],"context":{"live_cwd":"/Users/lc/work","platform":"macos"}}`, i))
	}
	g := strings.Replace(reviewAsked, `{"tool_use_ids":["toolu_01Capture001"],"context":{"live_cwd":"/Users/lc/work","platform":"macos"}}`, strings.Join(prior, ","), 1)
	return strings.Replace(g, `"deny":[]`, `"deny":[`+deny+`]`, 1)
}

// guardTurn sends the conversation so far with safeguards guards, and
// returns the reply's tool call's id, "" for a reply in words (text).
func guardTurn(t *testing.T, s *Server, conv, guards string) (id, text string) {
	t.Helper()
	body := `{"model":"claude-sonnet-5","max_tokens":100,"safeguards":` + guards + `,"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + conv + `]}`
	rec := httptest.NewRecorder()
	var u Usage
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Type, ID, Text string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 {
		t.Fatalf("no reply: %s", rec.Body)
	}
	last := res.Content[len(res.Content)-1]
	if last.Type == "tool_use" {
		// the run's call reaches magpie after its reply is told
		eventually(t, "the run's call "+last.ID, func() bool {
			s.subscription.mu.Lock()
			defer s.subscription.mu.Unlock()
			return s.subscription.calls[last.ID] != nil
		})
		return last.ID, ""
	}
	return "", last.Text
}

// guardServer is a gateway the run's MCP calls reach.
func guardServer(t *testing.T) *Server {
	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(s.subscription.abortAll) // before srv.Close: a run's MCP call holds a request open
	t.Setenv("MAGPIE_ADDR", srv.Listener.Addr().String())
	return s
}

// #4804: auto mode's classifier context grows with every tool batch (the
// turns before, prior_turn_context). The interactive run read it only as
// it started, so it either started again for each batch, the whole
// conversation written to the cache again each time, or went on sending
// the classifier the context it started with. Each batch's safeguards now
// reach the run's tap through its guard file, and every request of the
// one run carries the safeguards its batch was sent with.
func TestClaudeTUIToolLoopCarriesEachBatchsSafeguards(t *testing.T) {
	log := guardClaude(t)
	s := guardServer(t)
	conv := rmsg("user", "CALL3 read three files")
	var sent []string
	text := ""
	for n := 0; n <= 3; n++ {
		g := guardsAt(n, "")
		sent = append(sent, claudeExtraBody(json.RawMessage(g), ""))
		var id string
		id, text = guardTurn(t, s, conv, g)
		if n == 3 {
			if id != "" {
				t.Fatalf("a fourth call after three batches")
			}
			break
		}
		if id == "" {
			t.Fatalf("batch %d answered %q, not a call", n, text)
		}
		conv += `,{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}` +
			`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"file ` + fmt.Sprint(n) + `"}]}`
	}
	b, _ := os.ReadFile(log)
	if starts := strings.Count(string(b), "args "); starts != 1 {
		t.Fatalf("the tool loop started %d runs, want one:\n%s", starts, b)
	}
	if !strings.HasSuffix(text, " done") {
		t.Fatalf("the turn's end: %q\n%s", text, b)
	}
	var got []string
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "sent "); ok {
			got = append(got, v)
		}
	}
	if len(got) != len(sent) {
		t.Fatalf("%d requests, want %d:\n%s", len(got), len(sent), b)
	}
	for i := range sent {
		if got[i] != sent[i] {
			t.Fatalf("request %d carried\n%s\nwant its batch's\n%s", i, got[i], sent[i])
		}
	}
}

// A run without the tap (a Claude Code that doesn't load it) can't be
// given a batch's safeguards: one whose rules changed is let go, and the
// log says why, where it said "process expired" alone and the run's end
// read "magpie ended it" (vibbow, #4804).
func TestClaudeTUIToolResultsLogWhyTheRunEnded(t *testing.T) {
	log := guardClaude(t)
	t.Setenv("FAKE_NO_TAP", "1")
	logs := keepLog(t)
	s := guardServer(t)
	conv := rmsg("user", "CALL3 read three files")
	id, text := guardTurn(t, s, conv, guardsAt(0, ""))
	if id == "" {
		t.Fatalf("answered %q, not a call", text)
	}
	conv += `,{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}` +
		`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"file"}]}`
	guardTurn(t, s, conv, guardsAt(1, `"Bash(rm:*)"`))
	want := "tool results process expired: safeguards changed; restart Claude Code's interactive mode"
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), want); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(log)
			t.Fatalf("the log doesn't say why the run ended (%q):\n%s\n%s", want, logs, b)
		}
	}
}
