package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func tuneSandbox(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, k := range []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "FORCE_PROMPT_CACHING_5M", "CLAUDE_CODE_PROMPT_CACHE_TTL", "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL"} {
		t.Setenv(k, "")
	}
	return home
}

func tuneWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tuneFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTuneClaudeCompact: the advised window goes where /autocompact keeps
// it, under modelSettings for the model, the user's other settings kept;
// Undo takes it out again, empty entries with it.
func TestTuneClaudeCompact(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	tuneWrite(t, path, `{"theme": "dark", "autoCompactWindow": 300000}`)
	s, err := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5")
	if err != nil || s.Value != "300000" || s.Source != "settings" || s.Ours {
		t.Fatalf("read %+v %v: want the top-level 300000", s, err)
	}
	if err := TuneApply(home, "claude", TuneCompact, "claude-opus-5-5", "160000"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(tuneFile(t, path)), "")
	if !strings.Contains(got, `"claude-opus-5-5":{"autoCompactWindow":160000}`) || !strings.Contains(got, `"theme":"dark"`) || !strings.Contains(got, `"autoCompactWindow":300000`) {
		t.Fatalf("settings.json after apply:\n%s", got)
	}
	s, _ = TuneRead(home, "claude", TuneCompact, "claude-opus-5-5")
	if s.Value != "160000" || s.Source != "model" || !s.Ours {
		t.Fatalf("read after apply %+v", s)
	}
	if err := TuneUndo(home, "claude", TuneCompact, "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	got = tuneFile(t, path)
	if strings.Contains(got, "modelSettings") || !strings.Contains(got, "300000") {
		t.Fatalf("settings.json after undo:\n%s", got)
	}
	if err := TuneApply(home, "claude", TuneCompact, "claude-opus-5-5", "50000"); err == nil {
		t.Fatal("took a window under Claude Code's 100000")
	}
	if err := TuneApply(home, "claude", TuneCompact, "gpt-5.5", "160000"); err == nil {
		t.Fatal("took a window for a model that is no Claude model")
	}
}

// TestTuneUserValueKept: a TTL the user had is put back by Undo, and one
// they changed after Apply is theirs: Undo leaves it.
func TestTuneUserValueKept(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	tuneWrite(t, path, `{"promptCacheTtl": "5m"}`)
	if err := TuneApply(home, "claude", TuneCache, "", "1h"); err != nil {
		t.Fatal(err)
	}
	if err := TuneUndo(home, "claude", TuneCache, ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := TuneRead(home, "claude", TuneCache, ""); v.Value != "5m" || v.Ours {
		t.Fatalf("after undo %+v, want the user's 5m", v)
	}
	if err := TuneApply(home, "claude", TuneCache, "", "1h"); err != nil {
		t.Fatal(err)
	}
	tuneWrite(t, path, `{"promptCacheTtl": "5m", "other": 1}`)
	if err := TuneUndo(home, "claude", TuneCache, ""); err != nil {
		t.Fatal(err)
	}
	if got := tuneFile(t, path); !strings.Contains(got, `"5m"`) {
		t.Fatalf("undo wrote over the user's change:\n%s", got)
	}
	if err := TuneUndo(home, "claude", TuneCache, ""); err == nil {
		t.Fatal("undo with nothing of magpie's to put back")
	}
	if err := TuneApply(home, "claude", TuneCache, "", "2h"); err == nil {
		t.Fatal("took a lifetime Claude Code has no use for")
	}
}

// TestTuneLockedByEnv: a variable that comes before the setting is told
// of, and Apply changes nothing.
func TestTuneLockedByEnv(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL": "1h"}}`)
	s, _ := TuneRead(home, "claude", TuneSubCache, "")
	if s.Locked != "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL" || s.Value != "1h" {
		t.Fatalf("read %+v", s)
	}
	if err := TuneApply(home, "claude", TuneSubCache, "", "5m"); err == nil {
		t.Fatal("applied under a variable that takes precedence")
	}
	if got := tuneFile(t, path); strings.Contains(got, "subagentPromptCacheTtl") {
		t.Fatalf("wrote anyway:\n%s", got)
	}
	t.Setenv("FORCE_PROMPT_CACHING_5M", "1")
	if s, _ := TuneRead(home, "claude", TuneCache, ""); s.Locked != "FORCE_PROMPT_CACHING_5M" || s.Value != "5m" {
		t.Fatalf("read %+v", s)
	}
}

// TestTuneCodexCompact: where Codex compacts is Long conversations, one
// value with the agents' settings sheet (7eleven): the card reads it, Use
// sets it and writes no limit of tune's own beside it into config.toml,
// and Undo puts Long conversations back as it was.
func TestTuneCodexCompact(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".codex", "config.toml")
	tuneWrite(t, path, "model = \"gpt-5.5\"\n\n[features]\nx = true\n")
	if err := provider.SetCompactAt(200000); err != nil {
		t.Fatal(err)
	}
	s, err := TuneRead(home, "codex", TuneCompact, "")
	if err != nil || s.Value != "200000" || s.Source != TuneLong || s.Locked != "" || s.Ours {
		t.Fatalf("read %+v %v: want Long conversations' 200000", s, err)
	}
	if err := TuneApply(home, "codex", TuneCompact, "", "150000"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 150000 || got.FullContext {
		t.Fatalf("Long conversations after apply: %d full=%v, want 150000", got.CompactAt, got.FullContext)
	}
	if got := tuneFile(t, path); strings.Contains(got, codexCompactKey) || !strings.Contains(got, "x = true") {
		t.Fatalf("config.toml after apply:\n%s", got)
	}
	s, _ = TuneRead(home, "codex", TuneCompact, "")
	if s.Value != "150000" || s.Source != TuneLong || !s.Ours {
		t.Fatalf("read after apply %+v", s)
	}
	// the Claude Code card reads the same value where magpie's window is
	// what Claude Code runs at
	if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 200000 {
		t.Fatalf("Long conversations after undo: %d, want 200000", got.CompactAt)
	}
	// Full window comes back as Full window
	if err := provider.SetFullContext(true); err != nil {
		t.Fatal(err)
	}
	if s, _ := TuneRead(home, "codex", TuneCompact, ""); s.Value != "" || s.Source != TuneLong {
		t.Fatalf("read under Full window %+v", s)
	}
	if err := TuneApply(home, "codex", TuneCompact, "", "400000"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 400000 || got.FullContext {
		t.Fatalf("after apply under Full window: %+v", got.CompactAt)
	}
	if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); !got.FullContext {
		t.Fatal("undo didn't put Full window back")
	}
	// a value the user set in the sheet after Use is theirs: Undo leaves it
	if err := TuneApply(home, "codex", TuneCompact, "", "300000"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetCompactAt(250000); err != nil {
		t.Fatal(err)
	}
	if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 250000 || got.FullContext {
		t.Fatalf("undo wrote over the user's 250000: %d full=%v", got.CompactAt, got.FullContext)
	}
}

// TestTuneCodexOwnLimit: a model_auto_compact_token_limit the user wrote
// in config.toml stops Codex before magpie's window, so the card says so
// and Use changes nothing.
func TestTuneCodexOwnLimit(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".codex", "config.toml")
	tuneWrite(t, path, "model_auto_compact_token_limit = 180000\n")
	s, _ := TuneRead(home, "codex", TuneCompact, "")
	if s.Locked != codexCompactKey || s.Value != "180000" {
		t.Fatalf("read %+v", s)
	}
	if err := TuneApply(home, "codex", TuneCompact, "", "150000"); err == nil {
		t.Fatal("applied under the user's own limit")
	}
	if got := tuneFile(t, path); !strings.Contains(got, "= 180000") {
		t.Fatalf("config.toml:\n%s", got)
	}
	if got := settings.Load(); got.CompactAt != 0 {
		t.Fatalf("Long conversations changed to %d", got.CompactAt)
	}
}

// TestTuneCodexMigrate: a limit an older magpie's tune wrote into
// config.toml goes into Long conversations and the file gets back what
// it had; one the user set in the sheet is kept unless tune's was lower,
// and a limit the user changed since is theirs and stays.
func TestTuneCodexMigrate(t *testing.T) {
	old := func(t *testing.T, home, file, set string, had bool, raw string) string {
		path := filepath.Join(home, ".codex", "config.toml")
		tuneWrite(t, path, file)
		b, _ := json.Marshal(tuneStash{Had: had, Raw: raw, Set: set})
		stash(map[string]string{tuneKey("codex", TuneCompact, ""): string(b)})
		return path
	}
	t.Run("default takes tune's", func(t *testing.T) {
		home := tuneSandbox(t)
		path := old(t, home, "model = \"gpt-5.5\"\nmodel_auto_compact_token_limit = 150000\n", "150000", false, "")
		s, _ := TuneRead(home, "codex", TuneCompact, "")
		if s.Value != "150000" || s.Source != TuneLong || !s.Ours {
			t.Fatalf("read %+v", s)
		}
		if got := settings.Load(); got.CompactAt != 150000 {
			t.Fatalf("Long conversations %d, want tune's 150000", got.CompactAt)
		}
		if got := tuneFile(t, path); strings.Contains(got, codexCompactKey) || !strings.Contains(got, "gpt-5.5") {
			t.Fatalf("config.toml:\n%s", got)
		}
		if _, ok := tuneStashed("codex", TuneCompact, ""); ok {
			t.Fatal("the old stash stayed")
		}
		// Undo still goes back to before tune
		if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 0 || got.FullContext {
			t.Fatalf("after undo %d", got.CompactAt)
		}
	})
	t.Run("user's lower value kept", func(t *testing.T) {
		home := tuneSandbox(t)
		if err := provider.SetCompactAt(120000); err != nil {
			t.Fatal(err)
		}
		path := old(t, home, "model_auto_compact_token_limit = 150000\n", "150000", true, "200000")
		TuneRead(home, "codex", TuneCompact, "")
		if got := settings.Load(); got.CompactAt != 120000 {
			t.Fatalf("Long conversations %d, want the user's 120000", got.CompactAt)
		}
		if got := tuneFile(t, path); !strings.Contains(got, "= 200000") {
			t.Fatalf("config.toml didn't get the user's limit back:\n%s", got)
		}
	})
	t.Run("user's higher value gives way to where Codex compacted", func(t *testing.T) {
		home := tuneSandbox(t)
		if err := provider.SetCompactAt(400000); err != nil {
			t.Fatal(err)
		}
		old(t, home, "model_auto_compact_token_limit = 150000\n", "150000", false, "")
		TuneRead(home, "codex", TuneCompact, "")
		if got := settings.Load(); got.CompactAt != 150000 {
			t.Fatalf("Long conversations %d, want 150000", got.CompactAt)
		}
	})
	t.Run("limit the user changed stays", func(t *testing.T) {
		home := tuneSandbox(t)
		path := old(t, home, "model_auto_compact_token_limit = 170000\n", "150000", false, "")
		s, _ := TuneRead(home, "codex", TuneCompact, "")
		if s.Locked != codexCompactKey || s.Value != "170000" {
			t.Fatalf("read %+v", s)
		}
		if got := settings.Load(); got.CompactAt != 0 {
			t.Fatalf("Long conversations %d", got.CompactAt)
		}
		if got := tuneFile(t, path); !strings.Contains(got, "= 170000") {
			t.Fatalf("config.toml:\n%s", got)
		}
	})
}

// TestTuneClaudeLong: the window magpie wrote into Claude Code's env from
// Long conversations is that value, not a variable that locks the card:
// Use sets Long conversations; a window the user wrote there, or one
// from a provider's Compact at, still locks it.
func TestTuneClaudeLong(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "272000"}}`)
	stash(map[string]string{"claude.compact_window": "272000", "claude.compact_long": "1"})
	s, err := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5")
	if err != nil || s.Value != "272000" || s.Source != TuneLong || s.Locked != "" {
		t.Fatalf("read %+v %v: want Long conversations' 272000", s, err)
	}
	if err := TuneApply(home, "claude", TuneCompact, "claude-opus-5-5", "50000"); err == nil {
		t.Fatal("took a window under Claude Code's 100000")
	}
	if err := TuneApply(home, "claude", TuneCompact, "claude-opus-5-5", "400000"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 400000 {
		t.Fatalf("Long conversations %d, want 400000", got.CompactAt)
	}
	if got := tuneFile(t, path); strings.Contains(got, "modelSettings") {
		t.Fatalf("wrote a model's own window beside Long conversations:\n%s", got)
	}
	// before Sync writes the env again the card says the new value, not
	// the old one as the user's
	if s, _ := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5"); s.Value != "400000" || s.Source != TuneLong || s.Locked != "" {
		t.Fatalf("read before Sync %+v", s)
	}
	// Sync writes the env again; then the Codex card shows the same
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "400000"}}`)
	stash(map[string]string{"claude.compact_window": "400000"})
	if s, _ := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5"); s.Value != "400000" || s.Source != TuneLong || !s.Ours {
		t.Fatalf("read after apply %+v", s)
	}
	if s, _ := TuneRead(home, "codex", TuneCompact, ""); s.Value != "400000" || s.Source != TuneLong || !s.Ours {
		t.Fatalf("codex read after the Claude card's apply %+v", s)
	}
	if err := TuneUndo(home, "claude", TuneCompact, "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CompactAt != 0 {
		t.Fatalf("after undo %d", got.CompactAt)
	}
	// the user's own env value
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "272000"}}`)
	forget("claude.compact_window", "claude.compact_long")
	if s, _ := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5"); s.Locked != "CLAUDE_CODE_AUTO_COMPACT_WINDOW" {
		t.Fatalf("the user's env read %+v", s)
	}
	// magpie's, from a provider's Compact at, not Long conversations
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "150000"}}`)
	stash(map[string]string{"claude.compact_window": "150000"})
	forget("claude.compact_long")
	if s, _ := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5"); s.Locked != "CLAUDE_CODE_AUTO_COMPACT_WINDOW" {
		t.Fatalf("a provider's window read %+v", s)
	}
}

