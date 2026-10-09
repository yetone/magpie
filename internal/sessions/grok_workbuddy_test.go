package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	grokMain = "0190aaaa-0000-7000-8000-00000000000a"
	grokFork = "0190aaaa-0000-7000-8000-00000000000c"
	wbID     = "22222222-aaaa-4bbb-8ccc-000000000001"
	wbOldID  = "0123456789abcdef0123456789abcdef"
)

func TestGrok(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	ss := List(0)
	if n := count(ss, "grok"); n != 2 {
		t.Fatalf("want 2 Grok Build sessions (the subagent in its session, the one of hooks only left out), got %d", n)
	}
	g := find(t, ss, "grok", grokMain)
	// the first prompt typed, its chunks put together, before the title Grok made
	if g.Cwd != "/work/grok" || g.Title != "Parser port" {
		t.Fatalf("grok: %+v", g)
	}
	// the cache out of the input, the subagent's turn in once
	if m := model(g, "grok-4.7-build"); m.Tokens != (Tokens{700, 100, 2800, 0, 0}) {
		t.Fatalf("build: %+v", m)
	}
	if m := model(g, "grok-4.7-mini"); m.Tokens != (Tokens{300, 30, 100, 0, 0}) {
		t.Fatalf("mini, the subagent's in: %+v", m)
	}
	// the hook that ran as it was closed, a day on, isn't when it was at work
	if !g.Start.Equal(at("2026-09-27T08:00:00Z")) || !g.Last.Equal(at("2026-09-27T08:02:10Z")) {
		t.Fatalf("grok times %s %s", g.Start, g.Last)
	}
	if runtime.GOOS != "windows" {
		if want := "cd '/work/grok' && grok --resume " + grokMain; g.Resume != want {
			t.Fatalf("resume %q", g.Resume)
		}
	}
	// the fork: what it copied counted in the session it came from
	f := find(t, ss, "grok", grokFork)
	if f.Tokens != (Tokens{100, 30, 300, 0, 0}) || f.Title != "Parser port, another way" {
		t.Fatalf("fork: %+v", f)
	}
	if !f.Start.Equal(at("2026-09-27T09:00:00.25Z")) || !f.Last.Equal(at("2026-09-27T09:00:30Z")) {
		t.Fatalf("fork times %s %s", f.Start, f.Last)
	}
	var active int64
	for _, d := range StatsFor(0).Days {
		for _, a := range d.Active {
			if a.Agent == "grok" {
				active += a.Seconds
			}
		}
	}
	if active < 150 || active > 170 {
		t.Fatalf("active %ds, want the main session's 130 and the fork's 30", active)
	}

	// $GROK_HOME
	moved := filepath.Join(filepath.Dir(home), "grok-home")
	if err := os.Rename(filepath.Join(home, ".grok"), moved); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_HOME", moved)
	Reset()
	find(t, List(0), "grok", grokMain)
}

func TestWorkBuddy(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	ss := List(0)
	// the launch session, the one whose calls went through magpie's gateway,
	// and the one brought over from the older history
	if n := count(ss, "workbuddy"); n != 3 {
		t.Fatalf("want 3 WorkBuddy sessions, got %d", n)
	}
	w := find(t, ss, "workbuddy", wbID)
	if w.Cwd != "/work/wb" || w.Title != "Launch notes draft" || w.Resume != "" {
		t.Fatalf("workbuddy: %+v", w)
	}
	// a reply's usage counted once, however many of its lines carry it: the
	// last line's is the reply's own total, here m2's (9999 − 7777 cached)
	if m := model(w, "hy3"); m.Tokens != (Tokens{2422, 968, 8777, 0, 0}) {
		t.Fatalf("hy3: %+v", m)
	}
	if !w.Start.Equal(at("2026-09-27T10:00:00Z")) || !w.Last.Equal(at("2026-09-27T10:00:10Z")) {
		t.Fatalf("workbuddy times %s %s", w.Start, w.Last)
	}
	// brought over from its older history: the cwd in its meta file, and
	// each of its three replies counted once — they name no messageId
	o := find(t, ss, "workbuddy", wbOldID)
	if o.Cwd != "/work/wb-old" || o.Title != "Quarterly report" {
		t.Fatalf("old: %+v", o)
	}
	if m := model(o, "auto"); m.Tokens != (Tokens{5500, 130, 0, 0, 0}) {
		t.Fatalf("auto: %+v", m)
	}

	// $WORKBUDDY_CONFIG_DIR
	moved := filepath.Join(filepath.Dir(home), "wb")
	if err := os.Rename(filepath.Join(home, ".workbuddy"), moved); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_CONFIG_DIR", moved)
	Reset()
	find(t, List(0), "workbuddy", wbID)
}

func TestWorkBuddyPrompt(t *testing.T) {
	for in, want := range map[string]string{
		`[{"type":"input_text","text":"<system-reminder>x</system-reminder>"},{"type":"input_text","text":"@scene:4"},{"type":"input_text","text":"Fix   it"}]`: "Fix it",
		`"plain words"`:                                "plain words",
		`"<command>x</command>"`:                       "",
		`[{"type":"input_image","image_url":"data:"}]`: "",
	} {
		if got := wbPrompt(json.RawMessage(in)); got != want {
			t.Errorf("wbPrompt(%s) = %q, want %q", in, got, want)
		}
	}
}
