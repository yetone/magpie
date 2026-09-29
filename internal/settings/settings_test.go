package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/redact"
)

func TestRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Theme != "system" || s.Lang != "system" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := Save(Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("saved: %+v", s)
	}
	if err := Save(Settings{Theme: "sepia"}); err == nil {
		t.Fatal("bad theme accepted")
	}
	if Save(Settings{}) != nil || Load().Theme != "system" {
		t.Fatal("empty means system")
	}
	if filepath.Base(Path()) != "settings.json" {
		t.Fatal(Path())
	}
	if Save(Settings{CodexWarmup: "week"}) != nil || Load().CodexWarmup != "week" {
		t.Fatal("codex warm-up not kept")
	}
	if Save(Settings{CodexWarmup: "off"}) != nil || Load().CodexWarmup != "" {
		t.Fatal("off is off")
	}
	if Save(Settings{CodexWarmup: "daily"}) == nil {
		t.Fatal("bad codex warm-up accepted")
	}
	if Save(Settings{ClaudeWarmup: "all"}) != nil || Load().ClaudeWarmup != "all" {
		t.Fatal("claude warm-up not kept")
	}
	if Save(Settings{ClaudeWarmup: "hourly"}) == nil {
		t.Fatal("bad claude warm-up accepted")
	}
	// a time of day to start the 5-hour windows at, as 06:00 however given
	if Save(Settings{CodexWarmAt: "6:00", ClaudeWarmAt: "21:30:00"}) != nil || Load().CodexWarmAt != "06:00" || Load().ClaudeWarmAt != "21:30" {
		t.Fatalf("warm-up times not kept: %+v", Load())
	}
	for _, bad := range []string{"25:00", "6am", "06:60"} {
		if Save(Settings{CodexWarmAt: bad}) == nil {
			t.Fatalf("bad time %q accepted", bad)
		}
	}
	if Save(Settings{}) != nil || Load().CodexWarmAt != "" {
		t.Fatal("no time is off")
	}
	// the menu bar's usage: every 3 minutes unless told, left or used
	if s := Load(); s.TrayUsageEvery != 3 || s.QuotaLeft {
		t.Fatalf("tray defaults: %+v", s)
	}
	if Save(Settings{TrayUsageEvery: 10, QuotaLeft: true}) != nil || Load().TrayUsageEvery != 10 || !Load().QuotaLeft {
		t.Fatal("tray refresh or left not kept")
	}
	if Save(Settings{TrayUsageEvery: 7}) == nil {
		t.Fatal("bad tray refresh accepted")
	}
	// the user's masking rules: kept tidied, and one that doesn't compile
	// said, not saved and left out (#195)
	if err := Save(Settings{RedactRules: []redact.Rule{{Kind: "gw key", Prefix: " oc_sk_ "}, {Kind: "ns", Regex: `ns-[0-9a-f]{12}`}}}); err != nil {
		t.Fatal(err)
	}
	if r := Load().RedactRules; len(r) != 2 || r[0] != (redact.Rule{Kind: "GW_KEY", Prefix: "oc_sk_"}) || r[1].Kind != "NS" {
		t.Fatalf("rules not kept: %+v", r)
	}
	if err := Save(Settings{RedactRules: []redact.Rule{{Kind: "bad", Regex: `ns-[0-9a-f`}}}); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Fatalf("bad regex: %v", err)
	}
	if len(Load().RedactRules) != 2 {
		t.Fatal("a failed save changed the rules")
	}
}

func TestMigrate(t *testing.T) {
	cfg, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	old := filepath.Join(cfg, "dial")
	if err := os.MkdirAll(filepath.Join(old, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "providers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "sub", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	Migrate()
	fi, err := os.Stat(filepath.Join(cfg, "magpie", "providers.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("providers.json not copied with its mode: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "magpie", "sub", "x")); err != nil {
		t.Fatal("nested file not copied")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("the old folder must stay")
	}
	if _, err := os.Stat(filepath.Join(cache, "magpie")); err == nil {
		t.Fatal("no old cache, so no new one")
	}
	// a second run must not touch an existing folder
	if err := os.WriteFile(filepath.Join(cfg, "magpie", "providers.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	Migrate()
	if b, _ := os.ReadFile(filepath.Join(cfg, "magpie", "providers.json")); string(b) != "new" {
		t.Fatal("existing folder overwritten")
	}
}

func TestArrange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := Settings{AgentOrder: []string{"codex", "gone", "claude", "codex"}, AgentsHidden: []string{"crush"}}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	s = Load()
	if strings.Join(s.AgentOrder, ",") != "codex,gone,claude" {
		t.Fatalf("order kept as %v", s.AgentOrder)
	}
	// an agent the order doesn't name follows, in the order it came
	shown, hidden := Arrange(s, []string{"claude", "gemini", "crush", "codex", "pi"}, func(x string) string { return x })
	if strings.Join(shown, ",") != "codex,claude,gemini,pi" || strings.Join(hidden, ",") != "crush" {
		t.Fatalf("arranged %v, hidden %v", shown, hidden)
	}
	// the rest of the settings leave it as it is
	s.Theme = "dark"
	if Save(s) != nil || len(Load().AgentsHidden) != 1 {
		t.Fatal("arrangement lost")
	}
}
