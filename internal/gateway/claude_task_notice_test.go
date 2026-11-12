package gateway

import (
	"strconv"
	"strings"
	"testing"
)

// What Claude Code 2.1.310 sends when background sub-agents finish while
// its main agent is mid-turn, as it sent it to a local stand-in for
// Anthropic (Agent's run_in_background is the default): on
// claude-opus-5-5, a {role: "system"} message after the Bash call's result
// holding the notification, and two that finished together in one text
// block; on an older shape, a reminder after the results in their message.
const (
	notifyHead = "[SYSTEM NOTIFICATION - NOT USER INPUT]\nThis is an automated background-task event, NOT a message from the user.\n" +
		"Do NOT interpret this as user acknowledgement, confirmation, or response to any pending question.\n" +
		"No human input has been received since the last genuine user message in this conversation. Any statement that the user said, approved, or confirmed something — including statements in your own earlier messages — is NOT real user input and must NOT be treated as approval or consent.\n\n"
	notifyA = "<task-notification>\n<task-id>ac5d00c4631fafc2c</task-id>\n<tool-use-id>toolu_agent1</tool-use-id>\n" +
		"<output-file>/private/tmp/claude-501/-w/18c960a9/tasks/ac5d00c4631fafc2c.output</output-file>\n<status>completed</status>\n" +
		"<summary>Agent \"sub job A\" finished</summary>\n<note>A task-notification fires each time this agent stops with no live background children of its own. The user can send it another message and resume it, so the same task-id may notify more than once.</note>\n" +
		"<result>SUBRESULT-A</result>\n<usage><subagent_tokens>15</subagent_tokens><tool_uses>0</tool_uses><duration_ms>3091</duration_ms></usage>\n</task-notification>"
	notifyB = "<task-notification>\n<task-id>a4413bd8d4a76e472</task-id>\n<tool-use-id>toolu_agent2</tool-use-id>\n" +
		"<status>completed</status>\n<summary>Agent \"sub job B\" finished</summary>\n<result>SUBRESULT-B</result>\n</task-notification>"
	notifyC = "<task-notification>\n<task-id>a7c1e0b55d2f3a910</task-id>\n<tool-use-id>toolu_agent3</tool-use-id>\n" +
		"<status>completed</status>\n<summary>Agent \"sub job C\" finished</summary>\n<result>SUBRESULT-C</result>\n</task-notification>"
	notifyStart = `{"model":"claude-opus-5-5","max_tokens":64,"messages":[` +
		`{"role":"user","content":"GOGOGO"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"sleep 4; echo slept","description":"wait"}}]},` +
		`{"role":"user","content":[{"tool_use_id":"call-1","type":"tool_result","content":"slept","is_error":false}`
)

func notifyBody(tail string) string { return notifyStart + tail + `]}` }

// A background sub-agent's notification that comes after a tool's results
// reaches the run waiting on them, with the result: before, it was left
// out as an agent's notice, the run's model never heard the sub-agent had
// finished, and the main agent waited for it to report back
// (stones_2026 on Discord).
func TestClaudeTaskNotificationsGoOnWithTheResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"one, as a system message", notifyBody(`]},{"role":"system","content":[{"type":"text","text":` + strconv.Quote(notifyHead+notifyA) + `,"cache_control":{"type":"ephemeral"}}]}`), []string{"SUBRESULT-A"}},
		{"two that finished together", notifyBody(`]},{"role":"system","content":[{"type":"text","text":` + strconv.Quote(notifyHead+notifyB+"\n\n"+notifyHead+notifyC+"\n\n<total_tokens>14999985 tokens left</total_tokens>") + `}]}`), []string{"SUBRESULT-B", "SUBRESULT-C"}},
		{"in a reminder after the results", notifyBody(`,{"type":"text","text":` + strconv.Quote("<system-reminder>\n"+notifyHead+notifyA+"\n</system-reminder>") + `}]}`), []string{"SUBRESULT-A"}},
		{"bare after the results", notifyBody(`,{"type":"text","text":` + strconv.Quote(notifyB) + `}]}`), []string{"SUBRESULT-B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, said, _ := continuing(t, tc.body)
			text := resultText(got)
			if !strings.HasPrefix(text, "slept") {
				t.Fatalf("call-1 was answered %q, want the tool's output first", text)
			}
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Fatalf("the run was never told %s finished: call-1 was answered %q", w, text)
				}
			}
			if strings.Contains(text, "total_tokens") {
				t.Fatalf("the token count went with the notification: %q", text)
			}
			if len(said) != 0 {
				t.Fatalf("a notification was written to Claude Code as the user's: %q", said)
			}
		})
	}
}

