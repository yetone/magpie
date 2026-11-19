package sessions

import (
	"os"
	"regexp"
	"strings"
	"testing"
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
	"zcode":  "database rows",
	"dsh":    "session.v3.jsonl not read as a transcript",
	"cline":  "messages.json not read as a transcript",
	"grok":   "updates.jsonl not read as a transcript",
	"droid":  "not read as a transcript",
	"hermes": "database rows",
	"alma":   "database rows",
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
