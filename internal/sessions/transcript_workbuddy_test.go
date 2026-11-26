package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// listedAgents are the agent ids a Session's Agent names, from its doc
// comment: the agents whose sessions the Sessions page can list.
func listedAgents(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("sessions.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("json:\"agent\"`[^\\n]*magpie agent id: ([^\\n]+)").FindSubmatch(src)
	if m == nil {
		t.Fatal("Session.Agent's list of agent ids is gone")
	}
	var out []string
	for _, a := range strings.Split(string(m[1]), ",") {
		out = append(out, strings.TrimSpace(a))
	}
	return out
}

// noTranscriptYet are agents the Sessions page lists whose conversations
// are not read as a transcript, so "Learn from conversations"
// (memdistill, which reads sessions.TranscriptOf) skips them. An agent
// leaves this list when its reader is written, and may not be added to it
// without one being filed.
var noTranscriptYet = map[string]string{
	"zcode": "database rows",
	"dsh":   "session.v3.jsonl not read as a transcript",
	"cline": "messages.json not read as a transcript",
	"grok":  "updates.jsonl not read as a transcript",
	"droid": "not read as a transcript",
	"alma":  "database rows",
}

// Every agent the Sessions page lists is read by memory learning, or is
// named in noTranscriptYet: a new session reader can't be left out of it
// unseen (WorkBuddy's sessions showed on Sessions for weeks and were
// never learned from, #feedback bxgz233).
func TestEveryListedAgentHasATranscriptOrIsNamed(t *testing.T) {
	for _, a := range listedAgents(t) {
		_, parked := noTranscriptYet[a]
		switch {
		case HasTranscript(a) && parked:
			t.Errorf("%s reads as a transcript: take it off noTranscriptYet", a)
		case !HasTranscript(a) && !parked:
			t.Errorf("%s has a session reader and no transcript: memory learning never reads it", a)
		}
	}
	for a := range noTranscriptYet {
		found := false
		for _, l := range listedAgents(t) {
			found = found || l == a
		}
		if !found {
			t.Errorf("noTranscriptYet names %s, which is not a listed agent", a)
		}
	}
}

// WorkBuddy's, WorkBuddy AI's and CodeBuddy Code's sessions are read as a
// conversation: what was typed (beside WorkBuddy's own reminder and
// @scene blocks), the replies, and each tool call with its result.
func TestTranscriptWorkBuddyFamily(t *testing.T) {
	setupAgents(t)
	n := 0
	for _, s := range List(100) {
		if s.Agent != "workbuddy" && s.Agent != "codebuddy" || s.ParentSession != "" {
			continue
		}
		n++
		if !s.Transcript {
			t.Fatalf("%s session %s is not marked readable", s.Agent, s.ID)
		}
		tr, err := TranscriptOf(s)
		if err != nil || len(tr.Parts) == 0 {
			t.Fatalf("%s %s: %v, %d parts", s.Agent, s.ID, err, len(tr.Parts))
		}
		if strings.HasPrefix(s.ID, "22222222") {
			var typed, reply, call, result bool
			for _, p := range tr.Parts {
				typed = typed || p.Kind == "text" && p.Role == "user" && p.Text == "Draft the   launch notes"
				reply = reply || p.Kind == "text" && p.Role == "assistant" && p.Text == "Reading the notes."
				call = call || p.Kind == "tool_use" && p.Name == "Read"
				result = result || p.Kind == "tool_result" && p.Name == "Read" && p.Text == "notes"
				if strings.HasPrefix(p.Text, "@scene") {
					t.Errorf("WorkBuddy's @scene block read as said: %+v", p)
				}
			}
			if !typed || !reply || !call || !result {
				t.Fatalf("typed %v reply %v call %v result %v: %+v", typed, reply, call, result, tr.Parts)
			}
		}
	}
	if n < 3 {
		t.Fatalf("only %d WorkBuddy-family sessions listed", n)
	}
}

// WorkBuddy writes a turn as one input_text block, its <system-reminder>
// context and then the user's words in <user_query>: the block began with
// "<", so it was read as context whole, a session had nothing the user
// said, and "Learn from conversations" passed every one over (bxgz233 on
// X). The fixture is a real WorkBuddy 2026-10 session's lines, its
// identity files and replies cut short. Its words are read as said, the
// rest as context, and the first is its prompt.
func TestWorkBuddyOneBlockTurnsAreSaid(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	copyTree(t, "testdata/workbuddy-one-block", filepath.Join(home, ".workbuddy"))
	var s Session
	for _, x := range List(100) {
		if x.Agent == "workbuddy" {
			s = x
		}
	}
	if s.ID != "ac189729-58d4-45b8-b973-667a852aa5cc" || !s.Transcript {
		t.Fatalf("WorkBuddy's session not listed readable: %+v", s)
	}
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	context := 0
	for _, p := range tr.Parts {
		if p.Role != "user" {
			continue
		}
		switch p.Kind {
		case "text":
			said = append(said, p.Text)
		case "context":
			context++
			if strings.Contains(p.Text, "user_query") || !strings.HasPrefix(p.Text, "<system-reminder") {
				t.Errorf("context: %.200q", p.Text)
			}
		}
	}
	if !slices.Equal(said, []string{"你好", "你是啥模型"}) || context != 2 {
		t.Fatalf("said %q, %d context", said, context)
	}
	line, err := os.ReadFile(filepath.Join(home, ".workbuddy", "projects", "Users-me-WorkBuddy-2026-10-04-03-38-53", s.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(line), "\n")
	var l wbLine
	if err := json.Unmarshal([]byte(first), &l); err != nil {
		t.Fatal(err)
	}
	if got := wbPrompt(l.Content); got != "你好" {
		t.Fatalf("first prompt %q", got)
	}
}

// A turn the gateway recorded from WorkBuddy holds the same block: its
// words are what was said, and the session's title.
func TestGatewayOneBlockTurnSaid(t *testing.T) {
	setup(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetGatewayRecording(false, true) })
	block := "<system-reminder data-role=\"user-context\">\n<user_info>\nOS Version: darwin\n</user_info>\n</system-reminder>\n<user_query>你是啥模型</user_query>"
	turn := GatewayTurn{Agent: "workbuddy", Session: "wb", Time: time.Now().Add(-time.Hour), Status: 200,
		Input:  []Part{{Role: "system", Kind: "context", Text: "You are WorkBuddy."}, {Role: "user", Kind: "text", Text: block}},
		Output: []Part{{Role: "assistant", Kind: "text", Text: "我是 WorkBuddy 里的 AI 助手。"}}}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, err := GatewayTranscript("workbuddy", "wb")
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, p := range tr.Parts {
		if p.Role == "user" && p.Kind == "text" {
			said = append(said, p.Text)
		}
	}
	if !slices.Equal(said, []string{"你是啥模型"}) {
		t.Fatalf("said %q: %+v", said, tr.Parts)
	}
	if got := GatewayTitle("workbuddy", "wb"); got != "你是啥模型" {
		t.Fatalf("title %q", got)
	}
}
