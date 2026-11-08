package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A reply the client keeps as several text blocks — its words split by a
// thinking block, a web search or an attempt Claude Code asked again —
// goes on in its run at the next turn (#5705). The run was kept under its
// reply as one text, the words run together where the client's blocks
// meet ("Looking at it:Here"), and the client sent the same reply back as
// two ("Looking at it: Here"): its conversation was none the run had, the
// log said "no run here has had this conversation", and a new Claude Code
// was told all of it from a transcript, writing ~706K tokens to the cache
// again 56s after the turn before had written them (jerrychoi).
func TestClaudeReplySplitIntoBlocksGoesOnInItsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	ev := func(e string) string { return `echo '{"type":"stream_event","event":` + e + `}'` }
	script := `#!/bin/sh
n=0
while read -r line; do
  n=$((n+1))
  ` + ev(`{"type":"message_start","message":{"id":"m'$n'","model":"claude-opus-5-5","usage":{"input_tokens":1}}}`) + `
  if [ $n = 1 ]; then
    ` + ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) + `
    ` + ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking at it:"}}`) + `
    ` + ev(`{"type":"content_block_stop","index":0}`) + `
    ` + ev(`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}`) + `
    ` + ev(`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"the rest"}}`) + `
    ` + ev(`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig"}}`) + `
    ` + ev(`{"type":"content_block_stop","index":1}`) + `
    ` + ev(`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`) + `
    ` + ev(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Here is the answer."}}`) + `
    ` + ev(`{"type":"content_block_stop","index":2}`) + `
  else
    ` + ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) + `
    ` + ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}`) + `
    ` + ev(`{"type":"content_block_stop","index":0}`) + `
  fi
  ` + ev(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`) + `
  ` + ev(`{"type":"message_stop"}`) + `
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	ask := func(msgs string) json.RawMessage {
		t.Helper()
		body := `{"model":"claude-opus-5-5","max_tokens":100,"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-opus-5-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var res struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &res) != nil || len(res.Content) == 0 {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return res.Content
	}
	first := `{"role":"user","content":"hi"}`
	reply := ask(`[` + first + `]`)
	// the client is told the reply as Anthropic would have: two text
	// blocks with the thinking between them
	var blocks []struct{ Type, Text string }
	json.Unmarshal(reply, &blocks)
	var types []string
	for _, b := range blocks {
		types = append(types, b.Type)
	}
	if strings.Join(types, ",") != "text,thinking,text" {
		t.Fatalf("the reply's blocks: %s", reply)
	}
	// and sends it back as it was told it, with the next turn
	next := ask(`[` + first + `,{"role":"assistant","content":` + string(reply) + `},{"role":"user","content":"and?"}]`)
	var got []struct{ Text string }
	json.Unmarshal(next, &got)
	if len(got) != 1 || !strings.HasSuffix(got[0].Text, " turn 2") {
		t.Fatalf("the next turn went to a new Claude Code: %s\n%s", next, logs.String())
	}
	if strings.Contains(logs.String(), "no run waits for this turn") {
		t.Fatalf("log:\n%s", logs.String())
	}
}