// TestTuneCodexMigrateOnSync: the move happens when magpie writes Codex's
// config, without the Usage page opened, so Codex runs at the one window.
func TestTuneCodexMigrateOnSync(t *testing.T) {
	home, read := codexHome(t, "", "model_auto_compact_token_limit = 150000\n")
	b, _ := json.Marshal(tuneStash{Set: "150000"})
	stash(map[string]string{tuneKey("codex", TuneCompact, ""): string(b)})
	if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, codexCompactKey) || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("config.toml after a sync:\n%s", cfg)
	}
	if got := settings.Load(); got.CompactAt != 150000 {
		t.Fatalf("Long conversations %d, want tune's 150000", got.CompactAt)
	}
}

// TestTuneCodexAsync: the advice goes into Background compaction as a
// share of the window; the foreground limit, the user's own in
// config.toml or Long conversations, stays where it is (herman_hong's 384K
// backstop), and Undo puts Background compaction back as it was.
func TestTuneCodexAsync(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".codex", "config.toml")
	tuneWrite(t, path, "model_auto_compact_token_limit = 384000\n")
	if err := provider.SetCompactAt(200000); err != nil {
		t.Fatal(err)
	}
	setAsyncCompact(t, false, 0)
	if s := asyncRead(home); s.Value != "" || s.Locked != "" || s.Ours || s.Source != TuneAsyncSource {
		t.Fatalf("read %+v: want off", s)
	}
	for _, bad := range []string{"0", "101", "120000", "x"} {
		if err := TuneApply(home, "codex", TuneAsync, "", bad); err == nil {
			t.Fatalf("applied %q", bad)
		}
	}
	if err := TuneApply(home, "codex", TuneAsync, "", "30"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); !got.CodexAsyncCompact || got.CodexAsyncCompactPercent != 30 || got.CompactAt != 200000 {
		t.Fatalf("after apply: async=%v %d%% compactAt=%d", got.CodexAsyncCompact, got.CodexAsyncCompactPercent, got.CompactAt)
	}
	if got := tuneFile(t, path); !strings.Contains(got, "model_auto_compact_token_limit = 384000") {
		t.Fatalf("the foreground limit moved:\n%s", got)
	}
	if s, _ := TuneRead(home, "codex", TuneAsync, ""); s.Value != "30" || !s.Ours {
		t.Fatalf("read after apply %+v", s)
	}
	// applying again keeps what was there before tune
	if err := TuneApply(home, "codex", TuneAsync, "", "25"); err != nil {
		t.Fatal(err)
	}
	if err := TuneUndo(home, "codex", TuneAsync, ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.CodexAsyncCompact || got.CodexAsyncCompactPercent != 0 {
		t.Fatalf("after undo: async=%v %d%%, want off", got.CodexAsyncCompact, got.CodexAsyncCompactPercent)
	}
	if err := TuneUndo(home, "codex", TuneAsync, ""); err == nil {
		t.Fatal("undid twice")
	}
	// Codex's own 65 reads as 65; a share the user picks in Settings after
	// Use is theirs, and Undo leaves it
	setAsyncCompact(t, true, 0)
	if s := asyncRead(home); s.Value != "65" || s.Ours {
		t.Fatalf("read at Codex's own %+v", s)
	}
	if err := TuneApply(home, "codex", TuneAsync, "", "30"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetCodexAsyncCompact(true, 50); err != nil {
		t.Fatal(err)
	}
	if s := asyncRead(home); s.Value != "50" || s.Ours {
		t.Fatalf("read after the user's 50 %+v", s)
	}
	if err := TuneUndo(home, "codex", TuneAsync, ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); !got.CodexAsyncCompact || got.CodexAsyncCompactPercent != 50 {
		t.Fatalf("undo wrote over the user's 50: %v %d", got.CodexAsyncCompact, got.CodexAsyncCompactPercent)
	}
}

// TestTuneCodexAsyncOwn: background compaction the user set in Codex's
// config.toml comes before magpie's, so the card says so and Use changes
// nothing; a table magpie wrote itself is not the user's.
func TestTuneCodexAsyncOwn(t *testing.T) {
	for _, c := range []struct{ name, file, key string }{
		{"own share", "[features.async_compaction]\nenabled = true\nmodel_async_compact_threshold_percent = 80\n", codexAsyncTable + "." + codexAsyncPercent},
		{"turned off", "[features.async_compaction]\nenabled = false\n", codexAsyncTable + ".enabled"},
		{"bare off", "[features]\nasync_compaction = false\n", "features.async_compaction"},
		{"bare on", "[features]\nasync_compaction = true\n", "features.async_compaction"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := tuneSandbox(t)
			path := filepath.Join(home, ".codex", "config.toml")
			tuneWrite(t, path, c.file)
			setAsyncCompact(t, false, 0)
			if s := asyncRead(home); s.Locked != c.key || s.Value != "" {
				t.Fatalf("read %+v, want locked by %s", s, c.key)
			}
			if err := TuneApply(home, "codex", TuneAsync, "", "30"); err == nil {
				t.Fatal("applied under the user's own")
			}
			if got := settings.Load(); got.CodexAsyncCompact {
				t.Fatal("Background compaction turned on")
			}
			if got := tuneFile(t, path); got != c.file {
				t.Fatalf("config.toml changed:\n%s", got)
			}
		})
	}
	t.Run("magpie's own table", func(t *testing.T) {
		home := tuneSandbox(t)
		path := filepath.Join(home, ".codex", "config.toml")
		tuneWrite(t, path, "[features.async_compaction]\nenabled = true\nmodel_async_compact_threshold_percent = 40\n")
		b, _ := json.Marshal(codexAsyncStash{Enabled: true, Percent: 40})
		stash(map[string]string{here(tuneHome(home)).key("codex.async"): string(b)})
		setAsyncCompact(t, true, 40)
		if s := asyncRead(home); s.Locked != "" || s.Value != "40" {
			t.Fatalf("read %+v: magpie's own table counted as the user's", s)
		}
	})
}

