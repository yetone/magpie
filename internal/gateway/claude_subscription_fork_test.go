package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// ylorn on Discord: a lead Claude Code routed by group/orchestrator to a
// Claude subscription starts sub-agents with run_in_background, and they
// died mid-run with "API Error: 409 the agent's turn is already being
// resumed". Claude Code's fork sub-agent (2.1.294's "Fork started —
// processing in background") starts from its lead's conversation as it
// stands: the lead's reply, all its tool calls, then a result for each of
// them, the same placeholder in every fork, and the fork's directive. Each
// fork's first request so answers the very calls the lead's run waits on,
// by their ids, as the lead's own next request does, and they come at once.
// Only one of them can be the run's next turn; each of the others is a
// conversation of its own from there, and is answered by a run of its own,
// told it whole, never turned away with a 409 Claude Code doesn't retry.
// Each reply is its own conversation's: no request is answered with what
// another one said.
func TestClaudeForksAnsweringTheLeadsCallsEachGetAnAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("the script makes its MCP calls with curl")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-lead", "refreshToken": "r-lead", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"}}), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "lead@example.com", "accountUuid": "u-lead"}}), 0o600)

	// Claude Code: a turn it is told from the start calls Agent three
	// times, then makes the calls through its MCP server one after another,
	// as Claude Code does, and says what their results said; a turn told
	// with results in it says what those said. A user message it is
	// written while its calls are out it says it queued and takes into the
	// turn, as Claude Code 2.1.296 does (#1878): a fork's directive.
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"lead@example.com","subscriptionType":"max"}'; exit 0; fi
cb=$(cat "$(printf '%s\n' "$@" | grep -o '"claude-mcp-helper","[^"]*"' | head -1 | sed 's/.*","//; s/"$//')")
say() {
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"heard:'"$1"'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
}
marks() { grep -o 'directive [0-9]*\|agent launched' | sort -u | tr '\n' ','; }
out=$(mktemp -d "$HOME/queued.XXXXXX")
while read -r line; do
  case "$line" in
    *control_request*) continue ;;
  esac
  if [ -e "$out/calls" ]; then
    printf '%s\n' "$line" >> "$out/queued"
    echo '{"type":"command_lifecycle","command_uuid":"'"$(printf '%s' "$line" | sed 's/.*"uuid":"\([^"]*\)".*/\1/')"'","state":"queued"}'
    continue
  fi
  case "$line" in
    *"agent launched"*|*"Fork started"*) say "$(printf '%s' "$line" | marks)"; continue ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  for k in 1 2 3; do
    echo '{"type":"stream_event","event":{"type":"content_block_start","index":'$k',"content_block":{"type":"tool_use","id":"toolu_L'$$'_'$k'","name":"mcp__magpie__Agent"}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":'$k',"delta":{"type":"input_json_delta","partial_json":"{\"part\":'$k'}"}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_stop","index":'$k'}}'
  done
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  : > "$out/calls"
  (
    got=
    for k in 1 2 3; do
      call='{"tool_call_id":"toolu_L'$$'_'$k'","name":"Agent","arguments":{"part":'$k'}}'
      got="$got $(curl --noproxy '*' -s -X POST -H 'Content-Type: application/json' --data "$call" "$cb")"
    done
    rm -f "$out/calls"
    say "$(printf '%s %s' "$got" "$(cat "$out/queued" 2>/dev/null)" | marks)"
    rm -f "$out/queued"
  ) &
done
wait
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	if err := provider.SaveGroup(provider.Group{ID: "orchestrator", Name: "Orchestrator", Members: []string{"claude/claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(s.subscription.abortAll) // before srv.Close: a run's MCP call holds a request open
	t.Setenv("MAGPIE_ADDR", srv.Listener.Addr().String())

	post := func(messages string) (int, string) {
		res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"group/orchestrator","max_tokens":1000,"stream":true,"tools":[{"name":"Agent","input_schema":{"type":"object","properties":{"part":{"type":"integer"}}}}],"messages":`+messages+`}`))
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// the lead's conversation so far, as long as a lead's gets, which its
	// run is checked against at each turn
	lead := `{"role":"user","content":` + string(mustJSON("lead task: "+strings.Repeat("the codebase as read so far. ", 20000))) + `}`
	code, body := post(`[` + lead + `]`)
	ids := regexp.MustCompile(`toolu_L\d+_\d`).FindAllString(body, -1)
	if code != 200 || len(ids) != 3 {
		t.Fatalf("the lead's turn: %d, calls %v: %.400s", code, ids, body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		s.subscription.mu.Lock()
		waits := s.subscription.calls[ids[0]] != nil
		s.subscription.mu.Unlock()
		if waits {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lead's run never made its first call")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var calls []string
	for k, id := range ids {
		calls = append(calls, fmt.Sprintf(`{"type":"tool_use","id":%q,"name":"Agent","input":{"part":%d}}`, id, k+1))
	}
	reply := `{"role":"assistant","content":[` + strings.Join(calls, ",") + `]}`
	results := func(text func(id string) string, after string) string {
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"content":[{"type":"text","text":%q}]}`, id, text(id)))
		}
		if after != "" {
			parts = append(parts, fmt.Sprintf(`{"type":"text","text":%q}`, after))
		}
		return `{"role":"user","content":[` + strings.Join(parts, ",") + `]}`
	}

	// the lead's next turn, its sub-agents launched, and each fork's first
	// request, all at once
	const forks = 8
	asks := []string{`[` + lead + `,` + reply + `,` + results(func(id string) string {
		return "Async agent launched successfully. agentId: a" + id[len(id)-1:]
	}, "") + `]`}
	want := []string{"heard:agent launched,"}
	for k := 1; k <= forks; k++ {
		asks = append(asks, `[`+lead+`,`+reply+`,`+results(func(string) string {
			return "Fork started — processing in background"
		}, fmt.Sprintf("<fork-boilerplate>directive %d: take part %d</fork-boilerplate>", k, k))+`]`)
		want = append(want, fmt.Sprintf("heard:directive %d,", k))
	}
	ready := make(chan struct{})
	codes, bodies := make([]int, len(asks)), make([]string, len(asks))
	var wg sync.WaitGroup
	for n, msgs := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			codes[n], bodies[n] = post(msgs)
		}()
	}
	close(ready)
	wg.Wait()
	for n := range asks {
		who := "the lead"
		if n > 0 {
			who = fmt.Sprintf("fork %d", n)
		}
		if codes[n] != 200 {
			t.Errorf("%s: %d %.300s", who, codes[n], bodies[n])
			continue
		}
		if !strings.Contains(bodies[n], want[n]) {
			t.Errorf("%s was answered %.600s, want %q", who, bodies[n], want[n])
		}
	}
}

