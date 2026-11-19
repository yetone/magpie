package usage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// A page's AgentModels are, for each agent, what the same page with that
// agent picked has as its By["modelAt"]: a day's card reads them off the
// day's own page instead of building a page of each agent (WaiooT on X),
// on the cells' path and on the ledger's (LogCalls) alike.
func TestAgentModelsAreEachAgentsPage(t *testing.T) {
	for _, viaLog := range []bool{false, true} {
		t.Run(map[bool]string{false: "cells", true: "ledger"}[viaLog], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			os.MkdirAll(filepath.Join(home, ".config"), 0o755)
			now := holdClock(t, time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local))
			for i, r := range []Record{
				{Agent: "claude-cli/2.1", Provider: "anthropic", Model: "claude-x", Input: 50, Output: 5},
				{Agent: "claude-cli/2.1", Provider: "relay", Model: "claude-x", Input: 40, Output: 4},
				{Agent: "claude-cli/2.1", Provider: "anthropic", Model: "claude-y", Input: 30, Output: 3},
				{Agent: "codex_cli_rs/0.9", Provider: "openai", Model: "gpt-z", Input: 20, Output: 2},
				{Agent: "", Provider: "openai", Model: "gpt-z", Input: 10, Output: 1},
				// yesterday's, which a page of today leaves out
				{Agent: "claude-cli/2.1", Provider: "anthropic", Model: "claude-old", Input: 8, Output: 8, Time: now.AddDate(0, 0, -1)},
				{Agent: "codex_cli_rs/0.9", Provider: "openai", Model: "gpt-old", Input: 9, Output: 9, Time: now.AddDate(0, 0, -1)},
			} {
				if r.Time.IsZero() {
					r.Time = now.Add(-time.Duration(i+1) * time.Minute)
				}
				r.Status = 200
				Append(r)
			}
			if viaLog {
				old := LogCalls
				LogCalls = func(time.Time) []sessions.Call {
					return []sessions.Call{{Time: now.Add(-time.Hour), Agent: "codex", Session: "c1", Model: "gpt-w", Tokens: sessions.Tokens{Input: 7, Output: 7}}}
				}
				t.Cleanup(func() { LogCalls = old })
			}
			today := Filter{Day: now.Format(time.DateOnly), Zone: time.Local}
			yesterday := Filter{Day: now.AddDate(0, 0, -1).Format(time.DateOnly), Zone: time.Local}
			for _, f := range []Filter{{}, today, yesterday} {
				page := QueryPage(Week, f, 0, 1)
				if len(page.By["agent"]) < 1 {
					t.Fatalf("agents %+v", page.By["agent"])
				}
				for _, a := range page.By["agent"] {
					if a.ID == "" {
						continue
					}
					af := f
					af.Agent = a.ID
					want := QueryPage(Week, af, 0, 1).By["modelAt"]
					if got := page.AgentModels[a.ID]; !reflect.DeepEqual(got, want) {
						t.Errorf("%+v: %s's models %+v, its own page's %+v", f, a.ID, got, want)
					}
				}
			}
		})
	}
}
