package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// TestTuneCodexTakeOver: a model_auto_compact_token_limit of the user's
// locks the Codex card (sufeng1894 on X), and the card's one action lets
// magpie set it: the limit moves into Long conversations (the rule
// tuneMigrateCodex moves tune's own by) and out of config.toml, the card
// adjusts it from then on, and Undo puts both back. Nothing of the user's
// moves without that say.
func TestTuneCodexTakeOver(t *testing.T) {
	t.Run("default Long conversations takes the limit", func(t *testing.T) {
		home := tuneSandbox(t)
		path := filepath.Join(home, ".codex", "config.toml")
		tuneWrite(t, path, "model = \"gpt-5.5\"\nmodel_auto_compact_token_limit = 140_000\n\n[features]\nx = true\n")
		s, _ := TuneRead(home, "codex", TuneCompact, "")
		if s.Locked != codexCompactKey || !s.Take {
			t.Fatalf("read %+v: want locked, and takeable", s)
		}
		if err := TuneApply(home, "codex", TuneCompact, "", "100000"); err == nil {
			t.Fatal("applied over the user's own limit without their say")
		}
		if got := tuneFile(t, path); !strings.Contains(got, "140_000") {
			t.Fatalf("config.toml changed without the say:\n%s", got)
		}
		if err := TuneTakeOver(home, "codex"); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 140000 || got.FullContext {
			t.Fatalf("Long conversations %d full=%v, want the file's 140000", got.CompactAt, got.FullContext)
		}
		if got := tuneFile(t, path); strings.Contains(got, codexCompactKey) || !strings.Contains(got, "gpt-5.5") || !strings.Contains(got, "x = true") {
			t.Fatalf("config.toml after the take-over:\n%s", got)
		}
		s, _ = TuneRead(home, "codex", TuneCompact, "")
		if s.Locked != "" || s.Take || s.Source != TuneLong || s.Value != "140000" || !s.Ours {
			t.Fatalf("read after the take-over %+v", s)
		}
		// adjusted from the card now
		if err := TuneApply(home, "codex", TuneCompact, "", "100000"); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 100000 {
			t.Fatalf("Long conversations %d after apply", got.CompactAt)
		}
		if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 0 || got.FullContext {
			t.Fatalf("Long conversations %d after undo, want magpie's default", got.CompactAt)
		}
		if got := tuneFile(t, path); !strings.Contains(got, "model_auto_compact_token_limit = 140000") || !strings.Contains(got, "x = true") {
			t.Fatalf("undo didn't put the user's limit back:\n%s", got)
		}
		if s, _ := TuneRead(home, "codex", TuneCompact, ""); s.Locked != codexCompactKey || s.Value != "140000" {
			t.Fatalf("read after undo %+v", s)
		}
		if _, ok := stashShared()[tuneFileKey]; ok {
			t.Fatal("the file's stash stayed after undo")
		}
	})
	t.Run("a lower Long conversations stays where Codex compacted", func(t *testing.T) {
		home := tuneSandbox(t)
		path := filepath.Join(home, ".codex", "config.toml")
		if err := provider.SetCompactAt(120000); err != nil {
			t.Fatal(err)
		}
		tuneWrite(t, path, "model_auto_compact_token_limit = 180000\n")
		if err := TuneTakeOver(home, "codex"); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 120000 {
			t.Fatalf("Long conversations %d, want 120000 kept", got.CompactAt)
		}
		s, _ := TuneRead(home, "codex", TuneCompact, "")
		if s.Locked != "" || !s.Ours {
			t.Fatalf("read %+v: Undo must be offered", s)
		}
		if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
			t.Fatal(err)
		}
		if got := tuneFile(t, path); !strings.Contains(got, "= 180000") {
			t.Fatalf("undo didn't put the user's limit back:\n%s", got)
		}
		if got := settings.Load(); got.CompactAt != 120000 {
			t.Fatalf("Long conversations %d after undo", got.CompactAt)
		}
	})
	t.Run("Long conversations changed since is the user's", func(t *testing.T) {
		home := tuneSandbox(t)
		path := filepath.Join(home, ".codex", "config.toml")
		tuneWrite(t, path, "model_auto_compact_token_limit = 180000\n")
		if err := TuneTakeOver(home, "codex"); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetCompactAt(250000); err != nil {
			t.Fatal(err)
		}
		if err := TuneUndo(home, "codex", TuneCompact, ""); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load(); got.CompactAt != 250000 {
			t.Fatalf("undo wrote over the user's 250000: %d", got.CompactAt)
		}
		if got := tuneFile(t, path); strings.Contains(got, codexCompactKey) {
			t.Fatalf("undo put a limit back over the user's later choice:\n%s", got)
		}
	})
	t.Run("nothing to take", func(t *testing.T) {
		home := tuneSandbox(t)
		tuneWrite(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5.5\"\n")
		if err := TuneTakeOver(home, "codex"); err == nil {
			t.Fatal("took over nothing")
		}
		if err := TuneTakeOver(home, "claude"); err == nil {
			t.Fatal("took over Claude Code")
		}
	})
}

