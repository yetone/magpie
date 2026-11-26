package agent

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Grok Build compacts at the percent of its window the user picked for it
// (moooyo on Discord: compact once 65% of the context is used): 65% of the
// model's whole window by default, where Grok Build runs to it, and 65% of
// the size Long conversations gives it when one is typed. None picked
// gives back Grok Build's own 85%; the user's own percent, in a table or
// in [session], stays theirs.
func TestGrokCompactPercent(t *testing.T) {
	home, path := grokSandbox(t)
	a := grok(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	set := func(f func(*settings.Settings)) {
		t.Helper()
		s := settings.Load()
		f(&s)
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	want := func(when string, pro, flash int) {
		t.Helper()
		if p, f := grokPercents(t, path); p != pro || f != flash {
			raw, _ := os.ReadFile(path)
			t.Fatalf("%s: percents pro %d, flash %d, want %d and %d\n%s", when, p, f, pro, flash, raw)
		}
	}
	set(func(s *settings.Settings) {})
	want("none picked", -1, -1)
	set(func(s *settings.Settings) { s.AgentCompactPcts = map[string]int{"grok": 65} })
	want("65 of the whole window", 65, 65)
	set(func(s *settings.Settings) { s.CompactAt = 300000 })
	want("65 of 300K", 19, 65)
	set(func(s *settings.Settings) { s.CompactAt, s.AgentCompactPcts = 0, map[string]int{"codex": 65} })
	want("codex's percent is not grok's", -1, -1)
	set(func(s *settings.Settings) { s.AgentCompactPcts = map[string]int{"grok": 70} })
	want("70 picked", 70, 70)
	if err := edit.SetTOMLKey(path, `model."magpie/deepseek/pro"`, "auto_compact_threshold_percent", 60); err != nil {
		t.Fatal(err)
	}
	set(func(s *settings.Settings) { s.AgentCompactPcts = map[string]int{"grok": 75} })
	want("pro's own 60", 60, 75)
	set(func(s *settings.Settings) { s.AgentCompactPcts = nil })
	want("cleared, pro's own 60 kept", 60, -1)
}
