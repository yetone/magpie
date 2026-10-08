package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeWarmClaude is a claude that writes what it was run with to log and
// answers as out says.
func fakeWarmClaude(t *testing.T, out string, code int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\n" +
		"{ printf 'args:'; for a in \"$@\"; do printf '[%s]' \"$a\"; done; echo; " +
		"echo \"dir:$CLAUDE_CONFIG_DIR\"; echo \"base:$ANTHROPIC_BASE_URL\"; echo \"pwd:$(pwd)\"; printf 'stdin:'; cat; echo; " +
		"echo \"traffic:$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC\"; echo \"telemetry:$DISABLE_TELEMETRY\"; " +
		"echo \"errors:$DISABLE_ERROR_REPORTING\"; echo \"updater:$DISABLE_AUTOUPDATER\"; } > " + log + "\n" +
		"echo '" + out + "'\nexit " + string(rune('0'+code)) + "\n"
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:3425")
	return log
}

func TestWarmClaudeAsksClaudeCodeOnce(t *testing.T) {
	log := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"Hi!"}`, 0)
	t.Setenv("CLAUDE_CONFIG_DIR", "/the/users/own")
	if err := warmClaude(context.Background(), "/saved/account"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"[-p]", "[--model][haiku]", "[--tools][]", "[--setting-sources][]", "[--no-session-persistence]",
		"dir:/saved/account\n", "base:\n", "stdin:hi", "magpie-claude-"} {
		if !strings.Contains(got, want) {
			t.Errorf("run lacks %q:\n%s", want, got)
		}
	}
	// its folder goes with it
	for _, l := range strings.Split(got, "\n") {
		if d, ok := strings.CutPrefix(l, "pwd:"); ok {
			if _, err := os.Stat(d); !os.IsNotExist(err) {
				t.Errorf("%s left behind", d)
			}
		}
	}
	// the account Claude Code is signed in to is its own, in its own
	// config directory
	if err := warmClaude(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "dir:/the/users/own\n") {
		t.Errorf("another config directory given for Claude Code's own account:\n%s", b)
	}
}

func TestWarmClaudeSaysWhyNot(t *testing.T) {
	fakeWarmClaude(t, `{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}`, 1)
	err := warmClaude(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "Please run /login") {
		t.Fatalf("got %v", err)
	}
	fakeWarmClaude(t, `not json`, 2)
	if err := warmClaude(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not json") {
		t.Fatalf("got %v", err)
	}
}

// A Claude account's model test runs Claude Code at the model tested.
func TestClaudeTestRunsClaudeCode(t *testing.T) {
	log := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"Hi!"}`, 0)
	p := provider.Provider{Name: "Claude", Account: &provider.Account{Agent: "claude"}}
	r := p.TestModels(context.Background(), []string{"claude-sonnet-4-5"})
	if len(r) != 1 || !r[0].OK || r[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("result %+v", r)
	}
	b, _ := os.ReadFile(log)
	if got := string(b); !strings.Contains(got, "[--model][claude-sonnet-4-5]") || !strings.Contains(got, "stdin:hi") {
		t.Fatalf("run:\n%s", got)
	}
}

// Claude's usage is Claude Code's own /usage, run with nothing of the
// user's settings and nothing kept, as the account it is signed in to.
func TestClaudeUsageRunsClaudeCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	out := filepath.Join(dir, "out")
	text := "You are currently using your subscription\n\nCurrent session: 13% used · resets Oct 1 at 3:30pm (Asia/Shanghai)\n"
	b, _ := json.Marshal(map[string]any{"type": "result", "is_error": false, "num_turns": 0, "result": text})
	os.WriteFile(out, b, 0o644)
	script := "#!/bin/sh\n" +
		"{ printf 'args:'; for a in \"$@\"; do printf '[%s]' \"$a\"; done; echo; echo \"token:$CLAUDE_CODE_OAUTH_TOKEN\"; } > " + log + "\n" +
		"cat " + out + "\n"
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "leaked")
	got, err := claudeUsage(context.Background())
	if err != nil || got != text {
		t.Fatalf("usage %q %v", got, err)
	}
	b, _ = os.ReadFile(log)
	for _, want := range []string{"[-p][/usage]", "[--setting-sources][]", "[--strict-mcp-config]", "[--no-session-persistence]", "token:\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("run lacks %q:\n%s", want, b)
		}
	}
}

// /usage runs without CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, which keeps
// Claude Code from asking for the usage, unless the user set it; telemetry,
// error reporting and autoupdate stay off. The warm-up keeps it.
func TestClaudeUsageSendsItsRequest(t *testing.T) {
	log := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"Current session: 13% used"}`, 0)
	for _, k := range []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING", "DISABLE_AUTOUPDATER"} {
		t.Setenv(k, "")
	}
	if _, err := claudeUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"traffic:\n", "telemetry:1\n", "errors:1\n", "updater:1\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("/usage run lacks %q:\n%s", want, b)
		}
	}
	if err := warmClaude(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "traffic:1\n") {
		t.Errorf("warm-up lacks CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:\n%s", b)
	}
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	if _, err := claudeUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "traffic:1\n") {
		t.Errorf("/usage run dropped the user's CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:\n%s", b)
	}
}

// With Claude Code's verbose output on (its /config, kept in .claude.json),
// claude -p --output-format json prints every message as an array, the
// system init first and the result last (KevinXC on Discord: usage
// couldn't be read; the tooltip began [{"type":"system","subtype":"init")
func TestClaudeVerboseOutput(t *testing.T) {
	text := "Current session: 13% used"
	b, _ := json.Marshal([]map[string]any{
		{"type": "system", "subtype": "init", "tools": []string{}},
		{"type": "result", "subtype": "success", "is_error": false, "result": text},
	})
	fakeWarmClaude(t, string(b), 0)
	got, err := claudeUsage(context.Background())
	if err != nil || got != text {
		t.Fatalf("usage %q %v", got, err)
	}
	b, _ = json.Marshal([]map[string]any{
		{"type": "system", "subtype": "init"},
		{"type": "result", "is_error": true, "result": "Invalid API key · Please run /login"},
	})
	fakeWarmClaude(t, string(b), 1)
	if err := warmClaude(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "Please run /login") || strings.Contains(err.Error(), "init") {
		t.Fatalf("got %v", err)
	}
}
