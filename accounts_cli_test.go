package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// Run the public command in a fresh process, with only a fake account,
// fake /usage and an on-disk snapshot in a temporary home.
func TestAccountsCachedReading(t *testing.T) {
	if mode := os.Getenv("MAGPIE_TEST_ACCOUNTS_READING"); mode != "" {
		provider.UsageClaudeVia(func(context.Context) (string, error) {
			if strings.HasPrefix(mode, "fresh") {
				return "Current session: 25% used", nil
			}
			return "You are currently using your subscription to power your Claude Code usage.", nil
		})
		args := []string{"accounts", "claude"}
		if strings.HasSuffix(mode, "json") {
			args = append(args, "--json")
		}
		if err := accountsCmd(args); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"cached-text", "cached-json", "fresh-text", "fresh-json"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, ".config", "magpie")
			at := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
			reset := at.Add(time.Hour)
			write := func(path string, value any) {
				t.Helper()
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(home, ".claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "sk-ant-oat01-test", "refreshToken": "sk-ant-ort01-test", "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
				"subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"},
			}})
			write(filepath.Join(home, ".claude", ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com"}})
			write(filepath.Join(config, "quotas.json"), map[string]any{"claude/a@example.com": map[string]any{
				"at": at, "quota": provider.SubscriptionQuota{Provider: "claude", User: "a@example.com", Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &reset}}},
			}})
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(self, "-test.run=^TestAccountsCachedReading$")
			// A minimal environment prevents discovering real sign-ins or tools.
			cmd.Env = []string{"MAGPIE_TEST_ACCOUNTS_READING=" + mode, "HOME=" + home, "USERPROFILE=" + home,
				"XDG_CONFIG_HOME=" + filepath.Dir(config), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
				"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "CODEX_HOME=" + filepath.Join(home, ".codex"), "PATH=" + home,
				"SystemRoot=" + os.Getenv("SystemRoot"), "USER=magpie-test", "NO_COLOR=1",
				// so that the binary keeps this home instead of a sandbox of its own
				testenv.Marker + "=" + os.Getenv(testenv.Marker),
				"TMP=" + os.Getenv("TMP"), "TEMP=" + os.Getenv("TEMP"), "TMPDIR=" + os.Getenv("TMPDIR")}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("accounts: %v: %s", err, out)
			}
			cached := strings.HasPrefix(mode, "cached")
			if strings.HasSuffix(mode, "json") {
				var rows []struct {
					AsOf    *time.Time           `json:"asOf"`
					Windows []provider.QuotaSpan `json:"windows"`
				}
				if err := json.Unmarshal(out, &rows); err != nil || len(rows) != 1 || len(rows[0].Windows) != 1 {
					t.Fatalf("accounts JSON: %s (%v)", out, err)
				}
				q := rows[0]
				if cached {
					if q.AsOf == nil || !q.AsOf.Equal(at) || q.Windows[0].Used != 100 || q.Windows[0].ResetsAt == nil || !q.Windows[0].ResetsAt.Equal(reset) {
						t.Fatalf("dated historical reading lost: %s", out)
					}
				} else if q.AsOf != nil || q.Windows[0].Used != 25 {
					t.Fatalf("fresh reading still marked cached: %s", out)
				}
			} else if cached {
				for _, want := range []string{"100%", "as of", "(cached)", "reset time passed", "current allowance unknown"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("accounts missing %q: %s", want, out)
					}
				}
			} else if !strings.Contains(string(out), "25%") || strings.Contains(string(out), "(cached)") {
				t.Fatalf("fresh accounts output: %s", out)
			}
		})
	}
}

func TestUntilShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute:                  "now",
		45 * time.Minute:              "45m",
		2*time.Hour + 13*time.Minute:  "2h13m",
		76*time.Hour + 30*time.Minute: "3d4h",
	} {
		if got := untilShort(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestQuotaCell(t *testing.T) {
	// a fixed now, so the reset reads the same day whenever the test runs
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	at := now.Add(2*time.Hour + 13*time.Minute + 30*time.Second)
	got := quotaCell(quotaSpan{Name: "5 hours", Used: 42, ResetsAt: &at}, now)
	if !strings.HasPrefix(got, "5h 42%") || !strings.Contains(got, "↻2h13m 14:13") {
		t.Fatalf("%q", got)
	}
	// a reset on the next day reads "tomorrow" against the same now
	next := now.Add(14*time.Hour + 13*time.Minute + 30*time.Second)
	if got := quotaCell(quotaSpan{Name: "7 days", Used: 42, ResetsAt: &next}, now); !strings.Contains(got, "↻14h13m tomorrow 02:13") {
		t.Fatalf("tomorrow: %q", got)
	}
	// a pool's own window comes in named with its pool (PooledWindows) and
	// reads compactly, without the " · "
	pooled := quotaCell(quotaSpan{Name: "Gemini · 7 days", Pool: "Gemini", Used: 80}, now)
	if !strings.HasPrefix(pooled, "Gemini 7d 80%") {
		t.Fatalf("pooled: %q", pooled)
	}
}

// accountRows shows a pool's own windows in place of its models', as the
// usage page does; reverting it to the raw per-model windows fails here.
func TestAccountRowsPoolsAntigravity(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	reset := now.Add(48 * time.Hour)
	five, week := 5*time.Hour, 7*24*time.Hour
	provider.LoginUsageVia(func(context.Context, string) map[string]provider.SubscriptionQuota {
		return map[string]provider.SubscriptionQuota{"u@x.com": {
			Provider: "antigravity", Name: "Antigravity", User: "u@x.com",
			Windows: []provider.QuotaWindow{
				{Name: "Gemini 3 Flash", Model: "gemini-3-flash", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
				{Name: "Gemini 3.1 Pro (High)", Model: "gemini-3.1-pro-high", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
				{Name: "Claude Opus 4.6 (Thinking)", Model: "claude-opus-4-6-thinking", Family: "Claude", Pool: "Claude & GPT", Used: 90},
				{Name: "GPT-OSS 120B (Medium)", Model: "gpt-oss-120b-medium", Family: "GPT-OSS", Pool: "Claude & GPT", Used: 90},
				{Name: "7 days", Pool: "Gemini", Span: week, Aside: true, Used: 85, ResetsAt: &reset},
				{Name: "5 hours", Pool: "Gemini", Span: five, Aside: true, Used: 95, ResetsAt: &reset},
				{Name: "7 days", Pool: "Claude & GPT", Span: week, Aside: true, Used: 90, ResetsAt: &reset},
				{Name: "5 hours", Pool: "Claude & GPT", Span: five, Aside: true, Used: 90, ResetsAt: &reset},
			},
		}}
	})
	t.Cleanup(func() { provider.LoginUsageVia(nil) })
	rows := accountRows([]provider.Login{{Agent: "antigravity", User: "u@x.com", Active: true, On: true}}, now)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	var names []string
	for _, w := range rows[0].Windows {
		names = append(names, w.Name)
	}
	if strings.Join(names, ",") != "Gemini · 7 days,Gemini · 5 hours,Claude & GPT · 7 days,Claude & GPT · 5 hours" {
		t.Fatalf("windows %v", names)
	}
}

// A ChatGPT account's credits are in its row beside its windows, as on the
// usage page and in magpie quota.
func TestAccountRowsTellCredits(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	provider.LoginUsageVia(func(context.Context, string) map[string]provider.SubscriptionQuota {
		return map[string]provider.SubscriptionQuota{"me@example.com": {
			Provider: "codex", Name: "Codex", User: "me@example.com", Balance: "1.2K credits",
			Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 100}, {Name: "7 days", Used: 40}},
		}}
	})
	t.Cleanup(func() { provider.LoginUsageVia(nil) })
	rows := accountRows([]provider.Login{{Agent: "codex", User: "Me@example.com", Active: true, On: true}}, now)
	if len(rows) != 1 || rows[0].Balance != "1.2K credits" || len(rows[0].Windows) != 2 {
		t.Fatalf("%+v", rows)
	}
}
