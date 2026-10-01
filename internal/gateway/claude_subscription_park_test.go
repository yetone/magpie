package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fakeClaudeCalling answers each line with a reply that calls one of the
// client's tools, as Claude Code's stream-json does, and then waits on its
// MCP call as Claude Code does: a line asking for "final" calls
// StructuredOutput, one asking to "hang" says a word and never ends its
// reply, its pid left in hang.pid of the directory returned, any other
// calls read. The call's id carries the process's pid.
func fakeClaudeCalling(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
while read -r line; do
  case "$line" in
    *hang*)
      echo $$ > '` + dir + `/hang.pid'
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"thinking it over"}}}'
      continue ;;
    *final*) name=StructuredOutput ;;
    *) name=read ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_'$$'","name":"mcp__magpie__'$name'"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"answer\":42}"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// alive says a process is still running (or not yet reaped).
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// leftover is how many of pids still run after within, polled.
func leftover(pids []int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for {
		n := 0
		for _, pid := range pids {
			if alive(pid) {
				n++
			}
		}
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// askForCall sends a conversation of its own, offering read and
// StructuredOutput, and returns the pid of the Claude Code its reply's tool
// call came from.
func askForCall(t *testing.T, s *Server, ctx context.Context, what string) int {
	t.Helper()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}},{"name":"StructuredOutput","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":` + strconv.Quote(what) + `}]}`
	rec := httptest.NewRecorder()
	var u Usage
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx)
	if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Errorf("%s: %d %s", what, code, msg)
		return 0
	}
	var res struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	for _, c := range res.Content {
		if c.Type == "tool_use" {
			pid, _ := strconv.Atoi(strings.TrimPrefix(c.ID, "toolu_"))
			return pid
		}
	}
	t.Errorf("%s: no tool call in %s", what, rec.Body)
	return 0
}

// A workflow's subagents each end their turn calling StructuredOutput, a
// tool their client answers itself and whose result it never sends back:
// the Claude Code runs that made those calls end at once, not 30 minutes
// later, one process each (#345). A call to any other tool still waits for
// its result.
func TestClaudeRunEndsAfterStructuredOutput(t *testing.T) {
	fakeClaudeCalling(t)
	grace := finalGrace
	finalGrace = 100 * time.Millisecond
	t.Cleanup(func() { finalGrace = grace })
	s := New()
	t.Cleanup(s.subscription.abortAll)

	const fanOut = 8
	pids := make([]int, fanOut)
	var wg sync.WaitGroup
	for i := range pids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pids[i] = askForCall(t, s, context.Background(), fmt.Sprintf("subagent %d: give the final answer", i))
		}()
	}
	wg.Wait()
	read := askForCall(t, s, context.Background(), "read a file")
	if t.Failed() {
		return
	}
	if n := leftover(pids, 3*time.Second); n != 0 {
		t.Fatalf("%d of %d Claude Code processes left waiting on a StructuredOutput result that never comes", n, fanOut)
	}
	if !alive(read) {
		t.Fatalf("the run waiting on a read result was let go")
	}
	s.subscription.mu.Lock()
	runs := len(s.subscription.runs)
	s.subscription.mu.Unlock()
	if runs != 1 {
		t.Fatalf("%d runs kept, want only the one waiting on read", runs)
	}
}

// Runs whose callers stopped with their tool calls unanswered are let go
// past parkedMost, the longest waiting first; the one that just asked is
// kept.
func TestClaudeParkedRunsAreBounded(t *testing.T) {
	fakeClaudeCalling(t)
	longest := parkLongest
	parkLongest = 0
	t.Cleanup(func() { parkLongest = longest })
	s := New()
	t.Cleanup(s.subscription.abortAll)

	var pids []int
	for i := range parkedMost + 3 {
		pids = append(pids, askForCall(t, s, context.Background(), fmt.Sprintf("subagent %d: read a file", i)))
		if t.Failed() {
			return
		}
	}
	if n := leftover(pids[:3], 3*time.Second); n != 0 {
		t.Fatalf("%d of the longest waiting runs kept past parkedMost", n)
	}
	for _, pid := range pids[3:] {
		if !alive(pid) {
			t.Fatalf("run %d let go though it is within parkedMost", pid)
		}
	}
}

// A client gone before its reply is whole stops its Claude Code at once.
func TestClaudeRunEndsWhenTheClientLeaves(t *testing.T) {
	dir := fakeClaudeCalling(t)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hang on"}]}`
		var u Usage
		s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
	}()
	var pid int
	for deadline := time.Now().Add(3 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(dir, "hang.pid"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("no Claude Code started")
	}
	time.Sleep(200 * time.Millisecond) // its reply under way
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the request went on after its client left")
	}
	if leftover([]int{pid}, 3*time.Second) != 0 {
		t.Fatal("Claude Code still runs after its client left")
	}
}

// A run waiting for its conversation's next turn is kept as long as the
// prompt cache Claude Code writes on a subscription lasts, an hour: let go
// at twenty minutes, a turn after half an hour went to a new run told the
// whole conversation in one message, which wrote all of it to the cache
// again while what the old run wrote was still there to read (#463).
func TestClaudeIdleRunKeptWhileItsCacheLasts(t *testing.T) {
	if claudeCacheTTL < time.Hour || idleLongest < claudeCacheTTL {
		t.Fatalf("an idle run is let go after %s, before the cache it wrote, kept %s, expires", idleLongest, claudeCacheTTL)
	}
}
