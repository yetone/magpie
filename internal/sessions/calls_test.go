package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/desktopdir"
)

var callT0 = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

// callDirs are the folders a test's calls are read from.
type callDirs struct{ claude, codex, desktop string }

func setupCalls(t *testing.T) callDirs {
	t.Helper()
	dir := t.TempDir()
	d := callDirs{filepath.Join(dir, "claude"), filepath.Join(dir, "codex"), filepath.Join(dir, "desktop")}
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", d.claude)
	t.Setenv("CODEX_HOME", d.codex)

	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	callDesktopDirs = func() []string { return []string{d.desktop} }
	Reset()
	t.Cleanup(func() { callDesktopDirs = desktopDataDirs; Reset() })
	return d
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendText(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// swap changes the first old in a line to new.
func swap(line, old, new string) string { return strings.Replace(line, old, new, 1) }

func stamp(sec int) string {
	return callT0.Add(time.Duration(sec) * time.Second).Format("2006-01-02T15:04:05.000Z")
}

// claudeMsg is one block of a Claude Code reply, sec seconds after callT0.
func claudeMsg(id, model string, in, out, cr, cw, sec int) string {
	return fmt.Sprintf(`{"parentUuid":"p","isSidechain":false,"type":"assistant","uuid":"u-%s-%d","timestamp":"%s","message":{"id":"%s","role":"assistant","model":"%s","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":0},"output_tokens":%d}},"requestId":"req_%s","sessionId":"sess1","cwd":"/work/app","entrypoint":"cli"}`,
		id, sec, stamp(sec), id, model, in, cw, cr, cw, out, id)
}

// claudeSynthetic is a line Claude Code wrote itself, in place of a reply.
func claudeSynthetic(text string, sec int, extra string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"id":"syn-%d","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":%q}],"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}},"cwd":"/work/app","sessionId":"sess1","entrypoint":"cli"%s}`,
		stamp(sec), sec, text, extra)
}

func TestCallsClaude(t *testing.T) {
	d := setupCalls(t)
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		`{"type":"user","timestamp":"`+stamp(0)+`","message":{"role":"user","content":"hello"},"cwd":"/work/app","sessionId":"sess1"}`,
		// a message of three blocks, each repeating its usage; a block of
		// another message comes between, then one more of the first
		claudeMsg("m1", "claude-opus-5-5", 100, 1, 5000, 1000, 1),
		claudeMsg("m1", "claude-opus-5-5", 100, 20, 5000, 1000, 2),
		claudeMsg("m1", "claude-opus-5-5", 100, 50, 5000, 1000, 3),
		claudeMsg("m2", "claude-haiku-4-5-20251001", 7, 8, 0, 0, 4),
		claudeMsg("m1", "claude-opus-5-5", 100, 55, 5000, 1000, 5),
		// nothing spent, and a synthetic line that is no call
		claudeMsg("m3", "claude-opus-5-5", 0, 0, 0, 0, 6),
		claudeSynthetic("No response requested.", 7, ""),
		// API errors, one with no kind named and a long message
		claudeSynthetic("You've hit your session limit · resets 3:40am", 8, `,"isApiErrorMessage":true,"error":"rate_limit","requestId":"req_err"`),
		claudeSynthetic("API Error: "+strings.Repeat("é", 600), 9, `,"isApiErrorMessage":true`),
		// Desktop's Code tab runs Claude Code into the same folder
		swap(swap(claudeMsg("m4", "claude-sonnet-5-5", 1, 2, 3, 4, 10), `"entrypoint":"cli"`, `"entrypoint":"claude-desktop"`), `"sessionId":"sess1"`, `"sessionId":"sess-d"`),
		// lines that say nothing usable
		`{"type":"assistant","timestamp":"soon","message":{"id":"m9","model":"x","usage":{"input_tokens":1}}}`,
		`{"type":"assistant","timestamp":"`+stamp(12)+`","message":{"id":"m8","model":"x"}}`,
		`{"type":"assistant","error":`,
	)
	// a subagent's file: its lines carry the session's id
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1", "subagents", "agent-a1.jsonl"),
		swap(claudeMsg("s1", "claude-haiku-4-5-20251001", 11, 12, 13, 14, 11), `"isSidechain":false`, `"isSidechain":true`))

	cs := Calls(time.Time{})
	want := []Call{
		{Time: callT0.Add(11 * time.Second), Agent: "claude", Session: "sess1", Model: "claude-haiku-4-5-20251001", Tokens: Tokens{11, 12, 13, 14, 0}, RequestID: "req_s1", Cwd: "/work/app"},
		{Time: callT0.Add(10 * time.Second), Agent: "claude-desktop", Session: "sess-d", Model: "claude-sonnet-5-5", Tokens: Tokens{1, 2, 3, 4, 0}, RequestID: "req_m4", Cwd: "/work/app", Millis: 1000},
		{Time: callT0.Add(9 * time.Second), Agent: "claude", Session: "sess1", Error: "unknown", ErrorText: "API Error: " + strings.Repeat("é", 389), Cwd: "/work/app"},
		{Time: callT0.Add(8 * time.Second), Agent: "claude", Session: "sess1", Error: "rate_limit", ErrorText: "You've hit your session limit · resets 3:40am", RequestID: "req_err", Cwd: "/work/app"},
		{Time: callT0.Add(5 * time.Second), Agent: "claude", Session: "sess1", Model: "claude-opus-5-5", Tokens: Tokens{100, 55, 5000, 1000, 0}, RequestID: "req_m1", Cwd: "/work/app", Millis: 5000},
		{Time: callT0.Add(4 * time.Second), Agent: "claude", Session: "sess1", Model: "claude-haiku-4-5-20251001", Tokens: Tokens{7, 8, 0, 0, 0}, RequestID: "req_m2", Cwd: "/work/app", Millis: 1000},
	}
	if len(cs) != len(want) {
		t.Fatalf("want %d calls, got %d: %+v", len(want), len(cs), cs)
	}
	for i := range want {
		if bare(cs[i]) != want[i] {
			t.Errorf("call %d:\n got %+v\nwant %+v", i, cs[i], want[i])
		}
	}
}

