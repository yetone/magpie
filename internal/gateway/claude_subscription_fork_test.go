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
	// with results in it says what those said.
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"lead@example.com","subscriptionType":"max"}'; exit 0; fi
cb=$(printf '%s\n' "$@" | grep -o 'http://[^"]*/_magpie/claude-mcp/[A-Za-z0-9_-]*' | head -1)
say() {
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"heard:'"$1"'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
}
marks() { grep -o 'directive [0-9]*\|agent launched' | sort -u | tr '\n' ','; }
while read -r line; do
  case "$line" in
    *control_request*) continue ;;
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
  got=
  for k in 1 2 3; do
    call='{"tool_call_id":"toolu_L'$$'_'$k'","name":"Agent","arguments":{"part":'$k'}}'
    got="$got $(curl -s -X POST -H 'Content-Type: application/json' --data "$call" "$cb")"
  done
  say "$(printf '%s' "$got" | marks)"
done
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
