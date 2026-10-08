package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// sessionsHome is a sandbox HOME with the sessions package's fixtures as
// Claude Code's and Codex's folders, their models priced, dates in UTC.
func sessionsHome(t *testing.T) time.Time {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	// OpenCode's and Pi's folders in the sandbox too (HOME isn't the home
	// on Windows)
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
	}
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(h, ".pi", "agent"))

	for from, env := range map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME"} {
		dir := filepath.Join(h, "."+from)
		if err := os.CopyFS(dir, os.DirFS(filepath.Join("internal", "sessions", "testdata", from))); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, dir)
	}
	// put back after the Reset below, whose index write reads the zone
	testenv.Zone(t, time.UTC)
	oldPrice := sessions.PriceOf
	sessions.PriceOf = func(_ settings.Settings, m string) (catalog.Price, bool) {
		switch m {
		case "claude-opus-5-5":
			return catalog.Price{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}, true
		case "gpt-6-astra":
			return catalog.Price{Input: 10, Output: 50, CacheRead: 1}, true
		}
		return catalog.Price{}, false
	}
	t.Cleanup(func() {
		// the index is written behind the page: let that write finish before
		// the zone it reads (a stat of the file it writes) goes back
		sessions.Reset()
		sessions.PriceOf = oldPrice
	})
	sessions.Reset()
	return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
}

func runSessions(t *testing.T, now time.Time, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := sessionsTo(&b, args, now); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return b.String()
}

func wantAll(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

// magpie sessions lists the latest sessions; --days sums them day by day
// with the top models and folders; --model and --folder narrow both.
func TestSessionsCmd(t *testing.T) {
	now := sessionsHome(t)

	list := runSessions(t, now)
	wantAll(t, list, "ago", "Codex", "it's", "Add a README", "Claude Code", "app", "Fix login bug", "latest 2")
	if strings.Index(list, "Add a README") > strings.Index(list, "Fix login bug") {
		t.Error("not the latest first")
	}
	if got := runSessions(t, now, "--folder", "app"); strings.Contains(got, "README") || !strings.Contains(got, "login") {
		t.Errorf("--folder app:\n%s", got)
	}

	week := runSessions(t, now, "--days", "7")
	wantAll(t, week, "2.1K tokens last 7 days", "cache read 6.0K (75% hit)", "2026-09-22 – 2026-09-28",
		"Tue Sep 22", "Wed Sep 23    2.1K", "Mon Sep 28", "████", "models", "gpt-6-astra", "100%", "folders", "/work/it's")
	if strings.Contains(week, "Sep 21") || strings.Contains(week, "claude-opus") {
		t.Errorf("outside the range:\n%s", week)
	}

	app := runSessions(t, now, "--days=all", "--folder", "/work/app")
	wantAll(t, app, "all time · /work/app", "Sun Sep 20", "active 5m on 1 day", "claude-opus-5-5", "claude-haiku-4-5-20251001", "not priced: claude-haiku-4-5-20251001")
	if strings.Contains(app, "folders") {
		t.Errorf("folders under a folder:\n%s", app)
	}
	opus := runSessions(t, now, "stats", "--days", "all", "--model", "opus")
	wantAll(t, opus, "all time · claude-opus-5-5", "active —", "folders", "/work/app")

	var r sessions.Rollup
	if err := json.Unmarshal([]byte(runSessions(t, now, "--days", "today", "--json")), &r); err != nil {
		t.Fatal(err)
	}
	if r.From != "2026-09-28" || len(r.Days) != 1 || r.Spent() != 0 {
		t.Errorf("today: %+v", r)
	}
	var ss []sessions.Session
	if err := json.Unmarshal([]byte(runSessions(t, now, "--json", "--model", "gpt-6-astra")), &ss); err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Agent != "codex" || ss[0].Resume == "" {
		t.Errorf("--json --model: %+v", ss)
	}

	var b bytes.Buffer
	if err := sessionsTo(&b, []string{"--days", "7", "--model", "nope"}, now); err == nil || !strings.Contains(err.Error(), "gpt-6-astra") {
		t.Errorf("an unknown model: %v", err)
	}
	if err := sessionsTo(&b, []string{"--days", "week"}, now); err == nil {
		t.Error("--days week taken")
	}
}

// The "%d more with --limit" foot is counted over every session, not over a
// list cut at sessions.Limit: with the default --limit 20 the command read
// only 200, so a history longer than that reported a remainder that was
// wrong, and a model or folder filter picked among the rest could never be
// resolved (yetone, #1019).
func TestSessionsMoreCountsTheWholeSet(t *testing.T) {
	now := sessionsHome(t)
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	proj := filepath.Join(claude, "projects", "-work-many")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// past sessions.Limit: the fixtures hold 2, so 210 more
	const n = sessions.Limit + 10
	for i := range n {
		id := fmt.Sprintf("%08d-1111-2222-3333-444444444444", i)
		line := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi %d"},"uuid":"u","timestamp":"2026-09-20T10:00:00.000Z","cwd":"/work/many","sessionId":"%s"}`+"\n"+
			`{"parentUuid":"u","isSidechain":false,"message":{"model":"claude-opus-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2}},"requestId":"req_1","type":"assistant","uuid":"a","timestamp":"2026-09-20T10:00:05.000Z","cwd":"/work/many","sessionId":"%s"}`+"\n", i, id, id)
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sessions.Reset()

	// the default --limit 20 prints 20 rows and says how many are left of all
	got := runSessions(t, now, "--limit", "20")
	if !strings.Contains(got, "latest 20") {
		t.Fatalf("the foot of the default:\n%s", got)
	}
	total := 2 + n // the fixtures' own two and these
	if want := fmt.Sprintf("· %d more with --limit", total-20); !strings.Contains(got, want) {
		t.Errorf("the foot says %q, want %q:\n%s", footOf(got), want, got)
	}
	// and --limit past 200 shows that many, which the old read could not
	if got := runSessions(t, now, "--limit", "205"); !strings.Contains(got, "latest 205") {
		t.Errorf("--limit 205:\n%s", got)
	}
}

// footOf is the line the command's foot is on, for a failure message.
func footOf(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "with --limit") || strings.Contains(l, "latest ") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// The hit rate is of all the prompts came to: what was written to the
// cache counts in it, as Input leaves it out — a prompt written again at
// every turn showed near 100% otherwise.
func TestHitRateCountsCacheWrites(t *testing.T) {
	if got := hitRate(sessions.Tokens{Input: 10, CacheRead: 20, CacheWrite: 70}); !strings.Contains(got, "(20% hit)") {
		t.Errorf("hitRate = %q, want 20%%", got)
	}
}