func TestCallsCowork(t *testing.T) {
	d := setupCalls(t)
	home := filepath.Join(d.desktop, "local-agent-mode-sessions", "acct", "org", "local_abc", ".claude", "projects", "-sessions-x")
	line := swap(claudeMsg("c1", "claude-sonnet-5-5", 5, 6, 7, 8, 3), `"entrypoint":"cli"`, `"entrypoint":"local-agent"`)
	writeLines(t, filepath.Join(home, "cw1.jsonl"), line)
	writeLines(t, filepath.Join(home, "cw1", "subagents", "agent-z.jsonl"), swap(claudeMsg("c2", "claude-haiku-4-5-20251001", 1, 1, 0, 0, 4), `"sessionId":"sess1"`, `"sessionId":"cw1"`))
	// Cowork's own audit log is no session file
	writeLines(t, filepath.Join(d.desktop, "local-agent-mode-sessions", "acct", "org", "local_abc", "audit.jsonl"), claudeMsg("c3", "claude-sonnet-5-5", 1, 1, 1, 1, 5))
	// and a Claude Code one beside it
	writeLines(t, filepath.Join(d.claude, "projects", "-p", "sess1.jsonl"), claudeMsg("m1", "claude-opus-5-5", 1, 2, 3, 4, 1))

	cs := Calls(time.Time{})
	if len(cs) != 3 {
		t.Fatalf("want 3 calls, got %d: %+v", len(cs), cs)
	}
	if cs[0].Agent != "claude-desktop" || cs[0].Model != "claude-haiku-4-5-20251001" || cs[0].Session != "cw1" ||
		cs[1].Agent != "claude-desktop" || cs[1].Tokens != (Tokens{5, 6, 7, 8, 0}) ||
		cs[2].Agent != "claude" {
		t.Fatalf("calls: %+v", cs)
	}
}

// cxTurnLines are a Codex turn's opening lines.
func cxTurnLines(sec int, turn, model, effort string) []string {
	return []string{
		fmt.Sprintf(`{"timestamp":"%s","type":"event_msg","payload":{"type":"task_started","turn_id":"%s","model_context_window":258400}}`, stamp(sec), turn),
		fmt.Sprintf(`{"timestamp":"%s","type":"turn_context","payload":{"turn_id":"%s","cwd":"/work/it","model":"%s","effort":"%s"}}`, stamp(sec), turn, model, effort),
	}
}

func tokenCountLine(sec int, total, last string) string {
	info := `null`
	if total != "" {
		info = fmt.Sprintf(`{"total_token_usage":%s,"last_token_usage":%s}`, total, last)
	}
	return fmt.Sprintf(`{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":%s,"rate_limits":{}}}`, stamp(sec), info)
}

