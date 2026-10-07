package gateway

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// What Claude Code writes as it exits reaches the client: its output is
// read to the end before the run ends, not cut by Wait closing the pipe
// under the reader ("read |0: file already closed", CI on 963a8d8f).
func TestClaudeLastWordsAreRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	// a long reply, more than a pipe holds, written all at once on exit
	script := `#!/bin/sh
read -r line
d='{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + strings.Repeat("x", 1000) + `"}}}'
{
echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}'
i=0; while [ $i -lt 300 ]; do echo "$d"; i=$((i+1)); done
echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the very end"}}}'
echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
echo '{"type":"stream_event","event":{"type":"message_stop"}}'
}
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for i := 0; i < 5; i++ {
		s := New()
		p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"say a lot"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
		s.subscription.abortAll()
		out := rec.Body.String()
		if !strings.Contains(out, "the very end") || strings.Contains(out, "event: error") {
			t.Fatalf("run %d: the reply's end: %q", i, out[max(0, len(out)-400):])
		}
	}
}

// A write to a Claude Code that has exited says how it ended and what it
// said on stderr, not the pipe Wait closed under the write ("write |1:
// file already closed", CI on main, where the run's first prompt failed
// so and nothing told why).
func TestClaudeEndedBeforeItsInputSaysWhy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), "#!/bin/sh\nread -r line\necho 'Not logged in · Please run /login' >&2\nexit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := New()
	t.Cleanup(s.subscription.abortAll)
	req := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	run, events, err := s.subscription.start(context.Background(), req, "claude-sonnet-5", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range events { // until the run ends with its Claude Code
	}
	err = run.setEffort("high")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("a write after Claude Code exited: %v", err)
	}
}
