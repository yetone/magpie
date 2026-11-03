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