// Aisiverto on X: a Claude Pro account's five hours went from 96% left to
// 1% in a few turns of a 900K-token conversation, and the usage showed two
// requests 100ms apart, one reading 900K from the cache and the other
// writing 877K to it. A fork sub-agent's first request answers the calls
// its lead's run made, after the lead's own next turn did: it found no run
// to take them, and a new Claude Code was told the whole conversation in
// one message, a prefix the cache has never seen. It branches off the
// lead's run's session instead, cut after the reply that made the calls:
// the conversation as Claude Code asked it of Anthropic, cached, and its
// run is told the results and the directive only. The lead's session
// stays the lead's. The cut is a copy of the session's lines as far as
// that reply, a session of its own (bbfatx on X): Claude Code's
// interactive mode doesn't take --resume-session-at, so a branch was -p,
// and read none of the conversation its interactive lead had cached; with
// the interactive mode on, a -p branch is never run.
func TestClaudeForkBranchesOffTheLeadsSession(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) { forkBranches(t, mode) })
	}
}

func forkBranches(t *testing.T, mode string) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("the script makes its MCP calls with curl")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	config := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(config, 0o755)
	os.WriteFile(filepath.Join(config, ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-lead", "refreshToken": "r-lead", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"}}), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "lead@example.com", "accountUuid": "u-lead"}}), 0o600)

	// Claude Code, keeping its session as it does: a turn told from the
	// start calls Agent through its MCP server, the call's block written
	// to the session as a line of its own, and says what the result said,
	// written after it. Resumed, it goes on from the session's last line,
	// or as -p from the one --resume-session-at names, and says which, and
	// whether every line is that session's; the interactive mode takes no
	// --resume-session-at (Claude Code 2.1.316). The interactive mode's
	// line of a call comes after the reply's stream, once the run has
	// parked on it, as the real one's is read from the file (kay20475 on
	// X: told before the park here, the branch was never missed).
	dir, logs := t.TempDir(), t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"lead@example.com","subscriptionType":"max"}'; exit 0; fi
