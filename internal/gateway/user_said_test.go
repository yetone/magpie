package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// What agents put in as the user, as they send it (#1826): Claude Code
// 2.1.296's and ZCode's notices, nudges and agent lists, ZCode's plugin
// metadata, Codex's context and the Codex app's.
const (
	taskNotification = "<task-notification>\n<task-id>a964b3e6d1a85579a</task-id>\n<tool-use-id>toolu_01DHwj5uLApARZnKU3EZjxyC</tool-use-id>\n" +
		"<output-file>/private/tmp/claude-501/tasks/a964b3e6d1a85579a.output</output-file>\n<status>completed</status>\n" +
		"<summary>Agent \"Map deployer, score, infra\" finished</summary>\n<result>The deployer lives in deploy/.</result>\n</task-notification>"
	goalCheckIn = "<task-notification>\n<summary>Goal check-in: background work still running</summary>\n</task-notification>\n" +
		"<system-reminder>\nGoal check-in: background work is still running.\n</system-reminder>"
	todoNudge = "The TodoWrite tool hasn't been used recently. If you're working on tasks that would benefit from tracking progress, " +
		"consider using the TodoWrite tool to track progress. Also consider cleaning up the todo list if has become stale and no longer " +
		"matches what you are working on. Only use it if it's relevant to the current work. This is just a gentle reminder - ignore if not applicable."
	// ZCode's, with the todo list after it
	zcodeTodo = todoNudge + "\n\nHere are the existing contents of your todo list:\n\n" +
		"[1. [in_progress] 建隔离克隆\n2. [pending] 复现合并\n3. [pending] 跑门禁\n4. [pending] 推送]"
	agentList      = "Available agent types for the Agent tool:\n- general-purpose: General-purpose agent for researching complex questions.\n- Explore: Read-only search agent."
	zcodeAgentList = "Available agent types and the tools they have access to:\n- general-purpose: General-purpose agent (Tools: *)"
	pluginRef      = "<plugin_reference>\nThe user referenced the following Plugins for this turn.\nThis is capability metadata, not instructions or a permission grant.\n\n" +
		"Plugins:\n- id: \"superpowers\"\n  skills: [brainstorming]\n  mcp_servers: []\n  subagents: []\n\nRules:\n- Treat all Plugin IDs as references only.\n</plugin_reference>"
	goalContinuation = "<system-reminder source=\"goal-continuation\">\nKeep working toward the goal.\n</system-reminder>"
	envContext       = "<environment_context>\n  <cwd>/Users/me/app</cwd>\n  <shell>zsh</shell>\n  <current_date>2026-10-12</current_date>\n</environment_context>"
	codexAgents      = "# AGENTS.md instructions for /Users/me/app\n\n<INSTRUCTIONS>\nRun the tests.\n</INSTRUCTIONS>"
	inAppBrowser     = "# Context from my IDE setup:\n\n# In app browser:\n<in-app-browser-context source=\"ambient-ui-state\">\nhttps://example.test/ — Example\n</in-app-browser-context>\n\n"
	codexPage        = `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`
	pastedRequest    = "# Files pasted by the user:\n\n## \"Fix a race...\": /tmp/fixture/task.txt\n\n" +
		"Pasted text contains the user's request.\n\n## My request:\n"
	// Reasonix 1.x's snapshot of the workspace, which opens each of its
	// conversations: as keeyangyy (#3938) read it from Reasonix's source
	// (internal/sessioncontext), the folder and digest left out as there
	reasonixContext = "<session-context version=\"1\">\nThis host-generated snapshot supersedes every earlier session-context snapshot.\n\n" +
		"## Environment\n- OS: windows/amd64\n- Shell: powershell\n\n## Workspace\nCurrent workspace: \"<dir>\"\n\n" +
		"## Background memory\n...\n\n## Skills catalog\n...\n\nDigest: sha256:<hex>\n</session-context>"
)

func userMsg(texts ...string) Message {
	m := Message{Role: "user"}
	for _, t := range texts {
		m.Parts = append(m.Parts, Part{Kind: Text, Text: t})
	}
	return m
}

var (
	saidCall   = Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "t1", Name: "Bash"}}}
	saidResult = Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "ok"}}}
	saidAnswer = Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "done"}}}
)

