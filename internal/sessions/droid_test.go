package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	droidMain = "d0000000-0000-4000-8000-00000000000a"
	droidSub  = "d0000000-0000-4000-8000-00000000000b"
	droidFork = "d0000000-0000-4000-8000-00000000000c"
	droidOld  = "d0000000-0000-4000-8000-00000000000d"
)

// putDroid writes Droid's sessions under home/.factory as droid keeps
// them: a folder per cwd, an older one right in sessions/, each
// transcript's settings beside it.
func putDroid(t *testing.T, home string) {
	t.Helper()
	root := filepath.Join(home, ".factory")
	write := func(rel string, lines ...string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// magpie's own custom model, as the agent writes it
	write("settings.json", `{"customModels":[{"model":"anthropic/claude-sonnet-4-5","id":"custom:magpie/anthropic/claude-sonnet-4-5","provider":"anthropic"}]}`)
	start := `{"type":"session_start","id":"` + droidMain + `","title":"Login bug","owner":"me","version":2,"cwd":"/work/droid"}`
	m1 := `{"type":"message","id":"m1","timestamp":"2026-09-27T08:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>house rules</system-reminder>"}]}}`
	m2 := `{"type":"message","id":"m2","timestamp":"2026-09-27T08:00:05.000Z","message":{"role":"user","content":[{"type":"text","text":"Fix the   login bug"}]}}`
	m3 := `{"type":"message","id":"m3","timestamp":"2026-09-27T08:01:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Reading"},{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`
	m4 := `{"type":"message","id":"m4","timestamp":"2026-09-27T08:02:00.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	write("sessions/-work-droid/"+droidMain+".jsonl", start, m1, m2, m3, m4)
	write("sessions/-work-droid/"+droidMain+".settings.json", `{"model":"custom:magpie/anthropic/claude-sonnet-4-5","tokenUsage":{"inputTokens":100,"outputTokens":50,"cacheCreationTokens":10,"cacheReadTokens":1000,"thinkingTokens":20}}`)
	// a subagent it called: its spend is the session's
	write("sessions/-work-droid/"+droidSub+".jsonl",
		`{"type":"session_start","id":"`+droidSub+`","title":"Explore","owner":"me","cwd":"/work/droid","callingSessionId":"`+droidMain+`"}`,
		`{"type":"message","id":"s1","timestamp":"2026-09-27T08:01:10.000Z","message":{"role":"user","content":"Look around"}}`)
	write("sessions/-work-droid/"+droidSub+".settings.json", `{"model":"claude-haiku-4-5","tokenUsage":{"inputTokens":5,"outputTokens":2,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0}}`)
	// a fork: the lines it copied are its source's
	write("sessions/-work-droid/"+droidFork+".jsonl",
		`{"type":"session_start","id":"`+droidFork+`","title":"Login bug","owner":"me","cwd":"/work/droid","parent":"`+droidMain+`","forkedAtMessageId":"m4"}`,
		m1, m2, m3, m4,
		`{"type":"message","id":"f1","timestamp":"2026-09-27T09:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"Try another way"}]}}`,
		`{"type":"message","id":"f2","timestamp":"2026-09-27T09:00:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Sure"}]}}`)
	write("sessions/-work-droid/"+droidFork+".settings.json", `{"model":"custom:Kimi-K2-[Groq]-0","tokenUsage":{"inputTokens":7,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0}}`)
	// an older one, in sessions/ itself, with no settings
	write("sessions/"+droidOld+".jsonl",
		`{"type":"session_start","id":"`+droidOld+`","title":"New Session","owner":"me"}`,
		`{"type":"message","id":"o1","timestamp":"2026-09-20T10:00:00.000Z","message":{"role":"user","content":"hi there"}}`)
}

func TestDroid(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	t.Setenv("FACTORY_HOME_OVERRIDE", "")
	putDroid(t, home)
	Reset()
	ss := List(0)
	if n := count(ss, "droid"); n != 3 {
		t.Fatalf("want 3 Droid sessions (the subagent in its session), got %d", n)
	}
	d := find(t, ss, "droid", droidMain)
	if d.Cwd != "/work/droid" || d.Title != "Login bug" {
		t.Fatalf("droid: %+v", d)
	}
	// magpie's custom model by the model it asks for, the subagent's in
	if m := model(d, "anthropic/claude-sonnet-4-5"); m.Tokens != (Tokens{100, 50, 1000, 10, 0}) {
		t.Fatalf("sonnet: %+v", d.Models)
	}
	if m := model(d, "claude-haiku-4-5"); m.Tokens != (Tokens{5, 2, 0, 0, 0}) {
		t.Fatalf("haiku, the subagent's in: %+v", d.Models)
	}
	if !d.Start.Equal(at("2026-09-27T08:00:00Z")) || !d.Last.Equal(at("2026-09-27T08:02:00Z")) {
		t.Fatalf("droid times %s %s", d.Start, d.Last)
	}
	want := "cd '/work/droid' && droid --resume " + droidMain
	if runtime.GOOS == "windows" {
		want = "Set-Location -LiteralPath '/work/droid'; droid --resume " + droidMain
	}
	if d.Resume != want {
		t.Fatalf("resume %q", d.Resume)
	}
	// the fork: what it copied is its source's; a custom model of the
	// user's own by its name
	f := find(t, ss, "droid", droidFork)
	if f.Title != "Login bug" || !f.Start.Equal(at("2026-09-27T09:00:00Z")) || !f.Last.Equal(at("2026-09-27T09:00:30Z")) {
		t.Fatalf("fork: %+v", f)
	}
	if m := model(f, "Kimi-K2"); m.Tokens != (Tokens{7, 3, 0, 0, 0}) {
		t.Fatalf("fork models: %+v", f.Models)
	}
	if o := find(t, ss, "droid", droidOld); o.Title != "hi there" || !o.Tokens.zero() {
		t.Fatalf("old: %+v", o)
	}

	// $FACTORY_HOME_OVERRIDE
	moved := filepath.Join(filepath.Dir(home), "factory-home")
	os.MkdirAll(moved, 0o755)
	if err := os.Rename(filepath.Join(home, ".factory"), filepath.Join(moved, ".factory")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_HOME_OVERRIDE", moved)
	Reset()
	find(t, List(0), "droid", droidMain)
}
