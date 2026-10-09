package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// home is a sandbox: a home of its own with Claude Code and Codex in it,
// nothing on PATH, and two providers.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
	}
	t.Setenv("APPDATA", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	for _, f := range []string{".claude/settings.json", ".codex/config.toml"} {
		write(t, filepath.Join(h, f), "")
	}
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m", "claude-opus-5-5"}, Chat: "http://127.0.0.1:1/v1"},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"gpt-5.5"}, Chat: "http://127.0.0.1:1/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// press sends keys one by one, each followed by what its command says, as
// the program would.
func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(keyMsg(k))
		m = next.(model)
		for i := 0; cmd != nil && i < 5; i++ {
			msg := cmd()
			if msg == nil {
				break
			}
			next, cmd = m.Update(msg)
			m = next.(model)
		}
	}
	return m
}

// typeIn types into the line or filter open; the cursor's blinking is
// left alone.
func typeIn(m model, s string) model {
	next, _ := m.Update(keyMsg(s))
	return next.(model)
}

func group(t *testing.T, id string) provider.Group {
	t.Helper()
	g, ok := findGroup(id)
	if !ok {
		t.Fatalf("no group %s", id)
	}
	return g
}

func wantFlash(t *testing.T, m model, ok bool, has string) {
	t.Helper()
	if m.flashOK != ok || !strings.Contains(m.flash, has) {
		t.Fatalf("flash %q (ok %v), want %q (ok %v)", m.flash, m.flashOK, has, ok)
	}
}

func TestRoutingPage(t *testing.T) {
	home(t)
	m := press(t, model{w: 120, h: 40}, "3")
	if m.page != pageGroups || !strings.Contains(m.View(), "no routing groups yet") {
		t.Fatalf("page %d:\n%s", m.page, m.View())
	}

	// a new group, its first model, then a second
	m = press(t, m, "n")
	m = typeIn(m, "Opus")
	m = press(t, m, "enter")
	m = typeIn(m, "a/claude-opus-5-5")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "made group Opus")
	m = press(t, m, "enter", "a")
	m = typeIn(m, "b/gpt-5.5")
	m = press(t, m, "enter")
	if g := group(t, "opus"); !slices.Equal(g.Members, []string{"a/claude-opus-5-5", "b/gpt-5.5"}) || m.mode != modeGroup {
		t.Fatalf("members %v, mode %d", g.Members, m.mode)
	}

	// rules, as magpie group rule add takes them
	m = press(t, m, "n")
	m = typeIn(m, "use=gpt-5.5 tokens=200k")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "rule added")
	m = press(t, m, "n")
	m = typeIn(m, `use=claude-opus-5-5 intent="a quick question"`)
	m = press(t, m, "enter")
	wantFlash(t, m, false, "classifier")
	m = press(t, m, "n")
	m = typeIn(m, `use=claude-opus-5-5 intent="a quick question" classifier=a/m at=1`)
	m = press(t, m, "enter")
	g := group(t, "opus")
	if len(g.Rules) != 2 || g.Rules[0].Intent != "a quick question" || g.Rules[1].Tokens != 200000 || g.Classifier != "a/m" {
		t.Fatalf("rules %+v classifier %q", g.Rules, g.Classifier)
	}
	if v := m.View(); !strings.Contains(v, `intent "a quick question"`) || !strings.Contains(v, "classifier a/m") {
		t.Fatalf("the rules aren't shown:\n%s", v)
	}

	// the second rule picked: up above the first, then typed again
	m = press(t, m, "down", "down", "down")
	if m.gsel != 3 {
		t.Fatalf("picked %d", m.gsel)
	}
	m = press(t, m, "K")
	if g := group(t, "opus"); g.Rules[0].Tokens != 200000 || m.gsel != 2 {
		t.Fatalf("not moved: %+v, picked %d", g.Rules, m.gsel)
	}
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Value() != "use=b/gpt-5.5 tokens=200000" {
		t.Fatalf("editing %q", m.ask.input.Value())
	}
	m = press(t, m, "ctrl+u")
	m = typeIn(m, "use=gpt-5.5 images")
	m = press(t, m, "enter")
	if g := group(t, "opus"); len(g.Rules) != 2 || !g.Rules[0].Images || g.Rules[0].Tokens != 0 {
		t.Fatalf("not changed in place: %+v", g.Rules)
	}

	// the classifier, routing, how long a conversation stays, the context
	m = press(t, m, "c")
	m = typeIn(m, "b/gpt-5.5")
	m = press(t, m, "enter", "o", "s", "x")
	m = typeIn(m, "300k")
	m = press(t, m, "enter")
	g = group(t, "opus")
	if g.Classifier != "b/gpt-5.5" || g.Routing != provider.Ordered || g.Affinity != provider.AffinitySession || g.Context != 300000 {
		t.Fatalf("group %+v", g)
	}
	// the levels agents are offered (#295)
	m = press(t, m, "l")
	m = typeIn(m, "xhigh,medium")
	m = press(t, m, "enter")
	if g := group(t, "opus"); !slices.Equal(g.Levels, []string{"medium", "xhigh"}) {
		t.Fatalf("levels %v", g.Levels)
	}

	// d twice removes the rule picked; a model taken out takes its rules
	m = press(t, m, "d")
	if len(group(t, "opus").Rules) != 2 {
		t.Fatal("one d removed it")
	}
	m = press(t, m, "d")
	if g := group(t, "opus"); len(g.Rules) != 1 || g.Rules[0].Use != "a/claude-opus-5-5" {
		t.Fatalf("rules %+v", g.Rules)
	}
	m = press(t, m, "up", "up", "d", "d")
	if g := group(t, "opus"); len(g.Rules) != 0 || !slices.Equal(g.Members, []string{"b/gpt-5.5"}) {
		t.Fatalf("group %+v", g)
	}

	// renamed, the page follows it
	m = press(t, m, "R", "ctrl+u")
	m = typeIn(m, "fast")
	m = press(t, m, "enter")
	if m.gid != "fast" || m.mode != modeGroup {
		t.Fatalf("open %q, mode %d, flash %q", m.gid, m.mode, m.flash)
	}
	group(t, "fast")

	// and removed from the list
	m = press(t, m, "esc", "d", "d")
	if _, ok := findGroup("fast"); ok {
		t.Fatalf("not removed: %q", m.flash)
	}
}