log=`+logs+`/$$
echo "args $*" > $log
echo "mode ${FAKE_TUI:-print}" >> $log
cb=$(cat "$(printf '%s\n' "$@" | grep -o '"claude-mcp-helper","[^"]*"' | head -1 | sed 's/.*","//; s/"$//')")
sid=lead$$; resumed=; at=; fork=; prev=
for a in "$@"; do
  if [ "$prev" = --resume ]; then sid=$a; resumed=1; fi
  if [ "$prev" = --resume-session-at ] && [ -z "$FAKE_TUI" ]; then at=$a; fi
  if [ "$a" = --fork-session ]; then fork=1; fi
  prev=$a
done
proj="$CLAUDE_CONFIG_DIR/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
mkdir -p "$proj"
file="$proj/$sid.jsonl"
if [ -n "$resumed" ]; then
  [ -f "$file" ] || { echo "No conversation found with session ID: $sid" >&2; exit 1; }
  cut=$(tail -n 1 "$file" | sed 's/.*"uuid":"\([^"]*\)".*/\1/')
  if grep -v '"sessionId":"'$sid'"' "$file" | grep -q .; then cut="$cut, lines of another session"; fi
  if [ -n "$at" ]; then cut=$at; fi
  if [ -n "$fork" ]; then sid=fork$$; file="$proj/$sid.jsonl"; fi
fi
: >> "$file"
say() {
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$$'","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'"$1"'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
}
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case "$line" in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  echo "told $line" >> $log
  if [ -n "$resumed" ]; then say "branched at $cut"; continue; fi
  case "$line" in
    *"Fork started"*) say "told it whole"; continue ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$$'","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_L'$$'","name":"mcp__magpie__Agent"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"part\":1}"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_stop","index":0}}'
  echo '{"type":"user","uuid":"ask-'$$'","sessionId":"'$sid'"}' >> "$file"
  echo '{"type":"assistant","uuid":"line-'$$'","sessionId":"'$sid'","message":{"id":"m'$$'","model":"claude-opus-5-5"}}' >> "$file"
  row='{"type":"assistant","uuid":"line-'$$'","message":{"id":"m'$$'","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_L'$$'","name":"mcp__magpie__Agent","input":{"part":1}}]}}'
  [ -n "$FAKE_TUI" ] || echo "$row"
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  (
    call='{"tool_call_id":"toolu_L'$$'","name":"Agent","arguments":{"part":1}}'
    got=$(curl --noproxy '*' -s -X POST -H 'Content-Type: application/json' --data "$call" "$cb")
    # the interactive mode tells a reply's session line after its stream,
    # read from the file, so the run has parked on the call by then: here
    # once its result is handed over, after the park for certain
    [ -z "$FAKE_TUI" ] || echo "$row"
    echo '{"type":"user","uuid":"result-'$$'","sessionId":"'$sid'"}' >> "$file"
    case "$got" in *"agent launched"*) say "heard:agent launched" ;; *) say "heard:something else" ;; esac
  ) &
