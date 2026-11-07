package library

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// #5604 (NEKO-CwC): a session-start hook narrowed to some starts, as a
// "re-inject after compaction" hook is to compact alone. It reaches each
// agent whose session start takes a matcher in that agent's own words for
// those starts, and is given to no agent that can't run it at just those,
// with why: never widened to every start, nor written with a matcher the
// agent never matches. Only Read's view and the files are looked at, so
// this compiles against the code before the change and fails there.
func TestHookSessionStartNarrowed(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/settings.json")
	codex := filepath.Join(h, ".codex/hooks.json")
	gem := filepath.Join(h, ".gemini/settings.json")
	cur := filepath.Join(h, ".cursor/hooks.json")
	grok := filepath.Join(h, ".grok/config.toml")
	write(t, grok, "model = \"grok-4.7\"\n")
	every := []string{"claude", "codex", "cursor", "gemini", "grok"}

	// save saves the hook, and says why each agent it couldn't be given
	// wasn't, as the page shows it (Read with the change's problems)
	save := func(old string, x Hook) map[string]string {
		t.Helper()
		res, err := SaveHook(old, x)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Read(res.Problems)
		if err != nil {
			t.Fatal(err)
		}
		for _, y := range v.Hooks {
			if y.Name == x.Name {
				return y.Problems
			}
		}
		t.Fatalf("no hook %s in the view", x.Name)
		return nil
	}
	matcherOf := func(path, ev string) []string {
		t.Helper()
		var out []string
		for _, e := range hooksOf(t, path, ev) {
			out = append(out, gjson.Get(e, "matcher").String())
		}
		return out
	}

	// compact alone: Claude Code and Codex take it as written; Gemini CLI
	// and Grok never start a session after compaction, and Cursor's start
	// takes no matcher at all
	p := save("", Hook{Name: "reinject", Event: "session-start", Matcher: "compact", Command: "cat AGENTS.md", Agents: every})
	if got := matcherOf(claude, "SessionStart"); !slices.Equal(got, []string{"compact"}) {
		t.Errorf("claude SessionStart matchers: %q", got)
	}
	if got := matcherOf(codex, "SessionStart"); !slices.Equal(got, []string{"compact"}) {
		t.Errorf("codex SessionStart matchers: %q", got)
	}
	if got := hooksOf(t, gem, "SessionStart"); len(got) != 0 {
		t.Errorf("gemini was given a compact-only hook it would run at every start: %v", got)
	}
	if got := hooksOf(t, cur, "sessionStart"); len(got) != 0 {
		t.Errorf("cursor was given a compact-only hook it would run at every start: %v", got)
	}
	if got := grokBlocks(t, grok, "SessionStart"); len(got) != 0 {
		t.Errorf("grok was given a compact-only hook: %q", got)
	}
	for _, a := range []string{"gemini", "cursor", "grok"} {
		if p[a] == "" {
			t.Errorf("%s isn't said why it has no compact-only hook: %+v", a, p)
		}
	}
	if p["claude"] != "" || p["codex"] != "" {
		t.Errorf("problems for the agents that took it: %+v", p)
	}

	// an agent's own edit of the entry magpie wrote is theirs: changing the
	// hook leaves it, and the new one is written beside it
	s := read(t, claude)
	write(t, claude, strings.Replace(s, "cat AGENTS.md", "cat AGENTS.md CLAUDE.md", 1))
	save("reinject", Hook{Name: "reinject", Event: "session-start", Matcher: "resume", Command: "cat AGENTS.md", Agents: every})
	if got := matcherOf(claude, "SessionStart"); !slices.Equal(got, []string{"compact", "resume"}) {
		t.Errorf("claude SessionStart after the change, the user's edit kept: %q", got)
	}
	// one start each tells apart: Gemini CLI's own word, Grok's "load"
	if got := matcherOf(gem, "SessionStart"); !slices.Equal(got, []string{"resume"}) {
		t.Errorf("gemini SessionStart matchers: %q", got)
	}
	if got := grokBlocks(t, grok, "SessionStart"); len(got) != 1 || !strings.Contains(got[0], `matcher = "load"`) {
		t.Errorf("grok SessionStart: %q", got)
	}
	if got := matcherOf(codex, "SessionStart"); !slices.Equal(got, []string{"resume"}) {
		t.Errorf("codex SessionStart, compact's taken out: %q", got)
	}

	// every start but a fork: Claude Code and Codex are told so; Gemini CLI
	// and Grok never fork a session, so for them it is every start, written
	// whole; Cursor can't be told
	save("reinject", Hook{Name: "reinject", Event: "session-start", Matcher: "startup|resume|clear|compact", Command: "cat AGENTS.md", Agents: every})
	if got := matcherOf(codex, "SessionStart"); !slices.Equal(got, []string{"startup|resume|clear|compact"}) {
		t.Errorf("codex SessionStart, every start but a fork: %q", got)
	}
	if got := matcherOf(gem, "SessionStart"); !slices.Equal(got, []string{""}) {
		t.Errorf("gemini SessionStart, every start it has: %q", got)
	}
	if got := grokBlocks(t, grok, "SessionStart"); len(got) != 1 || strings.Contains(got[0], "matcher") {
		t.Errorf("grok SessionStart, every start it has: %q", got)
	}
	if got := hooksOf(t, cur, "sessionStart"); len(got) != 0 {
		t.Errorf("cursor was given a hook it would run at a fork too: %v", got)
	}

	// a fresh session or one after /clear: Grok's "new" is both; Gemini CLI
	// compares its matcher whole, so it can't be told two of its starts
	p = save("reinject", Hook{Name: "reinject", Event: "session-start", Matcher: "startup|clear", Command: "cat AGENTS.md", Agents: every})
	if got := grokBlocks(t, grok, "SessionStart"); len(got) != 1 || !strings.Contains(got[0], `matcher = "new"`) {
		t.Errorf("grok SessionStart: %q", got)
	}
	if got := hooksOf(t, gem, "SessionStart"); len(got) != 0 || p["gemini"] == "" || p["grok"] != "" {
		t.Errorf("gemini given two of its starts it can't name together: %v", got)
	}

	// turned on for many agents at once, it goes to those that can run it
	if _, err := SaveHook("", Hook{Name: "late", Event: "session-start", Matcher: "compact", Command: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SomeHooksAgents([]string{"late"}, every, true); err != nil {
		t.Fatal(err)
	}
	if a := mustLoad(t).hook("late").Agents; !slices.Equal(a, []string{"claude", "codex"}) {
		t.Errorf("late's agents: %v", a)
	}

	// the page is told which starts each agent can be narrowed to
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v.HookAgents)
	starts := func(id string) []string {
		var out []string
		for _, a := range gjson.ParseBytes(raw).Array() {
			if a.Get("id").String() == id {
				for _, s := range a.Get("starts").Array() {
					out = append(out, s.String())
				}
			}
		}
		return out
	}
	if s := starts("claude"); !slices.Contains(s, "compact") || !slices.Contains(s, "startup|resume|clear|compact") {
		t.Errorf("claude's starts: %v", s)
	}
	if s := starts("gemini"); !slices.Contains(s, "resume") || slices.Contains(s, "compact") || slices.Contains(s, "startup|clear") {
		t.Errorf("gemini's starts: %v", s)
	}
	if s := starts("grok"); !slices.Contains(s, "startup|clear") || slices.Contains(s, "startup") {
		t.Errorf("grok's starts: %v", s)
	}
	if s := starts("cursor"); len(s) != 0 {
		t.Errorf("cursor's starts: %v", s)
	}

	ok(t)(RemoveHooks([]string{"reinject", "late"}))
	if got := matcherOf(claude, "SessionStart"); !slices.Equal(got, []string{"compact"}) {
		t.Errorf("claude SessionStart after removal, the user's own edit kept alone: %q", got)
	}
}

// Only the tool events take a tool, and only session-start takes a start:
// the validation of each stays as it was.
func TestHookMatcherKeptToItsEvent(t *testing.T) {
	sandbox(t)
	for _, c := range []Hook{
		{Event: "turn-end", Matcher: "compact"},
		{Event: "after-tool", Matcher: "compact"},
		{Event: "before-tool", Matcher: "startup"},
		{Event: "session-start", Matcher: "shell"},
		{Event: "session-start", Matcher: "compact|compact"},
		{Event: "session-start", Matcher: "compact|"},
		{Event: "session-start", Matcher: "Compact"},
		{Event: "prompt", Matcher: "resume"},
	} {
		c.Name, c.Command = "x", "x"
		if _, err := SaveHook("", c); err == nil {
			t.Errorf("%s was narrowed to %q", c.Event, c.Matcher)
			ok(t)(RemoveHooks([]string{"x"}))
		}
	}
	for _, m := range []string{"startup", "fork", "clear|fork", "startup|resume|clear|compact|fork"} {
		ok(t)(SaveHook("", Hook{Name: "x", Event: "session-start", Matcher: m, Command: "x"}))
		ok(t)(RemoveHooks([]string{"x"}))
	}
	ok(t)(SaveHook("", Hook{Name: "x", Event: "after-tool", Matcher: "shell", Command: "x"}))
}