func TestPutRule(t *testing.T) {
	home(t)
	g := provider.Group{ID: "g", Members: []string{"a/m", "b/gpt-5.5"}}
	for _, line := range []string{"use=m tokens=1k", "use=gpt-5.5 images", "use=m effort=high at=1"} {
		if err := putRule(&g, -1, line); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.Rules) != 3 || g.Rules[0].Effort != "high" || g.Rules[2].Images != true {
		t.Fatalf("%+v", g.Rules)
	}
	// typed again, a rule stays where it is unless moved
	if err := putRule(&g, 1, g.Rules[1].Line()+" agents=codex"); err != nil {
		t.Fatal(err)
	}
	if g.Rules[1].Tokens != 1000 || !slices.Equal(g.Rules[1].Agents, []string{"codex"}) {
		t.Fatalf("%+v", g.Rules)
	}
	if err := putRule(&g, -1, "tokens=1k"); err == nil {
		t.Fatal("a rule without its model")
	}
}

func TestLibraryPage(t *testing.T) {
	h := home(t)
	m := press(t, model{w: 120, h: 40}, "6")
	if m.page != pageLibrary || m.libErr != "" {
		t.Fatalf("page %d: %s", m.page, m.libErr)
	}
	row := func(kind, name string) int {
		t.Helper()
		i := slices.IndexFunc(m.lib, func(r libRow) bool { return r.kind == kind && r.name == name })
		if i < 0 {
			t.Fatalf("no %s %s: %+v", kind, name, m.lib)
		}
		return i
	}

	// a server by URL
	m = press(t, m, "a", "enter")
	m = typeIn(m, "ctx https://example.com/mcp")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "added ctx")

	// given to Claude Code, from the list of agents
	m.lrow = row("mcp", "ctx")
	m = press(t, m, "enter")
	m = typeIn(m, "claude")
	m = press(t, m, "enter", "esc")
	servers := func() []library.ServerView {
		v, err := library.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		return v.Servers
	}
	if s := servers(); len(s) != 1 || !slices.Equal(s[0].Agents, []string{"claude"}) {
		t.Fatalf("servers %+v, flash %q", s, m.flash)
	}
	if !strings.Contains(m.View(), "Claude Code") {
		t.Fatalf("its agents aren't shown:\n%s", m.View())
	}

	// its URL changed, its agents kept
	m.lrow = row("mcp", "ctx")
	m = press(t, m, "e", "ctrl+u")
	m = typeIn(m, "ctx https://example.com/v2")
	m = press(t, m, "enter")
	if s := servers(); len(s) != 1 || s[0].URL != "https://example.com/v2" || !slices.Equal(s[0].Agents, []string{"claude"}) {
		t.Fatalf("servers %+v, flash %q", s[0].Server, m.flash)
	}
	m = press(t, m, "s")
	wantFlash(t, m, true, "synced")

	// skills from a folder
	src := filepath.Join(h, "src")
	write(t, filepath.Join(src, "tidy/SKILL.md"), "---\nname: tidy\ndescription: Tidies up\n---\n")
	m = press(t, m, "a", "down", "enter")
	m = typeIn(m, src)
	m = press(t, m, "enter")
	if m.mode != modePick || len(m.pk.items) != 1 || m.pk.items[0].Value != "tidy" {
		t.Fatalf("mode %d items %+v flash %q", m.mode, m.pk.items, m.flash)
	}
	m = press(t, m, "enter")
	wantFlash(t, m, true, "added the skill")
	row("skill", "tidy")

	// d twice takes the server out
	m.lrow = row("mcp", "ctx")
	m = press(t, m, "d", "d")
	if s := servers(); len(s) != 0 {
		t.Fatalf("servers %+v", s)
	}

	// a skill of Claude Code's own: d twice takes it out of the agent
	// without bringing it in (#1303)
	old := filepath.Join(h, ".claude/skills/old")
	write(t, filepath.Join(old, "SKILL.md"), "---\nname: old\ndescription: Not wanted\n---\n")
	m = press(t, m, "s")
	m.lrow = row("found-skill", "old")
	m = press(t, m, "d")
	if _, err := os.Stat(old); err != nil {
		t.Fatal("one d removed it")
	}
	m = press(t, m, "d")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("still there, flash %q", m.flash)
	}
	if slices.ContainsFunc(m.lib, func(r libRow) bool { return r.name == "old" }) {
		t.Fatal("still listed")
	}
}

// A first run has no model catalog: Init syncs it, the status line says so
// until it is done, and S doesn't start a second sync meanwhile.
func TestFirstRunShowsCatalogSync(t *testing.T) {
	home(t)
	m := newModel()
	m.w, m.h = 120, 40
	if m.Init() == nil {
		t.Fatal("no catalog, and Init didn't sync it")
	}
	if !m.syncing || !strings.Contains(m.View(), "syncing model catalog") {
		t.Fatalf("syncing %v:\n%s", m.syncing, m.View())
	}
	if _, cmd := m.Update(keyMsg("S")); cmd != nil {
		t.Fatal("S started a second sync while the first runs")
	}
}