// TestTuneCodexTakeOverAsync: a background compaction threshold of the
// user's in config.toml (the screenshot's second lock) becomes Background
// compaction's on the same say: Settings takes its share, the file keeps
// it as magpie's, a sync leaves it, and Undo gives it back as the user's.
func TestTuneCodexTakeOverAsync(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".codex", "config.toml")
	file := "[features.async_compaction]\nenabled = true\nmodel_async_compact_threshold_percent = 80\n"
	tuneWrite(t, path, file)
	setAsyncCompact(t, false, 0)
	key := here(tuneHome(home)).key("codex.async")
	sync := func() {
		t.Helper()
		s := settings.Load()
		if err := codexAsyncSync(path, key, true, s.CodexAsyncCompact, s.CodexAsyncCompactPercent, true); err != nil {
			t.Fatal(err)
		}
	}
	if s := asyncRead(home); s.Locked != codexAsyncTable+"."+codexAsyncPercent || !s.Take {
		t.Fatalf("read %+v: want locked by the threshold, takeable", s)
	}
	if err := TuneTakeOver(home, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); !got.CodexAsyncCompact || got.CodexAsyncCompactPercent != 80 {
		t.Fatalf("Background compaction %v %d%%, want on at the file's 80", got.CodexAsyncCompact, got.CodexAsyncCompactPercent)
	}
	sync()
	if got := tuneFile(t, path); got != file {
		t.Fatalf("config.toml changed by the take-over and a sync:\n%s", got)
	}
	if s := asyncRead(home); s.Locked != "" || s.Value != "80" || !s.Ours {
		t.Fatalf("read after the take-over %+v", s)
	}
	// adjusted from the card now: a sync writes the new share
	if err := TuneApply(home, "codex", TuneAsync, "", "30"); err != nil {
		t.Fatal(err)
	}
	sync()
	if got := tuneFile(t, path); !strings.Contains(got, codexAsyncPercent+" = 30") {
		t.Fatalf("config.toml after apply:\n%s", got)
	}
	if err := TuneUndo(home, "codex", TuneAsync, ""); err != nil {
		t.Fatal(err)
	}
	sync()
	if got := settings.Load(); got.CodexAsyncCompact || got.CodexAsyncCompactPercent != 0 {
		t.Fatalf("Background compaction %v %d%% after undo, want off as before", got.CodexAsyncCompact, got.CodexAsyncCompactPercent)
	}
	if got := tuneFile(t, path); !strings.Contains(got, codexAsyncPercent+" = 80") || !strings.Contains(got, "enabled = true") {
		t.Fatalf("undo didn't give the user's threshold back:\n%s", got)
	}
	if s := asyncRead(home); s.Locked != codexAsyncTable+"."+codexAsyncPercent {
		t.Fatalf("read after undo %+v: the threshold is the user's again", s)
	}

	// enabled = false is no threshold to take: still locked, nothing taken
	tuneWrite(t, path, "[features.async_compaction]\nenabled = false\n")
	if s := asyncRead(home); s.Take {
		t.Fatalf("read %+v: enabled = false offered for taking", s)
	}
}

// TestTuneCodexMigrateKeepsStash: a limit an older tune wrote that can't
// be moved out of config.toml yet stays stashed as magpie's and is moved
// at a later read; before, the stash went first, and the limit was left
// in the file as if the user had written it, locking the card.
func TestTuneCodexMigrateKeepsStash(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder made read-only still takes writes here")
	}
	home := tuneSandbox(t)
	dir := filepath.Join(home, ".codex")
	path := filepath.Join(dir, "config.toml")
	tuneWrite(t, path, "model_auto_compact_token_limit = 150000\n")
	b, _ := json.Marshal(tuneStash{Set: "150000"})
	stash(map[string]string{tuneKey("codex", TuneCompact, ""): string(b)})
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	TuneRead(home, "codex", TuneCompact, "")
	if _, ok := tuneStashed("codex", TuneCompact, ""); !ok {
		t.Fatal("tune's stash went with its limit still in config.toml")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, _ := TuneRead(home, "codex", TuneCompact, "")
	if s.Locked != "" || s.Source != TuneLong || s.Value != "150000" {
		t.Fatalf("read after the file could be written %+v", s)
	}
	if got := tuneFile(t, path); strings.Contains(got, codexCompactKey) {
		t.Fatalf("config.toml:\n%s", got)
	}
}
