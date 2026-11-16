package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// droidCalls are the Droid calls Calls gives, by transcript.
func droidCalls(t *testing.T) map[string]Call {
	t.Helper()
	out := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "droid" {
			if _, dup := out[c.Msg]; dup {
				t.Fatalf("%s counted twice", c.Msg)
			}
			out[c.Msg] = c
		}
	}
	return out
}

// Droid's own calls reach the usage ledger (_meouwu on X: Droid's
// sessions were listed, but Usage counted none of what they spent).
// testdata/droid-skills are real droid 0.231.0 sessions (see
// TestDroidToolsAndSkills); droid writes no tokens into a transcript, only
// each session's running total into its settings, so each session is one
// call of that total, at its last message, under its settings' model.
func TestDroidCalls(t *testing.T) {
	// a home with droid's settings as custom says, "" for none
	home := func(custom string) {
		t.Helper()
		home := setupAgents(t)
		t.Setenv("FACTORY_HOME_OVERRIDE", "")
		t.Setenv("MAGPIE_ADDR", "127.0.0.1:39417")
		copyTree(t, "testdata/droid-skills", filepath.Join(home, ".factory"))
		if custom != "" {
			if err := os.WriteFile(filepath.Join(home, ".factory", "settings.json"), []byte(`{"customModels":[{"model":"mh","id":"custom:mh","baseUrl":"`+custom+`","apiKey":"k","provider":"generic-chat-completion-api"}]}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		Reset()
	}
	home("")
	cs := droidCalls(t)
	if len(cs) != 4 {
		t.Fatalf("want the 4 Droid sessions' totals, got %d: %+v", len(cs), cs)
	}
	for _, w := range []struct {
		id, last string
		tokens   Tokens
	}{
		{"86f11060-c188-4609-942b-8c3fc53638dc", "2026-10-30T03:02:08.518Z", Tokens{Input: 30750, Output: 113}},
		{"99bfae77-1ca3-4cc9-9c35-445f1fe4dfcd", "2026-10-30T03:02:28.602Z", Tokens{Input: 15435, Output: 330}},
		{"aea4c8cc-92da-4695-889d-5ad6c5b6cec3", "2026-10-30T03:02:38.879Z", Tokens{Input: 15539, Output: 76}},
		{"cb1d8bed-aa2c-4ba2-a23f-681dc5da403f", "2026-10-30T03:02:17.660Z", Tokens{Input: 15329, Output: 66}},
	} {
		c, ok := cs["droid:"+w.id]
		if !ok {
			t.Errorf("session %s not counted", w.id)
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, w.last)
		// no settings.json here names custom:mh, so it is priced by its name
		if c.Session != w.id || c.Model != "mh" || c.Tokens != w.tokens || !c.Time.Equal(at) {
			t.Errorf("session %s: %+v", w.id, c)
		}
	}

	// custom:mh on a model server of the user's own on this computer is
	// still counted; on magpie's gateway, the gateway counted its calls
	home("http://localhost:11434/v1")
	if cs := droidCalls(t); len(cs) != 4 {
		t.Fatalf("a model of the user's own on another port: %d calls, want 4", len(cs))
	}
	home("http://127.0.0.1:39417/v1")
	if cs := droidCalls(t); len(cs) != 0 {
		t.Fatalf("sessions through magpie's gateway counted again: %+v", cs)
	}

	home("")
	if err := SetReads(nil, map[string]bool{"droid": false}); err != nil {
		t.Fatal(err)
	}
	if cs := droidCalls(t); len(cs) != 0 {
		t.Fatalf("turning off Droid still counts its calls: %+v", cs)
	}
}

// A subagent's total is counted under the session that called it, a
// fork's from none, a session with no settings not at all, and one on
// magpie's own entry (custom:magpie/…) is the gateway's to count.
func TestDroidCallsOfSessions(t *testing.T) {
	home := setupAgents(t)
	t.Setenv("FACTORY_HOME_OVERRIDE", "")
	putDroid(t, home)
	Reset()

	cs := droidCalls(t)
	if len(cs) != 2 {
		t.Fatalf("want the subagent's and the fork's, got %d: %+v", len(cs), cs)
	}
	if c := cs["droid:"+droidSub]; c.Session != droidMain || c.Model != "claude-haiku-4-5" || c.Tokens != (Tokens{Input: 5, Output: 2}) || c.Cwd != "/work/droid" ||
		!c.Time.Equal(at("2026-09-27T08:01:10Z")) {
		t.Errorf("subagent: %+v", c)
	}
	if c := cs["droid:"+droidFork]; c.Session != droidFork || c.Model != "Kimi-K2" || c.Tokens != (Tokens{Input: 7, Output: 3}) ||
		!c.Time.Equal(at("2026-09-27T09:00:30Z")) {
		t.Errorf("fork: %+v", c)
	}

	// the same session, its settings on a model of Factory's own
	p := filepath.Join(home, ".factory", "sessions", "-work-droid", droidMain+".settings.json")
	if err := os.WriteFile(p, []byte(`{"model":"claude-sonnet-4-5-20250929","tokenUsage":{"inputTokens":100,"outputTokens":50,"cacheCreationTokens":10,"cacheReadTokens":1000,"thinkingTokens":20}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	c, ok := droidCalls(t)["droid:"+droidMain]
	if !ok || c.Model != "claude-sonnet-4-5-20250929" || c.Tokens != (Tokens{Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 10}) ||
		!c.Time.Equal(at("2026-09-27T08:02:00Z")) {
		t.Errorf("on Factory's own model: %+v (%v)", c, ok)
	}
}
