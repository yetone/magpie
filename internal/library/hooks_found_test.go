package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func foundHooks(t *testing.T) []FoundHook {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v.FoundHooks
}

func foundBy(fs []FoundHook, agent, cmd string) *FoundHook {
	for i := range fs {
		if fs[i].Agent == agent && strings.Join(fs[i].Commands, "\n") == cmd {
			return &fs[i]
		}
	}
	return nil
}

// keptCopies is the copies of a file named base in magpie's backups.
func keptCopies(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(BackupDir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == base {
			b, _ := os.ReadFile(p)
			out = append(out, string(b))
		}
		return nil
	})
	return out
}

// The hooks an agent already runs that magpie didn't give it are listed
// (@InkCoalW on X), each as what it runs and where; the ones magpie wrote
// aren't, nor are RTK's (the RTK tab's), and one the library can't keep as
// written is said so, to be removed only.
func TestFoundHooksListed(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	write(t, settings, `{
  // the user's own
  "model": "opus",
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "say done", "timeout": 5}]},
      {"hooks": [{"type": "command", "command": "bg-job", "async": true}]}
    ],
    "PostToolUse": [
      {"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "prettier --write ."}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "a"}, {"type": "command", "command": "b"}]}
    ],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "rtk hook claude"}]}],
    "Notification": [{"hooks": [{"type": "prompt", "prompt": "is it done?"}]}]
  }
}
`)
	write(t, filepath.Join(h, ".codex/hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/bin/notify-me.sh --loud"}]}]}}`)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: []string{"claude"}}))

	fs := foundHooks(t)
	if f := foundBy(fs, "claude", "bark"); f != nil {
		t.Errorf("the hook magpie wrote is listed as found: %+v", f)
	}
	say := foundBy(fs, "claude", "say done")
	if say == nil || say.Event != "Stop" || say.Timeout != 5 || say.File != settings || say.Hook == nil ||
		say.Hook.Event != "turn-end" || say.Hook.Name != "say" || say.Hook.Timeout != 5 {
		t.Fatalf("say done: %+v (%+v)", say, fs)
	}
	if f := foundBy(fs, "claude", "prettier --write ."); f == nil || f.Hook == nil || f.Hook.Event != "after-tool" || f.Hook.Matcher != "edit" {
		t.Errorf("prettier, a portable after-tool on edit: %+v", f)
	}
	if f := foundBy(fs, "claude", "a\nb"); f == nil || f.Hook != nil || f.Why != "several" || f.Matcher != "Bash" {
		t.Errorf("a group of two commands: %+v", f)
	}
	// async isn't a thing a library hook has: brought in, the next write
	// would drop it
	if f := foundBy(fs, "claude", "bg-job"); f == nil || f.Hook != nil || f.Why != "shape" {
		t.Errorf("an async hook: %+v", f)
	}
	if f := foundBy(fs, "claude", "(prompt)"); f == nil || f.Hook != nil || f.Why != "type" {
		t.Errorf("a prompt hook: %+v", f)
	}
	if f := foundBy(fs, "claude", "rtk hook claude"); f == nil || f.Own != "rtk" {
		t.Errorf("RTK's hook: %+v", f)
	}
	if f := foundBy(fs, "codex", "/usr/local/bin/notify-me.sh --loud"); f == nil || f.Hook == nil || f.Hook.Name != "notify-me" {
		t.Errorf("codex's: %+v", f)
	}
	keys := map[string]bool{}
	for _, f := range fs {
		if keys[f.Key] || f.Key == "" {
			t.Errorf("key %q twice or empty", f.Key)
		}
		keys[f.Key] = true
	}
}

// Brought into the library, a found hook is magpie's like one it wrote:
// the entry stays as it is, given to the other agents it lands in their
// files, and unticked it comes out of the file it was found in, the user's
// other hooks left. One the same as a library hook joins it.
func TestFoundHooksBroughtIn(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	mine := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}],"Notification":[{"hooks":[{"type":"command","command":"keep-me"}]}]}}`
	write(t, settings, mine)
	codexHooks := filepath.Join(h, ".codex/hooks.json")
	write(t, codexHooks, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`)

	fs := foundHooks(t)
	cl, cx := foundBy(fs, "claude", "say done"), foundBy(fs, "codex", "say done")
	if cl == nil || cx == nil {
		t.Fatalf("found: %+v", fs)
	}
	ok(t)(ImportHooks([]string{cl.Key, cx.Key}))
	if got := read(t, settings); got != mine {
		t.Errorf("bringing it in changed the file:\n%s", got)
	}
	v, _ := Read(nil)
	if len(v.Hooks) != 1 || v.Hooks[0].Name != "say" || v.Hooks[0].Event != "turn-end" || strings.Join(v.Hooks[0].Agents, ",") != "claude,codex" {
		t.Fatalf("library hooks: %+v", v.Hooks)
	}
	if f := foundBy(v.FoundHooks, "claude", "say done"); f != nil {
		t.Errorf("still found after it was brought in: %+v", f)
	}
	if f := foundBy(v.FoundHooks, "claude", "keep-me"); f == nil {
		t.Errorf("the one not brought in is no longer found")
	}
	// given to Gemini too, it lands there
	ok(t)(HookAgents("say", []string{"claude", "codex", "gemini"}))
	if got := hooksOf(t, filepath.Join(h, ".gemini/settings.json"), "AfterAgent"); len(got) != 1 || !strings.Contains(got[0], "say done") {
		t.Errorf("gemini AfterAgent: %v", got)
	}
	// unticked for Claude, it comes out of the file it was found in alone
	ok(t)(HookAgents("say", []string{"codex", "gemini"}))
	if got := hooksOf(t, settings, "Stop"); len(got) != 0 {
		t.Errorf("claude Stop after unticking: %v", got)
	}
	if got := hooksOf(t, settings, "Notification"); len(got) != 1 {
		t.Errorf("the user's other hook went: %v", got)
	}
}

// Removed, a found hook comes out of its agent's file and nothing else
// does: the user's other hooks, their comments and settings stay, the
// file is copied to the backups first, and Codex's trust of the hooks
// after it moves with them.
func TestFoundHooksRemoved(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	before := `{
  // the user's own
  "model": "opus",
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "old-thing"}]},
      {"hooks": [{"type": "command", "command": "say done"}]}
    ]
  }
}
`
	write(t, settings, before)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: []string{"claude", "codex"}}))
	codexHooks := filepath.Join(h, ".codex/hooks.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	// the user's two of their own, the first to go, after magpie's: the
	// second's trust moves to the first's place
	write(t, codexHooks, `{"hooks":{"Stop":[{"hooks":[{"command":"bark","type":"command"}]},{"hooks":[{"type":"command","command":"gone-soon"}]},{"hooks":[{"type":"command","command":"mine"}]}]}}`)
	key := func(g int) string { return codexKey(codexHooks + ":stop:" + string(rune('0'+g)) + ":0") }
	write(t, cfg, "model = \"gpt-5\"\n\n[hooks.state]\n\n[hooks.state."+key(1)+"]\ntrusted_hash = \"sha256:aaa\"\n\n[hooks.state."+key(2)+"]\ntrusted_hash = \"sha256:bbb\"\n")

	fs := foundHooks(t)
	old, gone := foundBy(fs, "claude", "old-thing"), foundBy(fs, "codex", "gone-soon")
	if old == nil || gone == nil {
		t.Fatalf("found: %+v", fs)
	}
	ok(t)(RemoveFoundHooks([]string{old.Key, gone.Key}))

	got := read(t, settings)
	if !strings.Contains(got, "// the user's own") || !strings.Contains(got, `"model": "opus"`) || strings.Contains(got, "old-thing") || !strings.Contains(got, "say done") {
		t.Errorf("claude's settings after:\n%s", got)
	}
	if s := hooksOf(t, settings, "Stop"); len(s) != 2 || !strings.Contains(s[1], `"bark"`) {
		t.Errorf("claude Stop after (say done, then magpie's bark): %v", s)
	}
	if b := keptCopies(t, "settings.json"); len(b) == 0 || !strings.Contains(strings.Join(b, ""), "old-thing") {
		t.Errorf("claude's settings weren't backed up first: %q", b)
	}
	if s := hooksOf(t, codexHooks, "Stop"); len(s) != 2 || !strings.Contains(s[0], "bark") || !strings.Contains(s[1], "mine") {
		t.Errorf("codex Stop after: %v", s)
	}
	if b := keptCopies(t, "hooks.json"); len(b) == 0 || !strings.Contains(strings.Join(b, ""), "gone-soon") {
		t.Errorf("codex's hooks weren't backed up first: %q", b)
	}
	if c, want := read(t, cfg), "model = \"gpt-5\"\n\n[hooks.state]\n\n[hooks.state."+key(1)+"]\ntrusted_hash = \"sha256:bbb\"\n"; c != want {
		t.Errorf("codex config.toml after:\n%s\nwant:\n%s", c, want)
	}
	// magpie's own hook is magpie's still: unticking it takes it out
	ok(t)(RemoveHooks([]string{"bark"}))
	if s := hooksOf(t, settings, "Stop"); len(s) != 1 || !strings.Contains(s[0], "say done") {
		t.Errorf("claude Stop after bark went: %v", s)
	}
	// a key the file no longer has is said, not taken as done
	if _, err := RemoveFoundHooks([]string{old.Key}); err == nil {
		t.Errorf("a found hook gone already was removed again without a word")
	}
}

// Of two entries the same, removing one leaves the other; a Grok block
// comes out of its config.toml alone.
func TestFoundHooksTwinsAndGrok(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	write(t, settings, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"twin"}]},{"hooks":[{"type":"command","command":"twin"}]}]}}`)
	build := filepath.Join(h, ".grok/config.toml")
	write(t, build, "model = \"grok-4.7\"\n\n[[hooks.Stop]]\n  [[hooks.Stop.hooks]]\n  type = \"command\"\n  command = \"say grok done\"\n\n[[hooks.Stop]]\n  [[hooks.Stop.hooks]]\n  type = \"command\"\n  command = \"stays\"\n")

	fs := foundHooks(t)
	var twins []FoundHook
	for _, f := range fs {
		if f.Agent == "claude" {
			twins = append(twins, f)
		}
	}
	if len(twins) != 2 || twins[0].Key == twins[1].Key {
		t.Fatalf("twins: %+v", twins)
	}
	g := foundBy(fs, "grok", "say grok done")
	if g == nil || g.Hook == nil || g.Hook.Event != "turn-end" {
		t.Fatalf("grok: %+v", fs)
	}
	ok(t)(RemoveFoundHooks([]string{twins[1].Key, g.Key}))
	if s := hooksOf(t, settings, "Stop"); len(s) != 1 {
		t.Errorf("claude Stop after one twin went: %v", s)
	}
	if b := grokBlocks(t, build, "Stop"); len(b) != 1 || !strings.Contains(b[0], "stays") {
		t.Errorf("grok Stop after: %q", b)
	}
	if s := read(t, build); !strings.HasPrefix(s, "model = \"grok-4.7\"\n") {
		t.Errorf("grok config after:\n%s", s)
	}
}