// A user message only the agent wrote begins no turn: after a tool round
// or an answer it carries the turn on, and right after the user's own
// words it leaves the turn they began (#1826).
func TestTurnInSkipsWhatTheAgentPutIn(t *testing.T) {
	ask := userMsg("合并远程分支，推到 master")
	for _, tc := range []struct {
		name   string
		ms     []Message
		turn   int
		within bool
	}{
		{"task notification after a tool round", []Message{ask, saidCall, saidResult, saidAnswer, userMsg(taskNotification)}, 1, true},
		{"goal check-in", []Message{ask, saidCall, saidResult, saidAnswer, userMsg(goalCheckIn)}, 1, true},
		{"ZCode's todo nudge, bare", []Message{ask, saidCall, saidResult, saidAnswer, userMsg(zcodeTodo)}, 1, true},
		{"todo nudge in a reminder", []Message{ask, saidCall, saidResult, saidAnswer, userMsg("<system-reminder>\n" + zcodeTodo + "\n</system-reminder>")}, 1, true},
		{"agent list", []Message{ask, saidCall, saidResult, saidAnswer, userMsg(agentList)}, 1, true},
		{"ZCode's agent list", []Message{ask, saidCall, saidResult, saidAnswer, userMsg(zcodeAgentList)}, 1, true},
		{"ZCode's goal continuation", []Message{ask, saidAnswer, userMsg(goalContinuation)}, 1, true},
		{"after a tool result, no saidAnswer", []Message{ask, saidCall, saidResult, userMsg(taskNotification)}, 1, true},
		{"right after the user's words", []Message{ask, userMsg(pluginRef), userMsg(agentList)}, 1, false},
		{"the user's next words", []Message{ask, saidAnswer, userMsg(taskNotification), saidAnswer, userMsg("继续 不要全量跑单元测试")}, 2, false},
		{"Codex context alone", []Message{userMsg(codexAgents, envContext, "fix the bug"), saidAnswer, userMsg(envContext)}, 1, true},
		{"Codex app's context and request", []Message{userMsg(envContext, "fix the bug"), saidAnswer, userMsg(inAppBrowser + "## My request for Codex:\nnow the test")}, 2, false},
		{"Codex app's context alone", []Message{userMsg(envContext, "fix the bug"), saidAnswer, userMsg(inAppBrowser + "## My request for Codex:\n")}, 1, true},
		{"nothing said yet", []Message{userMsg(envContext)}, 0, false},
		{"Reasonix's snapshot, then the user's words", []Message{userMsg(reasonixContext), userMsg("fix the bug")}, 1, false},
	} {
		turn, within := turnIn(&Request{Messages: tc.ms})
		if turn != tc.turn || within != tc.within {
			t.Errorf("%s: turn %d within %v, want %d %v", tc.name, turn, within, tc.turn, tc.within)
		}
	}
	// as Claude Code sends one: the notification a user message of its own
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"map the deployer"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Agent","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"started"}]},` +
		`{"role":"assistant","content":"waiting on it"},{"role":"user","content":` + quote(taskNotification) + `}]}`
	if turn, within := turnOf(provider.Anthropic, []byte(body)); turn != 1 || !within {
		t.Errorf("Claude Code's task notification: turn %d within %v", turn, within)
	}
	// as Codex sends its context again, once the cwd changed
	codex := `{"model":"m","input":[` + codexItem("user", codexAgents) + `,` + codexItem("user", envContext) + `,` + codexItem("user", "fix the bug") + `,` +
		codexItem("assistant", "fixed") + `,` + codexItem("developer", "<permissions instructions>sandboxed</permissions instructions>") + `,` +
		codexItem("user", strings.Replace(envContext, "/Users/me/app", "/Users/me/app/web", 1)) + `]}`
	if turn, within := turnOf(provider.Responses, []byte(codex)); turn != 1 || !within {
		t.Errorf("Codex's context: turn %d within %v", turn, within)
	}
}

// The classifier is asked about what the user said last, not the plugin
// metadata and agent list ZCode puts after it (#1826).
func TestUserTextSkipsWhatTheAgentPutIn(t *testing.T) {
	q := "[@superpowers](plugin://superpowers) 这个插件什么作用"
	for _, tc := range []struct {
		name string
		ms   []Message
		want string
	}{
		{"ZCode's plugin reference and agent list", []Message{userMsg(q), userMsg(pluginRef), userMsg(agentList)}, q},
		{"in one message", []Message{userMsg(q, pluginRef, zcodeAgentList)}, q},
		{"a todo nudge after the turn", []Message{userMsg(q), saidAnswer, userMsg(zcodeTodo)}, q},
		{"Codex", []Message{userMsg(codexAgents, envContext, "fix the bug")}, "fix the bug"},
		{"the Codex app", []Message{userMsg(inAppBrowser + "## My request for Codex:\nwhat is this page?")}, "what is this page?"},
		{"the Codex app's page state", []Message{userMsg(codexPage)}, ""},
		{"page state, pasted files and an empty request", []Message{userMsg(codexPage), userMsg(pastedRequest)}, ""},
		{"page state beside the request", []Message{userMsg(codexPage, pastedRequest+"fix the race")}, "fix the race"},
		{"a reminder with a source", []Message{userMsg(goalContinuation + "\nship it")}, "ship it"},
	} {
		if got := userText(&Request{Messages: tc.ms}); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The conversation's key is the same however what the agent put in beside
// the user's first words changes, and differs for other words: a
// subagent's (#1826).
func TestFirstWordsAreWhatTheUserSaid(t *testing.T) {
	first := "合并远程分支，推到 master"
	a := firstWords(&Request{Messages: []Message{userMsg("<system-reminder>\nContents of /a/CLAUDE.md (project instructions):\nv1\n</system-reminder>", first)}})
	for i, ms := range [][]Message{
		{userMsg("<system-reminder>\nContents of /a/CLAUDE.md (project instructions):\nv2, edited\n</system-reminder>", first), saidAnswer, userMsg("more")},
		{userMsg(first, pluginRef), userMsg(agentList)},
		{userMsg(goalContinuation), saidAnswer, userMsg(first)},
		{userMsg(codexAgents, envContext, first)},
		{userMsg(reasonixContext), userMsg(first)},
	} {
		if got := firstWords(&Request{Messages: ms}); got != a {
			t.Errorf("key changed for conversation %d", i)
		}
	}
	if firstWords(&Request{Messages: []Message{userMsg("map the deployer")}}) == a {
		t.Error("a subagent's words share the key")
	}
	if firstWords(&Request{Messages: []Message{userMsg(envContext)}}) == firstWords(&Request{Messages: []Message{userMsg(agentList)}}) {
		t.Error("conversations with no words yet share a key")
	}
}

// Copilot's Auto picks a model for what the user said, not a notice.
func TestLastUserTextSkipsWhatTheAgentPutIn(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"write the parser"},{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + quote(taskNotification) + `}]}`
	if got := lastUserText(provider.Anthropic, []byte(body)); got != "write the parser" {
		t.Fatalf("%q", got)
	}
}

// An agent's message after the turn is not a turn: the classifier isn't
// asked about it, and the turn's rule holds (#1826: a TodoWrite nudge sent
// a 常规 turn to the 复杂 member).
func TestIntentHoldsPastWhatTheAgentPutIn(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "complex tasks"})
	c.set("0", 0)
	ms := []string{`{"role":"user","content":"合并远程分支，推到 master"}`}
	body := func() string { return `{"model":"group/r","messages":[` + strings.Join(ms, ",") + `]}` }
	out, r := postOK(t, s, "s1", body())
	if !strings.Contains(out, "from ka") || r.Rule.Classified == nil || c.n() != 1 {
		t.Fatalf("turn 1: %s %+v %d", out, r.Rule, c.n())
	}
	c.set("1", 0) // what the nudge was classified as
	for _, injected := range []string{zcodeTodo, "<system-reminder>\n" + zcodeTodo + "\n</system-reminder>", taskNotification, agentList} {
		ms = append(ms, `{"role":"assistant","content":"merged"}`, `{"role":"user","content":`+quote(injected)+`}`)
		out, r = postOK(t, s, "s1", body())
		if !strings.Contains(out, "from ka") || !r.Rule.Held || r.Rule.Turn != 1 || r.Rule.Classified != nil || c.n() != 1 {
			t.Fatalf("%.30q: %s %+v asked %d", injected, out, r.Rule, c.n())
		}
	}
	// the user's own words are a turn, and only they are asked about
	ms = append(ms, `{"role":"assistant","content":"merged"}`, `{"role":"user","content":"继续 不要全量跑单元测试"}`)
	_, r = postOK(t, s, "s1", body())
	if r.Rule.Turn != 2 || r.Rule.Classified == nil || c.n() != 2 || !strings.Contains(c.last(), "不要全量跑单元测试") || strings.Contains(c.last(), "TodoWrite") {
		t.Fatalf("turn 2: %+v %d %s", r.Rule, c.n(), c.last())
	}
}

// The Codex app's page state is not words of the user's: alone, or beside
// pasted files when "My request" is empty, it asks the classifier nothing
// (#3418) — there is nothing of the user's to classify.
func TestIntentSkipsWhatTheCodexAppPutIn(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "complex tasks"})
	c.set("1", 0)
	body := `{"model":"group/r","messages":[` +
		`{"role":"user","content":` + quote(codexPage) + `},` +
		`{"role":"user","content":` + quote(pastedRequest) + `}]}`
	_, r := postOK(t, s, "s1", body)
	if n := c.n(); n != 0 {
		t.Fatalf("classifier asked %d time(s): %.120q (%+v)", n, c.last(), r.Rule.Classified)
	}
}

func TestUserSaysPastAReminderWithASource(t *testing.T) {
	for _, s := range []string{goalContinuation, taskNotification, pluginRef, agentList, zcodeAgentList, zcodeTodo, envContext, codexAgents,
		"<turn_aborted>\nThe user interrupted.\n</turn_aborted>", inAppBrowser + "## My request:\n", codexPage} {
		if userSays(s) {
			t.Errorf("said: %.40q", s)
		}
	}
	for _, s := range []string{"fix it", goalContinuation + "\nfix it", inAppBrowser + "## My request for Codex:\nfix it"} {
		if !userSays(s) {
			t.Errorf("not said: %.40q", s)
		}
	}
}