done
wait
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if mode != "print" {
		// the interactive mode's helper, a stand-in that runs Claude
		// Code as it was asked, saying the run is the interactive mode's
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
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	if err := provider.SaveGroup(provider.Group{ID: "orchestrator", Name: "Orchestrator", Members: []string{"claude/claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(s.subscription.abortAll) // before srv.Close: a run's MCP call holds a request open
	t.Setenv("MAGPIE_ADDR", srv.Listener.Addr().String())

	post := func(messages string) (int, string) {
		res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"group/orchestrator","max_tokens":1000,"stream":true,"tools":[{"name":"Agent","input_schema":{"type":"object","properties":{"part":{"type":"integer"}}}}],"messages":`+messages+`}`))
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	lead := `{"role":"user","content":` + string(mustJSON("lead task: "+strings.Repeat("the codebase as read so far. ", 2000))) + `}`
	code, body := post(`[` + lead + `]`)
	id := regexp.MustCompile(`toolu_L\d+`).FindString(body)
	if code != 200 || id == "" {
		t.Fatalf("the lead's turn: %d: %.400s", code, body)
	}
	pid := strings.TrimPrefix(id, "toolu_L")
	reply := fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Agent","input":{"part":1}}]}`, id)
	result := func(text, after string) string {
		parts := []string{fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"content":[{"type":"text","text":%q}]}`, id, text)}
		if after != "" {
			parts = append(parts, fmt.Sprintf(`{"type":"text","text":%q}`, after))
		}
		return `{"role":"user","content":[` + strings.Join(parts, ",") + `]}`
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		s.subscription.mu.Lock()
		waits := s.subscription.calls[id] != nil
		s.subscription.mu.Unlock()
		if waits {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lead's run never made its call")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// the lead's next turn takes the run, then its fork comes
	if code, body := post(`[` + lead + `,` + reply + `,` + result("Async agent launched successfully. agentId: a1", "") + `]`); code != 200 || !strings.Contains(body, "heard:agent launched") {
		t.Fatalf("the lead's next turn: %d %.400s", code, body)
	}
	code, body = post(`[` + lead + `,` + reply + `,` + result("Fork started — processing in background", "<fork-boilerplate>directive 1: take part 1</fork-boilerplate>") + `]`)
	if code != 200 || !strings.Contains(body, "branched at line-"+pid+`"`) {
		t.Fatalf("the fork was answered %d %.600s, want a run branched off the lead's session at line-%s", code, body, pid)
	}
	entries, _ := os.ReadDir(logs)
	branched := 0
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(logs, e.Name()))
		log := string(b)
		if !strings.Contains(log, "--resume ") {
			continue
		}
		branched++
		if strings.Contains(log, "--resume lead") || strings.Contains(log, "--fork-session") || strings.Contains(log, "--resume-session-at") {
			t.Errorf("the branch's arguments: %.300s", log)
		}
		// the branch is its lead's mode: the prompt its lead's cache has
		want := "mode print"
		if mode != "print" {
			want = "mode interactive"
		}
		if !strings.Contains(log, "\n"+want+"\n") {
			t.Errorf("the branch ran as %.300s, want %q", log, want)
		}
		told := log[strings.Index(log, "\ntold "):]
		if strings.Contains(told, "lead task") || !strings.Contains(told, "Fork started") || !strings.Contains(told, "directive 1") {
			t.Errorf("the branch was told %.600s, want the fork's results and directive only", told)
		}
	}
	if branched != 1 {
		t.Errorf("%d runs branched, want 1", branched)
	}
	if files, _ := filepath.Glob(filepath.Join(config, "projects", "*", "lead"+pid+".jsonl")); len(files) == 0 {
		t.Error("the lead's session file is gone")
	}
}
