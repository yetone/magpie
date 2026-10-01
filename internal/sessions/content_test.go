package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kinds are the parts as "role/kind[/name]: first 24 characters", one line each, to read
// what was said at a glance.
func kinds(ps []Part) []string {
	var out []string
	for _, p := range ps {
		s := p.Role + "/" + p.Kind
		if p.Name != "" {
			s += "/" + p.Name
		}
		w := strings.Join(strings.Fields(p.Text), " ")
		if len(w) > 24 {
			w = w[:24]
		}
		out = append(out, s+": "+w)
	}
	return out
}

func claudeUser(sec int, content string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","sessionId":"sess1","message":{"role":"user","content":%s}}`, stamp(sec), content)
}

func claudeBlocks(id string, sec int, blocks ...string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s","sessionId":"sess1","requestId":"req_%s","message":{"id":"%s","model":"claude-opus-5-5","content":[%s],"usage":{"input_tokens":5,"output_tokens":9,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		stamp(sec), id, id, strings.Join(blocks, ","))
}

// What was said in a Claude Code call, from where it was asked to where it
// ended: the prompt or the tool's result it was given, its words, its
// reasoning and its calls of tools, in the order of the file; a message of
// several lines is all of them, and another message that comes between is not.
func TestContentOfClaude(t *testing.T) {
	d := setupCalls(t)
	file := filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl")
	writeLines(t, file,
		claudeUser(1, `"Fix the login bug"`),
		`{"type":"attachment","timestamp":"`+stamp(1)+`","attachment":{"type":"skill_listing"}}`,
		claudeBlocks("m1", 2, `{"type":"thinking","thinking":"The user wants the login fixed."}`),
		claudeBlocks("other", 3, `{"type":"text","text":"a message that is not this one"}`),
		claudeBlocks("m1", 4, `{"type":"text","text":"I'll read the file first."}`),
		claudeBlocks("m1", 5, `{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/login.go"}}`),
		claudeUser(9, `[{"type":"tool_result","tool_use_id":"t1","content":"package login\nfunc Login() {}"},{"type":"text","text":"and be quick"}]`),
		claudeUser(9, `[{"type":"text","text":"<system-reminder>be careful</system-reminder>"}]`),
		`{"type":"user","isMeta":true,"timestamp":"`+stamp(9)+`","message":{"role":"user","content":"meta, left out"}}`,
		claudeBlocks("m2", 12, `{"type":"text","text":"Fixed: the check was inverted."}`),
	)
	byMsg := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		byMsg[c.Msg] = c
	}
	c1, err := ContentOf(byMsg["m1"])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(kinds(c1.Input), "|"), "user/text: Fix the login bug"; got != want {
		t.Errorf("m1's input: %s", got)
	}
	want := "assistant/thinking: The user wants the login|assistant/text: I'll read the file first|assistant/tool_use/Read: { \"file_path\": \"/a/login"
	if got := strings.Join(kinds(c1.Output), "|"); got != want {
		t.Errorf("m1's output:\n got %s\nwant %s", got, want)
	}
	c2, err := ContentOf(byMsg["m2"])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(kinds(c2.Input), "|"), "tool/tool_result: package login func Login|user/text: and be quick|user/context: <system-reminder>be care"; got != want {
		t.Errorf("m2's input (the tool's result, and what came with it; the meta line and the earlier message left out):\n got %s\nwant %s", got, want)
	}
	if got, want := strings.Join(kinds(c2.Output), "|"), "assistant/text: Fixed: the check was inv"; got != want {
		t.Errorf("m2's output: %s", got)
	}
}

