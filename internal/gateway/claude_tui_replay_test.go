package gateway

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A conversation no run has, asked of the interactive mode, is given to
// its new Claude Code as Claude Code's own transcript, resumed: the
// earlier turns' words and images are in the session's lines, the reply as
// the assistant's, and only the last turn is typed. Told whole, it was
// one first message, pasted: a run lost to an update was refused and every
// try rested an account (#4778), and the earlier turns' 25 images were
// pasted one by one until the paste timed out (#4780).
func TestClaudeTUIReplayGoesInATranscript(t *testing.T) {
	h := tuiScriptHarness(t, `[ -f "$file" ] && cp "$file" "$file.seen"`)
	img := base64.StdEncoding.EncodeToString(pngBytes)
	body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` +
		`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + img + `"}},{"type":"text","text":"what is it"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"x"},{"type":"text","text":"a red square"}]},` +
		`{"role":"user","content":"and its size?"}]}`
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	var run string
	for _, l := range runs {
		run = l
	}
	args, _, _ := strings.Cut(run, "\n")
	_, sid, ok := strings.Cut(args, "--resume ")
	if !ok {
		t.Fatalf("the run wasn't given a transcript to go on from: %s", args)
	}
	sid, _, _ = strings.Cut(sid, " ")
	told := run[strings.Index(run, "\ntold "):]
	if !strings.Contains(told, "and its size?") || strings.Contains(told, "what is it") || strings.Contains(told, "a red square") || strings.Contains(told, `"type":"image"`) || strings.Contains(told, "rules") {
		t.Fatalf("the run was typed more than the last turn:\n%s", told)
	}
	// the session as the run found it
	seen, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", sid+".jsonl.seen"))
	if len(seen) != 1 {
		t.Fatalf("no transcript %s was there as the run started", sid)
	}
	b, err := os.ReadFile(seen[0])
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		Type       string `json:"type"`
		UUID       string `json:"uuid"`
		ParentUUID string `json:"parentUuid"`
		SessionID  string `json:"sessionId"`
		Cwd        string `json:"cwd"`
		Message    struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	var lines []line
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("%v: %s", err, sc.Text())
		}
		lines = append(lines, l)
	}
	if len(lines) != 2 || lines[0].Type != "user" || lines[1].Type != "assistant" || lines[1].ParentUUID != lines[0].UUID || lines[0].SessionID != sid || lines[1].SessionID != sid || lines[0].Cwd == "" {
		t.Fatalf("the transcript:\n%s", b)
	}
	user, _ := json.Marshal(lines[0].Message.Content)
	if !strings.Contains(string(user), `"data":"`+img+`"`) || !strings.Contains(string(user), "what is it") || strings.Contains(string(user), "rules") || strings.Contains(string(user), "cache_control") {
		t.Fatalf("the transcript's user line: %s", user)
	}
	// the caller's instructions are after Claude Code's system prompt, as
	// a first run of the conversation had them (appendsPrompt), not in the
	// transcript's user line
	if !strings.Contains(args, "--append-system-prompt-file") {
		t.Fatalf("the run wasn't given the caller's instructions after Claude Code's prompt: %s", args)
	}
	reply, _ := json.Marshal(lines[1].Message.Content)
	if string(reply) != `[{"text":"a red square","type":"text"}]` {
		t.Fatalf("the transcript's reply: %s", reply)
	}
}

// A conversation with no reply that calls no tool, or with the interactive
// mode turned off, is told whole as before: there is nothing a transcript
// would hold, or -p goes on as it did.
func TestClaudeTUIReplayOnlyWithAReply(t *testing.T) {
	h := tuiScriptHarness(t, "")
	h.ask("rules", "only one")
	for _, l := range h.runs() {
		if strings.Contains(l, "--resume") {
			t.Fatalf("a first turn resumed: %s", l)
		}
	}
	printMode(t, true)
	h.ask("rules B", "b1", "b2")
	for _, l := range h.runs() {
		if strings.Contains(l, "rules B") && strings.Contains(l, "--resume") {
			t.Fatalf("-p resumed a transcript: %s", l)
		}
	}
}

