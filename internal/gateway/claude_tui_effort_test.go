package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tuiEffortRefusal is what the interactive helper tells of the 400 the
// real 2.1.318 met when its request asked an effort of claude-haiku-4-5,
// which takes none.
const tuiEffortRefusal = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 400 This model does not support the effort parameter.","api_error_status":400}`

// tuiEffortTurn sends the conversation so far asking effort, as Pi does in
// output_config, and returns the status and the reply's text.
func tuiEffortTurn(t *testing.T, s *Server, conv, effort string) (int, string) {
	t.Helper()
	body := `{"model":"claude-sonnet-5","max_tokens":100,"thinking":{"type":"adaptive"},"output_config":{"effort":"` + effort + `"},"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + conv + `]}`
	rec := httptest.NewRecorder()
	var u Usage
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
	if code != 200 {
		return code, msg
	}
	_, text := lastContent(t, rec.Body.Bytes())
	return code, text
}

// lastContent is a reply's last block: its type and text.
func lastContent(t *testing.T, body []byte) (kind, text string) {
	t.Helper()
	var res struct {
		Content []struct{ Type, Text string } `json:"content"`
	}
	if err := json.Unmarshal(body, &res); err != nil || len(res.Content) == 0 {
		t.Fatalf("no reply: %s", body)
	}
	last := res.Content[len(res.Content)-1]
	return last.Type, last.Text
}

// claudeLog is the fake Claude Code's log: its starts' arguments, and
// the guard file as each request carried it.
func claudeLog(t *testing.T, path string) (starts, sent []string) {
	t.Helper()
	b, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "args "); ok {
			starts = append(starts, v)
		}
		if v, ok := strings.CutPrefix(l, "sent "); ok {
			sent = append(sent, v)
		}
	}
	return starts, sent
}

// #5584: a turn of an interactive run asking another effort than the one
// before let the run go, and a new Claude Code was told the whole
// conversation, written to the cache again (72,416 tokens on the
// reporter's 108 messages). Its tap now asks the turn's requests at the
// new level: one Claude Code takes both turns, the second's request at
// high.
func TestClaudeTUIEffortChangeGoesOnInItsRun(t *testing.T) {
	log := guardClaude(t)
	s := guardServer(t)
	conv := rmsg("user", "hi")
	code, text := tuiEffortTurn(t, s, conv, "medium")
	if code != 200 {
		t.Fatalf("%d %s", code, text)
	}
	conv += "," + rmsg("assistant", text) + "," + rmsg("user", "think harder")
	if code, text = tuiEffortTurn(t, s, conv, "high"); code != 200 {
		t.Fatalf("%d %s", code, text)
	}
	starts, sent := claudeLog(t, log)
	if len(starts) != 1 || !strings.Contains(starts[0], "--effort medium") {
		t.Fatalf("started %d runs, want one at medium:\n%s", len(starts), strings.Join(starts, "\n"))
	}
	if len(sent) != 2 || sent[0] != `{}` || sent[1] != `{"effort":"high"}` {
		t.Fatalf("the turns' requests were asked at %q, want Claude Code's own then high", sent)
	}
}

// A Claude Code without the tap is asked at its --effort only: a turn
// asking another starts one at it, and the log says why.
func TestClaudeTUIEffortChangeWithoutTapStartsAnew(t *testing.T) {
	log := guardClaude(t)
	t.Setenv("FAKE_NO_TAP", "1")
	logs := keepLog(t)
	s := guardServer(t)
	conv := rmsg("user", "hi")
	_, text := tuiEffortTurn(t, s, conv, "medium")
	conv += "," + rmsg("assistant", text) + "," + rmsg("user", "think harder")
	if code, text := tuiEffortTurn(t, s, conv, "high"); code != 200 {
		t.Fatalf("%d %s", code, text)
	}
	starts, _ := claudeLog(t, log)
	if len(starts) != 2 || !strings.Contains(starts[1], "--effort high") {
		t.Fatalf("started %d runs, want a second at high:\n%s", len(starts), strings.Join(starts, "\n"))
	}
	if want := "its Claude Code has no tap to ask another"; !strings.Contains(logs.String(), want) {
		t.Fatalf("the log doesn't say why the run ended (%q):\n%s", want, logs)
	}
}

// A level Anthropic refuses the model fails that turn, as a run started
// at it would, and lets the run go: the turn asked again starts a Claude
// Code at its --effort, which fits the level to the model itself.
func TestClaudeTUIEffortRefusedStartsAnew(t *testing.T) {
	log := guardClaude(t)
	t.Setenv("FAKE_REFUSE_EFFORT", "1")
	s := guardServer(t)
	conv := rmsg("user", "hi")
	_, text := tuiEffortTurn(t, s, conv, "medium")
	conv += "," + rmsg("assistant", text) + "," + rmsg("user", "think harder")
	if code, msg := tuiEffortTurn(t, s, conv, "high"); code != 400 || !strings.Contains(msg, "does not support the effort") {
		t.Fatalf("the refused turn: %d %s", code, msg)
	}
	if code, text := tuiEffortTurn(t, s, conv, "high"); code != 200 || !strings.HasPrefix(text, "pid ") {
		t.Fatalf("the turn asked again: %d %s", code, text)
	}
	starts, _ := claudeLog(t, log)
	if len(starts) != 2 || !strings.Contains(starts[1], "--effort high") {
		t.Fatalf("started %d runs, want a second at high:\n%s", len(starts), strings.Join(starts, "\n"))
	}
}