// TestTuneAgentOwnLong: where an agent has its own Long conversations
// (molianyin4227), its Usage card reads and sets that one, every other
// agent's left as it was, and Undo puts the agent's own back.
func TestTuneAgentOwnLong(t *testing.T) {
	home := tuneSandbox(t)
	tuneWrite(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5.5\"\n")
	if err := provider.SetCompactAt(200000); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAgentCompact("codex", &settings.AgentCompact{Full: true}); err != nil {
		t.Fatal(err)
	}
	if s, _ := TuneRead(home, "codex", TuneCompact, ""); s.Value != "" || s.Source != TuneLong {
		t.Fatalf("read codex's own full window %+v", s)
	}
	if err := TuneApply(home, "codex", TuneCompact, "", "350000"); err != nil {
		t.Fatal(err)
	}
	got := settings.Load()
	if c := got.AgentCompacts["codex"]; c.At != 350000 || c.Full || got.CompactAt != 200000 || got.FullContext {
		t.Fatalf("after apply: codex %+v, every agent's %d full=%v", c, got.CompactAt, got.FullContext)
	}
	if s, _ := TuneRead(home, "codex", TuneCompact, ""); s.Value != "350000" || !s.Ours {
		t.Fatalf("read after apply %+v", s)
	}
	if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
		t.Fatal(err)
	}
	got = settings.Load()
	if c, ok := got.AgentCompacts["codex"]; !ok || !c.Full || got.CompactAt != 200000 {
		t.Fatalf("after undo: codex %+v %v, every agent's %d", c, ok, got.CompactAt)
	}
}