// A transcript's user line is the turns before its reply, all of them
// told as history: the reply answers the last of them, and the turn the
// run is to answer is the one typed after it. Rendered as a first
// message, the user line marked the turn its reply answers as the
// <current_turn>, "the turn to answer now", so a run resumed on
// U0 A0 U1 A1 and typed U2 saw U1, answered already, as the one to
// answer: the model answered an old question again, or said earlier tool
// calls had no results (愛睏柴犬阿茶 on Discord).
func TestClaudeTUIReplayMarksNoEarlierTurnCurrent(t *testing.T) {
	user := func(s string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}} }
	reply := func(s string) Message { return Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: s}}} }
	call := func(id string) Message {
		return Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: id, Name: "read", Args: json.RawMessage(`{}`)}}}
	}
	result := func(id, s string) Message {
		return Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: id, Text: s}}}
	}
	for _, tc := range []struct {
		name  string
		msgs  []Message
		told  []string // in the transcript's user line
		reply string   // its assistant line
		typed int      // messages typed after it
	}{
		{"two turns", []Message{user("U1 first ask"), reply("A1 answer"), user("U2 newest ask")},
			[]string{"U1 first ask"}, "A1 answer", 1},
		{"three turns", []Message{user("U0 first ask"), reply("A0 answer"), user("U1 second ask"), reply("A1 answer"), user("U2 newest ask")},
			[]string{"U0 first ask", "A0 answer", "U1 second ask"}, "A1 answer", 1},
		{"tool results", []Message{
			user("U0 first ask"), call("c0"), result("c0", "R0 result"), reply("A0 answer"),
			user("U1 second ask"), call("c1"), result("c1", "R1 result"), reply("A1 answer"),
			user("U2 newest ask")},
			[]string{"U0 first ask", "R0 result", "A0 answer", "U1 second ask", "R1 result"}, "A1 answer", 1},
		{"tool results only before the reply", []Message{
			user("U1 first ask"), call("c1"), result("c1", "R1 result"), reply("A1 answer"), user("U2 newest ask")},
			[]string{"U1 first ask", "R1 result"}, "A1 answer", 1},
		{"the client's calls after the reply", []Message{
			user("U0 first ask"), reply("A0 answer"), user("U1 second ask"), reply("A1 answer"),
			user("U2 newest ask"), call("c2"), result("c2", "R2 result")},
			[]string{"U0 first ask", "A0 answer", "U1 second ask"}, "A1 answer", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			req := &Request{System: "rules", Tools: []Tool{{Name: "read"}}, Messages: tc.msgs}
			s := replaySession([]string{dir}, dir, "claude-sonnet-5", req)
			if s == nil {
				t.Fatal("no transcript")
			}
			b, err := os.ReadFile(filepath.Join(dir, s.session+".jsonl"))
			s.discard()
			if err != nil {
				t.Fatal(err)
			}
			if len(s.since) != tc.typed {
				t.Fatalf("typed %d messages, want %d", len(s.since), tc.typed)
			}
			if got := s.since[0].Parts[0].Text; got != "U2 newest ask" {
				t.Fatalf("typed %q first, want the newest ask", got)
			}
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) != 2 {
				t.Fatalf("the transcript:\n%s", b)
			}
			var first, second struct {
				Message struct {
					Content []map[string]any `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[1]), &second) != nil {
				t.Fatalf("the transcript:\n%s", b)
			}
			var told strings.Builder
			for _, c := range first.Message.Content {
				s, _ := c["text"].(string)
				told.WriteString(s)
			}
			text := told.String()
			if strings.Contains(text, "current_turn") || strings.Contains(text, "turn to answer now") {
				t.Fatalf("the transcript marks a turn its reply answers as the one to answer now:\n%s", text)
			}
			if open := strings.Index(text, historyOpen); open < 0 || open > strings.Index(text, tc.told[0]) || !strings.HasSuffix(strings.TrimSpace(text), "</conversation_history>") {
				t.Fatalf("the transcript's turns aren't told as history:\n%s", text)
			}
			for _, w := range tc.told {
				if !strings.Contains(text, w) {
					t.Fatalf("the transcript's user line doesn't have %q:\n%s", w, text)
				}
			}
			if strings.Contains(text, "newest ask") || strings.Contains(text, tc.reply) {
				t.Fatalf("the transcript's user line has the reply or the turn after it:\n%s", text)
			}
			if got, _ := second.Message.Content[0]["text"].(string); got != tc.reply {
				t.Fatalf("the transcript's reply is %q, want %q", got, tc.reply)
			}
		})
	}
}