func cxUse(in, cached, write, out, reasoning int) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}`,
		in, cached, write, out, reasoning, in+out)
}

func cxDoneLine(sec int, turn string, ms, first int) string {
	return fmt.Sprintf(`{"timestamp":"%s","type":"event_msg","payload":{"type":"task_complete","turn_id":"%s","duration_ms":%d,"time_to_first_token_ms":%d}}`, stamp(sec), turn, ms, first)
}

func TestCallsCodex(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	var lines []string
	lines = append(lines, `{"timestamp":"`+stamp(0)+`","type":"session_meta","payload":{"id":"thread-id","session_id":"sess-x","cwd":"/work/start","base_instructions":{"text":"long"}}}`)
	// a turn with one call, told three times over: the same total again and a null info add nothing
	lines = append(lines, cxTurnLines(1, "t1", "gpt-6-astra", "high")...)
	lines = append(lines,
		tokenCountLine(2, cxUse(1000, 600, 0, 50, 20), cxUse(1000, 600, 0, 50, 20)),
		tokenCountLine(3, cxUse(1000, 600, 0, 50, 20), cxUse(1000, 600, 0, 50, 20)),
		tokenCountLine(4, "", ""),
		cxDoneLine(5, "t1", 5000, 700),
	)
	// a turn with two calls, on another model: no figures of its own from Codex, only
	// what the file's times tell of each (a second from what asked for it)
	lines = append(lines, cxTurnLines(10, "t2", "gpt-6-luna", "low")...)
	lines = append(lines,
		tokenCountLine(11, cxUse(3000, 1600, 0, 150, 60), cxUse(2000, 1000, 0, 100, 40)),
		tokenCountLine(12, cxUse(5500, 3600, 300, 300, 100), cxUse(2500, 2000, 300, 150, 40)),
		cxDoneLine(13, "t2", 3000, 400),
	)
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "20", name), lines...)
	// a rollout that goes on from an earlier one: its first count has a
	// total running on, so the last call's is the call; no session_id, so the id
	name2 := "rollout-2026-09-21T10-00-00-0190bbbb-1111-7222-8333-444455556666.jsonl"
	lines = []string{`{"timestamp":"` + stamp(100) + `","type":"session_meta","payload":{"id":"thread-2","cwd":"/work/two"}}`}
	lines = append(lines, cxTurnLines(101, "u1", "gpt-6-astra", "medium")...)
	lines = append(lines,
		tokenCountLine(102, cxUse(90000, 80000, 0, 900, 0), cxUse(4000, 3900, 0, 100, 30)),
		// a total that started over
		tokenCountLine(103, cxUse(500, 0, 0, 20, 5), cxUse(500, 0, 0, 20, 5)),
	)
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "21", name2), lines...)
	// and one with no session_meta at all: the thread id in its name
	name3 := "rollout-2026-09-22T10-00-00-0190cccc-1111-7222-8333-444455556666_2.jsonl"
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "22", name3),
		tokenCountLine(200, cxUse(10, 4, 0, 3, 0), cxUse(10, 4, 0, 3, 0)))

	cs := Calls(time.Time{})
	want := []Call{
		{Time: callT0.Add(200 * time.Second), Agent: "codex", Session: "0190cccc-1111-7222-8333-444455556666", Tokens: Tokens{6, 3, 4, 0, 0}},
		{Time: callT0.Add(103 * time.Second), Agent: "codex", Session: "thread-2", Model: "gpt-6-astra", Tokens: Tokens{500, 20, 0, 0, 0}, Reasoning: 5, Effort: "medium", Cwd: "/work/it", Millis: 1000},
		{Time: callT0.Add(102 * time.Second), Agent: "codex", Session: "thread-2", Model: "gpt-6-astra", Tokens: Tokens{100, 100, 3900, 0, 0}, Reasoning: 30, Effort: "medium", Cwd: "/work/it", Millis: 1000},
		// input_tokens holds the cache write as well as the read (#589): 2500 - 2000 - 300
		{Time: callT0.Add(12 * time.Second), Agent: "codex", Session: "sess-x", Model: "gpt-6-luna", Tokens: Tokens{200, 150, 2000, 300, 0}, Reasoning: 40, Effort: "low", Cwd: "/work/it", Millis: 1000},
		{Time: callT0.Add(11 * time.Second), Agent: "codex", Session: "sess-x", Model: "gpt-6-luna", Tokens: Tokens{1000, 100, 1000, 0, 0}, Reasoning: 40, Effort: "low", Cwd: "/work/it", Millis: 1000},
		{Time: callT0.Add(2 * time.Second), Agent: "codex", Session: "sess-x", Model: "gpt-6-astra", Tokens: Tokens{400, 50, 600, 0, 0}, Reasoning: 20, Effort: "high", Cwd: "/work/it", TTFT: 700, Millis: 5000},
	}
	if len(cs) != len(want) {
		t.Fatalf("want %d calls, got %d: %+v", len(want), len(cs), cs)
	}
	for i := range want {
		if bare(cs[i]) != want[i] {
			t.Errorf("call %d:\n got %+v\nwant %+v", i, cs[i], want[i])
		}
	}
}

func TestCallsSince(t *testing.T) {
	d := setupCalls(t)
	old := filepath.Join(d.claude, "projects", "-p", "old.jsonl")
	writeLines(t, old, swap(claudeMsg("a", "claude-opus-5-5", 1, 1, 0, 0, 0), `"sessionId":"sess1"`, `"sessionId":"old"`))
	recent := filepath.Join(d.claude, "projects", "-p", "recent.jsonl")
	writeLines(t, recent,
		swap(claudeMsg("b", "claude-opus-5-5", 2, 2, 0, 0, 100), `"sessionId":"sess1"`, `"sessionId":"recent"`),
		swap(claudeMsg("c", "claude-opus-5-5", 3, 3, 0, 0, 200), `"sessionId":"sess1"`, `"sessionId":"recent"`))
	// the files were last written when their last call was
	os.Chtimes(old, callT0, callT0)
	os.Chtimes(recent, callT0.Add(200*time.Second), callT0.Add(200*time.Second))

	// a file written before since is not even read
	cs := Calls(callT0.Add(150 * time.Second))
	if len(cs) != 1 || cs[0].Tokens.Input != 3 {
		t.Fatalf("since 150s: %+v", cs)
	}
	if callCache[old] != nil {
		t.Fatal("a file written before since was read")
	}
	// a file written after it has its calls before it left out
	if cs := Calls(callT0.Add(100 * time.Second)); len(cs) != 2 || cs[0].Tokens.Input != 3 || cs[1].Tokens.Input != 2 {
		t.Fatalf("since 100s: %+v", cs)
	}
	if cs := Calls(callT0.Add(101 * time.Second)); len(cs) != 1 {
		t.Fatalf("since 101s: %+v", cs)
	}
	// an older since afterwards reads the file left out, and all is right
	if cs := Calls(time.Time{}); len(cs) != 3 || cs[2].Session != "old" {
		t.Fatalf("all: %+v", cs)
	}
	if cs := Calls(callT0.Add(time.Hour)); len(cs) != 0 {
		t.Fatalf("since the future: %+v", cs)
	}
}

func TestCallsIncremental(t *testing.T) {
	d := setupCalls(t)
	cc := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, cc,
		claudeMsg("m1", "claude-opus-5-5", 1, 5, 0, 0, 1),
		claudeMsg("m2", "claude-opus-5-5", 2, 5, 0, 0, 2))
	rollout := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "low"), tokenCountLine(1, cxUse(100, 50, 0, 10, 0), cxUse(100, 50, 0, 10, 0)))
	writeLines(t, rollout, lines...)

	if cs := Calls(time.Time{}); len(cs) != 3 {
		t.Fatalf("first read: %+v", cs)
	}
	st := callCache[cc]
	if st == nil || st.Off == 0 {
		t.Fatal("the file's read was not kept")
	}

	// a repeat with nothing written reads nothing
	if cs := Calls(time.Time{}); len(cs) != 3 || callCache[cc] != st {
		t.Fatalf("repeat: %+v", cs)
	}

	// more of m2, a message begun later, a line half written; and Codex's
	// same total told again, and a new call
	appendText(t, cc, claudeMsg("m2", "claude-opus-5-5", 2, 9, 0, 0, 3)+"\n"+claudeMsg("m3", "claude-opus-5-5", 3, 5, 0, 0, 4)+"\n"+claudeMsg("m4", "claude-opus-5-5", 4, 5, 0, 0, 5)[:80])
	appendText(t, rollout, tokenCountLine(2, cxUse(100, 50, 0, 10, 0), cxUse(100, 50, 0, 10, 0))+"\n"+tokenCountLine(3, cxUse(300, 150, 0, 40, 0), cxUse(200, 100, 0, 30, 0))+"\n")
	cs := Calls(time.Time{})
	if len(cs) != 5 {
		t.Fatalf("after appending: %+v", cs)
	}
	if callCache[cc] == st {
		t.Fatal("a file that grew did not publish a new snapshot")
	}
	var claude, codex []Call
	for _, c := range cs {
		if c.Agent == "codex" {
			codex = append(codex, c)
		} else {
			claude = append(claude, c)
		}
	}
	if len(claude) != 3 || claude[0].Tokens.Input != 3 || claude[1].Tokens != (Tokens{Input: 2, Output: 9}) || claude[2].Tokens.Input != 1 {
		t.Fatalf("claude: %+v", claude)
	}
	if len(codex) != 2 || codex[0].Tokens != (Tokens{100, 30, 100, 0, 0}) || codex[1].Tokens != (Tokens{50, 10, 50, 0, 0}) {
		t.Fatalf("codex: %+v", codex)
	}

	// the half line finished: counted, once
	appendText(t, cc, claudeMsg("m4", "claude-opus-5-5", 4, 5, 0, 0, 5)[80:]+"\n")
	if cs := Calls(time.Time{}); len(cs) != 6 || cs[0].Tokens.Input != 4 {
		t.Fatalf("after the line was finished: %+v", cs)
	}

	// a file that shrank is read again from the start
	writeLines(t, cc, claudeMsg("n1", "claude-haiku-4-5-20251001", 9, 9, 0, 0, 7))
	cs = Calls(time.Time{})
	if got := callsOf(cs, "claude"); len(got) != 1 || got[0].Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("after shrinking: %+v", got)
	}
	// and one replaced by a longer one of another start
	writeLines(t, cc,
		swap(claudeMsg("p1", "claude-opus-5-5", 8, 8, 0, 0, 8), `"parentUuid":"p"`, `"parentUuid":"other-start"`),
		claudeMsg("p2", "claude-opus-5-5", 7, 7, 0, 0, 9),
		claudeMsg("p3", "claude-opus-5-5", 6, 6, 0, 0, 10),
		claudeMsg("p4", "claude-opus-5-5", 5, 5, 0, 0, 11))
	cs = Calls(time.Time{})
	if got := callsOf(cs, "claude"); len(got) != 4 || got[0].Tokens.Input != 5 || got[3].Tokens.Input != 8 {
		t.Fatalf("after replacing: %+v", got)
	}
	if len(cs) != 6 {
		t.Fatalf("Codex's calls went with it: %+v", cs)
	}

	// a file gone takes its calls
	os.Remove(rollout)
	if cs := Calls(time.Time{}); len(cs) != 4 || len(callCache) != 1 {
		t.Fatalf("after removing: %+v", cs)
	}
}

func callsOf(cs []Call, agent string) []Call {
	var out []Call
	for _, c := range cs {
		if c.Agent == agent {
			out = append(out, c)
		}
	}
	return out
}

func TestCallsNewestFirst(t *testing.T) {
	d := setupCalls(t)
	writeLines(t, filepath.Join(d.claude, "projects", "-p", "a.jsonl"),
		claudeMsg("a1", "claude-opus-5-5", 1, 1, 0, 0, 5),
		claudeMsg("a2", "claude-opus-5-5", 2, 1, 0, 0, 1))
	writeLines(t, filepath.Join(d.claude, "projects", "-p", "b.jsonl"),
		claudeMsg("b1", "claude-opus-5-5", 3, 1, 0, 0, 3),
		claudeMsg("b2", "claude-opus-5-5", 4, 1, 0, 0, 7))
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"),
		tokenCountLine(4, cxUse(10, 0, 0, 1, 0), cxUse(10, 0, 0, 1, 0)))
	var got []int
	for _, c := range Calls(time.Time{}) {
		got = append(got, int(c.Time.Sub(callT0)/time.Second))
	}
	if fmt.Sprint(got) != "[7 5 4 3 1]" {
		t.Fatalf("order: %v", got)
	}
	// calls of one instant come in the same order every time
	writeLines(t, filepath.Join(d.claude, "projects", "-p", "c.jsonl"),
		claudeMsg("c1", "claude-opus-5-5", 20, 1, 0, 0, 5),
		claudeMsg("c2", "claude-opus-5-5", 21, 1, 0, 0, 5))
	first := Calls(time.Time{})
	Reset()
	again := Calls(time.Time{})
	if fmt.Sprint(first) != fmt.Sprint(again) {
		t.Fatalf("order changed:\n%v\n%v", first, again)
	}
	if first[1].Tokens.Input != 21 || first[2].Tokens.Input != 20 || first[3].Tokens.Input != 1 {
		t.Fatalf("ties: the later line first: %+v", first)
	}
}

func TestCallsCodexTurnFinishedLater(t *testing.T) {
	d := setupCalls(t)
	rollout := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, rollout, append(cxTurnLines(0, "t1", "gpt-6-astra", "low"), tokenCountLine(1, cxUse(100, 50, 0, 10, 0), cxUse(100, 50, 0, 10, 0)))...)
	// while the turn is open the call has no first-token time, and only the time the file's
	// stamps tell: a second, from the turn's start to the call's end
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].TTFT != 0 || cs[0].Millis != 1000 {
		t.Fatalf("turn still open: %+v", cs)
	}
	// the turn's end comes in a later read: its figures, which are exact, reach the call kept
	// from the one before, over the estimate
	appendText(t, rollout, cxDoneLine(2, "t1", 1500, 300)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].TTFT != 300 || cs[0].Millis != 1500 {
		t.Fatalf("turn finished: %+v", cs)
	}
}

func TestCallsResumed(t *testing.T) {
	d := setupCalls(t)
	a := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, a, claudeMsg("m1", "claude-opus-5-5", 1, 1, 0, 0, 1), claudeMsg("m2", "claude-opus-5-5", 2, 2, 0, 0, 2))
	// picked up again: a file of its own, the history copied in as its own session's, and a call more
	b := filepath.Join(d.claude, "projects", "-p", "sessB.jsonl")
	own := func(line string) string { return swap(line, `"sessionId":"sess1"`, `"sessionId":"sessB"`) }
	writeLines(t, b, own(claudeMsg("m1", "claude-opus-5-5", 1, 1, 0, 0, 1)), own(claudeMsg("m2", "claude-opus-5-5", 2, 2, 0, 0, 2)),
		own(claudeMsg("m3", "claude-opus-5-5", 3, 3, 0, 0, 3700)))
	os.Chtimes(a, callT0.Add(2*time.Second), callT0.Add(2*time.Second))
	os.Chtimes(b, callT0.Add(3700*time.Second), callT0.Add(3700*time.Second))

	cs := Calls(time.Time{})
	if len(cs) != 3 || cs[0].Session != "sessB" || cs[1].Session != "sess1" || cs[2].Session != "sess1" || cs[1].Tokens.Input != 2 {
		t.Fatalf("each call once, the copied ones as of the first file: %+v", cs)
	}
	if cs := Calls(callT0.Add(30 * time.Minute)); len(cs) != 1 || cs[0].Tokens.Input != 3 {
		t.Fatalf("since half an hour: %+v", cs)
	}
	// the copy is left out again after the file grows
	appendText(t, b, own(claudeMsg("m4", "claude-opus-5-5", 4, 4, 0, 0, 3800))+"\n")
	if cs := Calls(time.Time{}); len(cs) != 4 || cs[0].Tokens.Input != 4 {
		t.Fatalf("after growing: %+v", cs)
	}
}

func TestDesktopDataDirs(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("USERPROFILE", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "Local"))
	t.Setenv("APPDATA", filepath.Join(dir, "Roaming"))
	ds := desktopDataDirs()
	// on Windows Desktop's own data is %APPDATA%\Claude, beside
	// %LOCALAPPDATA%'s Claude and Claude-3p
	want := []string{"Claude", "Claude-3p"}
	if runtime.GOOS == "windows" {
		want = []string{filepath.Join("Roaming", "Claude"), filepath.Join("Local", "Claude"), filepath.Join("Local", "Claude-3p")}
	}
	if len(ds) != len(want) {
		t.Fatalf("dirs: %v", ds)
	}
	for i, w := range want {
		if !strings.HasSuffix(ds[i], string(filepath.Separator)+w) {
			t.Fatalf("dirs: %v, want %v", ds, want)
		}
	}
}

// Claude Code's thinking effort is on the lines of its replies: the
// session's, or the turn's own when it asked for another.
func TestCallsClaudeEffort(t *testing.T) {
	d := setupCalls(t)
	with := func(line, extra string) string { return strings.TrimSuffix(line, "}") + "," + extra + "}" }
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		claudeMsg("m1", "claude-opus-5-5", 1, 1, 1, 1, 1),
		with(claudeMsg("m2", "claude-opus-5-5", 1, 1, 1, 1, 2), `"effort":"xhigh","perTurnEffort":null`),
		with(claudeMsg("m3", "claude-opus-5-5", 1, 1, 1, 1, 3), `"effort":"xhigh","perTurnEffort":"max"`),
		with(claudeMsg("m4", "claude-opus-5-5", 1, 1, 1, 1, 4), `"effort":"high"`),
	)
	var got []string
	for _, c := range Calls(time.Time{}) {
		got = append(got, c.RequestID+"="+c.Effort)
	}
	if want := "req_m4=high req_m3=max req_m2=xhigh req_m1="; strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

// Claude Code notes the model it runs as when a session starts and when it is
// changed: each call has the last note before it as the model asked for, as
// named (claude-opus-5[1m]), and the model the API answered as its own.
func TestCallsClaudeRequested(t *testing.T) {
	d := setupCalls(t)
	note := func(sec int, model string) string {
		return fmt.Sprintf(`{"type":"attachment","timestamp":"%s","attachment":{"type":"identity","identity":{"modelId":"%s","marketingName":"x"},"text":"t"},"sessionId":"sess1"}`, stamp(sec), model)
	}
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		claudeMsg("m0", "claude-sonnet-5-5", 1, 1, 0, 0, 0), // before any note: not known
		note(1, "claude-opus-5[1m]"),
		claudeMsg("m1", "claude-opus-5", 1, 1, 0, 0, 2),
		note(3, "claude-sonnet-5-5"), // changed
		claudeMsg("m2", "claude-sonnet-5-5", 1, 1, 0, 0, 4),
		claudeMsg("m3", "claude-sonnet-5-5", 1, 1, 0, 0, 5),
	)
	var got []string
	for _, c := range Calls(time.Time{}) {
		got = append(got, c.RequestID+"="+c.Requested+"→"+c.Model)
	}
	if want := "req_m3=claude-sonnet-5-5→claude-sonnet-5-5 req_m2=claude-sonnet-5-5→claude-sonnet-5-5 req_m1=claude-opus-5[1m]→claude-opus-5 req_m0=→claude-sonnet-5-5"; strings.Join(got, " ") != want {
		t.Fatalf("got %v\nwant %s", got, want)
	}
}

// A Claude call took from the user line that asked for it (a prompt, a tool's
// result) to its last line; without one since the last reply, from that
// reply's end; a message of several blocks is timed to its last.
func TestCallsClaudeTook(t *testing.T) {
	d := setupCalls(t)
	user := func(sec int) string {
		return fmt.Sprintf(`{"type":"user","timestamp":"%s","message":{"role":"user","content":"hi"},"sessionId":"sess1"}`, stamp(sec))
	}
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		user(10),
		claudeMsg("a", "claude-opus-5-5", 1, 1, 0, 0, 12), // 2 s after the prompt
		claudeMsg("a", "claude-opus-5-5", 1, 1, 0, 0, 14), // its last block: 4 s
		user(30), // the tool's result, after a long run
		claudeMsg("b", "claude-opus-5-5", 1, 1, 0, 0, 33), // 3 s after it, not 19 after the reply before
		claudeMsg("c", "claude-opus-5-5", 1, 1, 0, 0, 40), // no user line since: from b's end, 7 s
	)
	got := map[string]int64{}
	for _, c := range Calls(time.Time{}) {
		got[c.RequestID] = c.Millis
	}
	if got["req_a"] != 4000 || got["req_b"] != 3000 || got["req_c"] != 7000 {
		t.Fatalf("took: %v", got)
	}
}

// A Codex call in a turn of several took from what it was given — the prompt or
// a tool's output — or the call before it, whichever came later.
func TestCallsCodexTook(t *testing.T) {
	d := setupCalls(t)
	rollout := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	out := func(sec int) string {
		return fmt.Sprintf(`{"timestamp":"%s","type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":"ok"}}`, stamp(sec))
	}
	lines := cxTurnLines(0, "t1", "gpt-6-astra", "low")
	lines = append(lines,
		tokenCountLine(3, cxUse(1000, 0, 0, 50, 0), cxUse(1000, 0, 0, 50, 0)), // 3 s from the turn's start
		out(20), // a tool ran for a long time
		tokenCountLine(24, cxUse(2500, 1000, 0, 90, 0), cxUse(1500, 1000, 0, 40, 0)),  // 4 s from its output, not 21 from the call
		tokenCountLine(30, cxUse(4000, 2500, 0, 130, 0), cxUse(1500, 1500, 0, 40, 0)), // no input since: from the call before, 6 s
		cxDoneLine(31, "t1", 30000, 900),
	)
	writeLines(t, rollout, lines...)
	var took []int64
	for _, c := range Calls(time.Time{}) {
		took = append(took, c.Millis)
	}
	// the newest first; a turn of three calls has no figures of its own
	if fmt.Sprint(took) != "[6000 4000 3000]" {
		t.Fatalf("took %v", took)
	}
}

// bare is a call without where it is in its file, which a test of what the
// calls are has no want of.
func bare(c Call) Call {
	c.File, c.From, c.To, c.Msg = "", 0, 0, ""
	return c
}

// The agents of a Claude Code workflow (ultracode) write their transcripts
// a folder deeper, in <session>/subagents/workflows/<run>/, beside the run's
// journal.jsonl; their calls are the session's (ksinverse on X: Claude's
// usage read low with ultracode on, Codex's right).
func TestCallsWorkflowAgents(t *testing.T) {
	d := setupCalls(t)
	proj := filepath.Join(d.claude, "projects", "-work-app")
	writeLines(t, filepath.Join(proj, "sess1.jsonl"), claudeMsg("m1", "claude-opus-5-5", 1, 2, 3, 4, 1))
	run := filepath.Join(proj, "sess1", "subagents", "workflows", "wf_a1b2c3")
	writeLines(t, filepath.Join(run, "agent-w1.jsonl"),
		swap(claudeMsg("w1", "claude-sonnet-5-5", 5, 6, 7, 8, 2), `"isSidechain":false`, `"isSidechain":true`))
	// the run's journal isn't a transcript, even with a line that reads as one
	writeLines(t, filepath.Join(run, "journal.jsonl"), claudeMsg("j1", "claude-sonnet-5-5", 100, 100, 0, 0, 3))

	cs := Calls(time.Time{})
	want := []Call{
		{Time: callT0.Add(2 * time.Second), Agent: "claude", Session: "sess1", Model: "claude-sonnet-5-5", Tokens: Tokens{5, 6, 7, 8, 0}, RequestID: "req_w1", Cwd: "/work/app"},
		{Time: callT0.Add(1 * time.Second), Agent: "claude", Session: "sess1", Model: "claude-opus-5-5", Tokens: Tokens{1, 2, 3, 4, 0}, RequestID: "req_m1", Cwd: "/work/app"},
	}
	if len(cs) != len(want) {
		t.Fatalf("want %d calls, got %d: %+v", len(want), len(cs), cs)
	}
	for i := range want {
		if bare(cs[i]) != want[i] {
			t.Errorf("call %d:\n got %+v\nwant %+v", i, bare(cs[i]), want[i])
		}
	}
}

// Kilig on Discord: an MSIX Claude Desktop (Windows 10 LTSC) keeps Cowork's
// sessions in its package's LocalCache\Roaming\Claude, nothing in
// %APPDATA%\Claude; their calls are read from there.
func TestCallsCoworkMSIX(t *testing.T) {
	d := setupCalls(t)
	old := desktopdir.OS
	desktopdir.OS = "windows"
	callDesktopDirs = desktopDataDirs
	t.Cleanup(func() { desktopdir.OS = old })
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)
	os.MkdirAll(roaming, 0o755)
	data := filepath.Join(local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache", "Roaming", "Claude")
	writeLines(t, filepath.Join(data, "Local State"), `{}`)

	if ds := desktopDataDirs(); len(ds) != 3 || ds[0] != data {
		t.Fatalf("dirs: %v", ds)
	}
	cw := filepath.Join(data, "local-agent-mode-sessions", "acct", "org", "local_abc", ".claude", "projects", "-sessions-x")
	writeLines(t, filepath.Join(cw, "cw1.jsonl"), swap(claudeMsg("c1", "claude-sonnet-5-5", 5, 6, 7, 8, 3), `"entrypoint":"cli"`, `"entrypoint":"local-agent"`))
	writeLines(t, filepath.Join(d.claude, "projects", "-p", "sess1.jsonl"), claudeMsg("m1", "claude-opus-5-5", 1, 2, 3, 4, 1))
	Reset()
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[0].Agent != "claude-desktop" || cs[0].Tokens != (Tokens{5, 6, 7, 8, 0}) || cs[1].Agent != "claude" {
		t.Fatalf("calls: %+v", cs)
	}
}