// A file read on as it grows keeps the places right: the call read in the
// first pass and the one read in the second are both found where they are.
func TestContentOfClaudeAfterAppend(t *testing.T) {
	d := setupCalls(t)
	file := filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl")
	writeLines(t, file, claudeUser(1, `"first"`), claudeBlocks("m1", 2, `{"type":"text","text":"one"}`))
	if len(Calls(time.Time{})) != 1 {
		t.Fatal("one call")
	}
	appendText(t, file, claudeUser(3, `"second"`)+"\n"+claudeBlocks("m2", 4, `{"type":"text","text":"two"}`)+"\n")
	got := map[string]string{}
	for _, c := range Calls(time.Time{}) {
		ct, err := ContentOf(c)
		if err != nil {
			t.Fatal(err)
		}
		got[c.Msg] = strings.Join(kinds(ct.Input), "|") + " => " + strings.Join(kinds(ct.Output), "|")
	}
	if got["m1"] != "user/text: first => assistant/text: one" || got["m2"] != "user/text: second => assistant/text: two" {
		t.Fatalf("%v", got)
	}
}

// What was said in a Codex call: the items between the call before it and its
// own count — the prompt and the context the agent put in, the tool's output,
// then the model's reasoning, words and calls of tools.
func TestContentOfCodex(t *testing.T) {
	d := setupCalls(t)
	item := func(sec int, payload string) string {
		return fmt.Sprintf(`{"timestamp":"%s","type":"response_item","payload":%s}`, stamp(sec), payload)
	}
	lines := []string{`{"timestamp":"` + stamp(0) + `","type":"session_meta","payload":{"id":"t","session_id":"sess-x","base_instructions":{"text":"long"}}}`}
	lines = append(lines, cxTurnLines(1, "t1", "gpt-6-astra", "low")...)
	lines = append(lines,
		item(1, `{"type":"message","role":"developer","content":[{"type":"input_text","text":"left out"}]}`),
		item(1, `{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}`),
		item(1, `{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files"}]}`),
		item(2, `{"type":"reasoning","summary":[{"type":"summary_text","text":"Need to list."}],"encrypted_content":"xxxx"}`),
		item(2, `{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"c1"}`),
		tokenCountLine(3, cxUse(1000, 0, 0, 50, 10), cxUse(1000, 0, 0, 50, 10)),
		item(4, `{"type":"function_call_output","call_id":"c1","output":"a.go\nb.go"}`),
		item(5, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There are two files."}]}`),
		tokenCountLine(6, cxUse(2500, 0, 0, 90, 10), cxUse(1500, 0, 0, 40, 0)),
	)
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"), lines...)
	cs := Calls(time.Time{}) // newest first
	if len(cs) != 2 {
		t.Fatalf("%d calls", len(cs))
	}
	first, second := cs[1], cs[0]
	c1, err := ContentOf(first)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(kinds(c1.Input), "|"), "user/context: <environment_context>cwd|user/text: List the files"; got != want {
		t.Errorf("first call's input:\n got %s\nwant %s", got, want)
	}
	if got, want := strings.Join(kinds(c1.Output), "|"), "assistant/thinking: Need to list.|assistant/tool_use/shell: { \"command\": [ \"ls\" ] }"; got != want {
		t.Errorf("first call's output:\n got %s\nwant %s", got, want)
	}
	c2, err := ContentOf(second)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(kinds(c2.Input), "|"), "tool/tool_result: a.go b.go"; got != want {
		t.Errorf("second call's input: %s", got)
	}
	if got, want := strings.Join(kinds(c2.Output), "|"), "assistant/text: There are two files."; got != want {
		t.Errorf("second call's output: %s", got)
	}
}

// A long part is cut, and says by how much; when all of it is more than is kept,
// what is left off is said too; a call that has no place in a file has no content.
func TestContentCut(t *testing.T) {
	d := setupCalls(t)
	file := filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl")
	long := strings.Repeat("é", partMax+250)
	writeLines(t, file, claudeUser(1, `"`+long+`"`), claudeBlocks("m1", 2, `{"type":"text","text":"ok"}`))
	c, err := ContentOf(Calls(time.Time{})[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Input) != 1 || c.Input[0].Cut != 250 || len([]rune(c.Input[0].Text)) != partMax || c.Cut {
		t.Fatalf("a long part: cut %d, %d characters", c.Input[0].Cut, len([]rune(c.Input[0].Text)))
	}
	// more parts than the whole keeps
	var lines []string
	blocks := make([]string, 30)
	for i := range blocks {
		blocks[i] = fmt.Sprintf(`{"type":"text","text":"%s"}`, strings.Repeat("x", partMax))
	}
	lines = append(lines, claudeUser(3, `"go"`), claudeBlocks("m2", 4, blocks...))
	appendText(t, file, strings.Join(lines, "\n")+"\n")
	for _, x := range Calls(time.Time{}) {
		if x.Msg != "m2" {
			continue
		}
		c, err := ContentOf(x)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Cut || len(c.Output) >= 30 || len(c.Output) < 15 {
			t.Fatalf("the whole is cut: %d parts, cut %v", len(c.Output), c.Cut)
		}
	}
	if _, err := ContentOf(Call{Agent: "claude"}); err == nil {
		t.Fatal("a call with no file has no content")
	}
}

// A session's call is found by the time it was made in, between two: the newest
// of those in the window, and not those of another session or outside it.
func TestFindCall(t *testing.T) {
	d := setupCalls(t)
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		claudeBlocks("a", 10, `{"type":"text","text":"a"}`),
		claudeBlocks("b", 20, `{"type":"text","text":"b"}`),
		claudeBlocks("c", 40, `{"type":"text","text":"c"}`))
	at := func(s int) time.Time { return callT0.Add(time.Duration(s) * time.Second) }
	if c, ok := FindCall("sess1", at(15), at(25), at(25)); !ok || c.Msg != "b" {
		t.Fatalf("in the window: %+v %v", c, ok)
	}
	// a gateway request that began at 5 s and took 5 s: its window, 5 s before
	// to 30 s after, holds a and b; it ended nearest a's end
	if c, ok := FindCall("sess1", at(0), at(40), at(10)); !ok || c.Msg != "a" {
		t.Fatalf("the one that ended nearest, not the newest: %+v", c)
	}
	if c, ok := FindCall("sess1", at(5), at(30), at(30)); !ok || c.Msg != "b" {
		t.Fatalf("the nearest of two: %+v", c)
	}
	if _, ok := FindCall("sess1", at(25), at(35), at(30)); ok {
		t.Fatal("none in it")
	}
	if _, ok := FindCall("other", at(0), at(60), at(0)); ok {
		t.Fatal("another session's")
	}
	if _, ok := FindCall("", at(0), at(60), at(0)); ok {
		t.Fatal("no session")
	}
}

// A fast tool loop: the gateway's row for the first call (began at 0, took
// 2 s) is shown the first call's content, not the third's, though the window
// it is looked for in holds all three.
func TestFindCallToolLoop(t *testing.T) {
	d := setupCalls(t)
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl"),
		`{"type":"user","timestamp":"`+stamp(0)+`","message":{"role":"user","content":"first prompt"},"sessionId":"sess1"}`,
		claudeMsg("m1", "claude-opus-5-5", 100, 1, 0, 0, 2),
		`{"type":"user","timestamp":"`+stamp(3)+`","message":{"role":"user","content":[{"type":"tool_result","content":"tool out A"}]},"sessionId":"sess1"}`,
		claudeMsg("m2", "claude-opus-5-5", 100, 1, 0, 0, 5),
		`{"type":"user","timestamp":"`+stamp(6)+`","message":{"role":"user","content":[{"type":"tool_result","content":"tool out B"}]},"sessionId":"sess1"}`,
		claudeMsg("m3", "claude-opus-5-5", 100, 1, 0, 0, 8),
	)
	for i, want := range []string{"m1", "m2", "m3"} {
		start := callT0.Add(time.Duration(i*3) * time.Second) // each asked for as the last ended
		c, ok := FindCall("sess1", start.Add(-5*time.Second), start.Add(32*time.Second), start.Add(2*time.Second))
		if !ok || c.Msg != want {
			t.Errorf("gateway row %d shows the content of %s, want %s", i, c.Msg, want)
		}
	}
}