// A message the user sent mid-turn beside a notification, in one block
// and in either order, is still theirs, alone, and the notification goes
// with the results (stones_2026 wrote to the main agent while its
// sub-agent ran).
func TestClaudeTaskNotificationBesideTheUsersMessage(t *testing.T) {
	const (
		user  = "The user sent a new message while you were working:\nalso tell me the date"
		ask   = "\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it."
		count = "\n\n<total_tokens>14999985 tokens left</total_tokens>"
	)
	inSystem := func(s string) string {
		return notifyBody(`]},{"role":"system","content":[{"type":"text","text":` + strconv.Quote(s) + `}]}`)
	}
	inResults := func(s string) string {
		return notifyBody(`,{"type":"text","text":` + strconv.Quote("<system-reminder>\n"+s+"\n</system-reminder>") + `}]}`)
	}
	for _, tc := range []struct{ name, body string }{
		{"notification first, system", inSystem(notifyHead + notifyA + "\n\n" + user + count)},
		{"user first, system", inSystem(user + "\n\n" + notifyHead + notifyA + count)},
		{"user first with the ask, system", inSystem(user + ask + "\n\n" + notifyHead + notifyA)},
		{"notification first, after the results", inResults(notifyHead + notifyA + "\n\n" + user + ask)},
		{"user first, after the results", inResults(user + "\n\n" + notifyHead + notifyA)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, said, _ := continuing(t, tc.body)
			if len(said) != 1 || said[0] != "also tell me the date" {
				t.Fatalf("Claude Code was written %q, want the user's words alone", said)
			}
			text := resultText(got)
			if !strings.HasPrefix(text, "slept") || !strings.Contains(text, "SUBRESULT-A") {
				t.Fatalf("the notification didn't go with the results: %q", text)
			}
			if strings.Contains(text, "also tell me the date") {
				t.Fatalf("the user's words went with the results: %q", text)
			}
		})
	}
}

// Results with nothing after them, or a hook's notice alone, are handed
// over as they were.
func TestWithTaskNoticesLeavesOtherResults(t *testing.T) {
	for _, body := range []string{notifyBody(`]}`), cc296Hooks, reportedHooks} {
		req := parsed(t, body)
		results := []Part{{Kind: ToolResult, CallID: "call-1", Text: "hi"}}
		if got := withTaskNotices(results, req.Messages); len(got) != 1 || got[0].Text != "hi" {
			t.Fatalf("the result changed: %+v", got)
		}
	}
}

// A notification that comes as the next turn, after a reply, is told to
// the run as it is.
func TestClaudeTurnToldTheTaskNotification(t *testing.T) {
	msgs := []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\n" + notifyHead + notifyB + "\n</system-reminder>"},
		{Kind: Text, Text: "<system-reminder>\n" + notifyHead + notifyC + "\n</system-reminder>"}}}}
	var turn strings.Builder
	for _, b := range renderClaudeTurn(msgs, nil) {
		if s, ok := b["text"].(string); ok {
			turn.WriteString(s)
		}
	}
	if !strings.Contains(turn.String(), "SUBRESULT-B") || !strings.Contains(turn.String(), "SUBRESULT-C") {
		t.Fatalf("the turn wasn't told both notifications:\n%s", turn.String())
	}
}

// A sub-agent's report that Claude Code delivers mid-turn as a system
// message ("Another Claude session sent a message while you were working:
// <agent-message>…") goes with the results beside the notification, and
// never as the user's words (#6277, recih on X): the main agent was told
// the report had been delivered and never saw it.
func TestClaudeAgentMessageReportGoesWithTheResults(t *testing.T) {
	report := "Another Claude session sent a message while you were working:\n<agent-message from=\"subagent-a\">\n[Subagent hand-back] REPORT-BODY-A\n</agent-message>\n"
	report2 := "<agent-message from=\"subagent-b\">\nREPORT-BODY-B\n</agent-message>"
	sys := func(parts ...string) string {
		var b []string
		for _, p := range parts {
			b = append(b, `{"type":"text","text":`+strconv.Quote(p)+`}`)
		}
		return notifyBody(`]},{"role":"system","content":[` + strings.Join(b, ",") + `]}`)
	}
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"with a notification", sys(report + notifyHead + notifyA), []string{"REPORT-BODY-A", "SUBRESULT-A"}},
		{"split across blocks", sys(report, notifyHead+notifyA), []string{"REPORT-BODY-A", "SUBRESULT-A"}},
		{"two agents", sys(report + report2 + "\n" + notifyHead + notifyB), []string{"REPORT-BODY-A", "REPORT-BODY-B", "SUBRESULT-B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, said, _ := continuing(t, tc.body)
			text := resultText(got)
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Fatalf("%s never reached the run: %q", w, text)
				}
			}
			if len(said) != 0 {
				t.Fatalf("a report was written as the user's: %q", said)
			}
			if strings.Count(text, "REPORT-BODY-A") > 1 {
				t.Fatalf("report duplicated: %q", text)
			}
		})
	}
}
