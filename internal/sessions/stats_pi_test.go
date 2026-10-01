package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// Pi's sessions tell their prompts, replies, tool calls and skills (#384):
// the assistant's toolCall parts, a codemode script's nestedCalls, a
// SKILL.md read and a /skill:name prompt. A system message, a tool's
// result and an extension's tagged message are no prompt, and a fork's
// copied entries count in the session they came from.
func TestPiToolsAndMessages(t *testing.T) {
	inZone(t, 0)
	_, pi := setupMore(t)
	id := "0199cccc-1111-7222-8333-444455556666"
	ts := func(s string) string { return `"timestamp":"2026-09-27T10:` + s + `.000Z"` }
	asst := func(at, content string) string {
		return `{"type":"message","id":"a` + at + `","parentId":null,` + ts(at) + `,"message":{"role":"assistant","content":` + content +
			`,"api":"openai-completions","provider":"magpie","model":"group/auto","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"totalTokens":15},"stopReason":"toolUse","timestamp":1790503200000}}` + "\n"
	}
	user := func(at, content string) string {
		return `{"type":"message","id":"u` + at + `","parentId":null,` + ts(at) + `,"message":{"role":"user","content":` + content + `,"timestamp":1790503200000}}` + "\n"
	}
	result := func(at, name, extra string) string {
		return `{"type":"message","id":"r` + at + `","parentId":null,` + ts(at) + `,"message":{"role":"toolResult","toolCallId":"c","toolName":"` + name +
			`","content":[{"type":"text","text":"ok"}],"isError":false` + extra + `,"timestamp":1790503200000}}` + "\n"
	}
	lines := `{"type":"session","version":3,"id":"` + id + `",` + ts("00:00") + `,"cwd":"/work/pitools"}` + "\n" +
		`{"type":"model_change","id":"m1","parentId":null,` + ts("00:00") + `,"provider":"magpie","modelId":"group/auto"}` + "\n" +
		`{"type":"message","id":"s1","parentId":null,` + ts("00:01") + `,"message":{"role":"system","content":"<available_skills>…</available_skills>","timestamp":1790503200000}}` + "\n" +
		user("00:02", `[{"type":"text","text":"what tools can you use"}]`) +
		asst("00:03", `[{"type":"thinking","thinking":"…"},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}},{"type":"toolCall","id":"c2","name":"read","arguments":{"path":"/home/me/.pi/agent/skills/pdf/SKILL.md"}}]`) +
		result("00:04", "bash", "") +
		asst("00:05", `[{"type":"toolCall","id":"c3","name":"codemode","arguments":{"code":"…"}}]`) +
		result("00:06", "codemode", `,"details":{"calls":[{"name":"mcp__opencli_mcp__open","status":"ok"}]},"nestedCalls":{"calls":[{"id":"n1","name":"mcp__opencli_mcp__open","status":"ok"},{"id":"n2","name":"read","status":"ok"}],"complete":true}`) +
		asst("00:07", `[{"type":"text","text":"done"}]`) +
		user("00:08", `"<extension-note>x</extension-note>"`) +
		user("00:09", `[{"type":"text","text":"<skill name=\"review\" location=\"/home/me/.pi/agent/skills/review/SKILL.md\">\nReferences are relative to /home/me/.pi/agent/skills/review.\n\nbody\n</skill>\n\nthe parser"}]`) +
		asst("00:10", `[{"type":"toolCall","id":"c4","name":"edit","arguments":{"path":"a.go"}},{"type":"toolCall","id":"c5","name":"write","arguments":{"path":"b.go"}}]`)
	path := filepath.Join(pi, "sessions", "--work-pitools--", "2026-09-27T10-00-00-000Z_"+id+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(lines), 0o644)

	s := statsAt(0, statsNow)
	var p, fork Summary
	for _, x := range s.Sessions {
		switch {
		case x.Cwd == "/work/pitools":
			p = x
		case x.Agent == "pi" && x.ID == piFork:
			fork = x
		}
	}
	// two prompts typed (the second with /skill:review), four replies; the
	// calls: bash, read, codemode, its two nested, edit and write
	if p.Prompts != 2 || p.Replies != 4 || p.ToolCalls != 7 {
		t.Fatalf("pi: %d prompts, %d replies, %d calls (%v)", p.Prompts, p.Replies, p.ToolCalls, p.tools)
	}
	if p.tools["bash"] != 1 || p.tools["read"] != 2 || p.tools["codemode"] != 1 || p.tools["mcp__opencli_mcp__open"] != 1 || p.tools["edit"] != 1 {
		t.Fatalf("pi tools %v", p.tools)
	}
	if p.skills["pdf"] != 1 || p.skills["review"] != 1 || len(p.skills) != 2 {
		t.Fatalf("pi skills %v", p.skills)
	}
	// the fork's own prompt and reply; what it copied is the first's
	if fork.Prompts != 1 || fork.Replies != 1 {
		t.Fatalf("fork: %d prompts, %d replies", fork.Prompts, fork.Replies)
	}

	o := s.Overview("pi", "", "/work/pitools", 10)
	if o.Tools.Calls != 7 || o.Tools.Sessions != 1 {
		t.Fatalf("tools %+v", o.Tools)
	}
	cats := map[string]int{}
	for _, c := range o.Tools.Categories {
		cats[c.Name] = c.Calls
	}
	if cats["Bash"] != 1 || cats["Read"] != 2 || cats["Edit"] != 1 || cats["Write"] != 1 || cats["Tool"] != 1 || cats["Other"] != 1 {
		t.Fatalf("categories %v", cats)
	}
	if o.Skills.Calls != 2 || o.Skills.Count != 2 {
		t.Fatalf("skills %+v", o.Skills)
	}
	if sh := o.Shape["messages"]; sh.Total != 1 {
		t.Fatalf("shape by messages %+v", sh)
	}
	if sh := o.Shape["autonomy"]; sh.Total != 1 {
		t.Fatalf("shape by autonomy %+v", sh)
	}
}

func TestPiSkillRead(t *testing.T) {
	for path, want := range map[string]string{
		"/home/me/.pi/agent/skills/pdf/SKILL.md": "pdf", `C:\Users\me\.pi\agent\skills\pdf\SKILL.md`: "pdf",
		"skill://pdf": "pdf", "skill://pdf/SKILL.md": "pdf", "skill://pdf/reference.md": "", "SKILL.md": "", "/work/README.md": "",
	} {
		p := piPart{Type: "toolCall", Name: "read"}
		p.Arguments.Path = path
		if got := piSkillRead(p); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestToolCategoryPi(t *testing.T) {
	for name, want := range map[string]string{"bash": "Bash", "powershell": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "find": "Glob", "ls": "Glob", "grep": "Grep"} {
		if got := ToolCategory(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}
