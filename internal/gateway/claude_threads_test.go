package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A turn's Claude Code runs with its network as a plain claude has it, so it
// reads the account's feature flags, Message Threads among them (#5963):
// with CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, DISABLE_TELEMETRY or
// DO_NOT_TRACK set it reads none, and every turn sends the whole history,
// every image in it. Nothing forces threads (CLAUDE_CODE_TETHER_LIVE is
// never set). Updates and error reports stay off, and a quiet variable the
// user set, in magpie's environment or in their Claude Code's settings.json,
// stays as they set it.
func TestClaudeRunReadsItsFlags(t *testing.T) {
	for _, c := range []struct {
		name, settings string
		env            map[string]string
		want           string
	}{
		{name: "nothing set", want: "traffic unset telemetry unset track unset tether unset updater 1 errors 1"},
		{name: "settings telemetry off", settings: `{"env":{"DISABLE_TELEMETRY":"1"}}`, want: "traffic unset telemetry 1 track unset tether unset updater 1 errors 1"},
		{name: "settings nonessential off", settings: `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"true"}}`, want: "traffic true telemetry unset track unset tether unset updater 1 errors 1"},
		{name: "magpie's env", env: map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DO_NOT_TRACK": "1"}, want: "traffic 1 telemetry unset track 1 tether unset updater 1 errors 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range append(claudeQuietVars, "CLAUDE_CODE_TETHER_LIVE", "DISABLE_AUTOUPDATER", "DISABLE_ERROR_REPORTING") {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			h := newScriptHarness(t, "ok", `echo "traffic ${CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC-unset} telemetry ${DISABLE_TELEMETRY-unset} track ${DO_NOT_TRACK-unset} tether ${CLAUDE_CODE_TETHER_LIVE-unset} updater ${DISABLE_AUTOUPDATER-unset} errors ${DISABLE_ERROR_REPORTING-unset}" >> $log`)
			if c.settings != "" {
				if err := os.MkdirAll(h.config, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.config, "settings.json"), []byte(c.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h.ask("rules", "hello", "next")
			runs := h.runs()
			if len(runs) != 1 {
				t.Fatalf("runs: %d", len(runs))
			}
			for _, log := range runs {
				if !strings.Contains(log, c.want) {
					t.Fatalf("run's environment, want %q:\n%s", c.want, log)
				}
			}
		})
	}
}
