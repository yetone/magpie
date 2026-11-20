package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// A Claude Code run on an account (the warm-up, a model test, /usage, a
// bridge's turns) goes to Anthropic, which knows none of magpie's routing
// names. Where magpie wires Claude Code to itself, the variables its
// aliases resolve through hold them (claude/claude-haiku-5-5,
// group/opus-5-5[1m]); one in magpie's environment, or in ~/.claude.json's
// env, which Claude Code reads whatever --setting-sources says, made the
// warm-up's haiku ask Anthropic for claude/claude-haiku-5-5, refused "There's
// an issue with the selected model", and the 5-hour window never opened
// (@ahz2ygnhjmIJ0E0 on X). Each run's --settings sets them empty, which
// outranks both, and none of them is passed on from magpie's environment.
func TestClaudeRunsAskAnthropicsOwnModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	// each argument on its own line, then the model variables it was given
	script := "#!/bin/sh\n{ for a in \"$@\"; do printf 'arg:%s\\n' \"$a\"; done; env | grep -E '^(ANTHROPIC_|CLAUDE_CODE_SUBAGENT|CLAUDE_CODE_AUTO_MODE)' ; } > " + log + "\n" +
		"cat >/dev/null\necho '{\"type\":\"result\",\"is_error\":false,\"result\":\"Hi!\"}'\n"
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// the reporter's names, as a Claude Code wired to magpie holds them
	reporter := map[string]string{
		"ANTHROPIC_MODEL":                "group/opus-5-5[1m]",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "group/opus-5-5[1m]",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude/claude-sonnet-5-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "claude/claude-haiku-5-5",
		"ANTHROPIC_SMALL_FAST_MODEL":     "claude/claude-haiku-5-5",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "claude/claude-haiku-5-5",
	}
	for k, v := range reporter {
		t.Setenv(k, v)
	}
	runs := map[string]func() error{
		"warm-up":    func() error { return warmClaude(context.Background(), "") },
		"model test": func() error { return askClaude(context.Background(), "", "claude-haiku-5-5") },
		"/usage":     func() error { _, err := claudeUsage(context.Background(), ""); return err },
	}
	for name, run := range runs {
		_ = os.Remove(log)
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(log)
		got := string(b)
		for _, v := range []string{"claude/", "group/"} {
			if strings.Contains(got, "="+v) {
				t.Errorf("%s: Claude Code was given magpie's names in its environment:\n%s", name, got)
				break
			}
		}
		checkOwnModelSettings(t, name, strings.Split(got, "\n"), reporter)
	}
	// a bridge's turns
	args := claudeCLIArgs("claude-opus-5-5[1m]", `{}`, "", false)
	var lines []string
	for _, a := range args {
		lines = append(lines, "arg:"+a)
	}
	checkOwnModelSettings(t, "a bridge run", lines, reporter)
	// and its environment
	for _, e := range claudeRunEnv(os.Environ(), "1h") {
		if k, _, _ := strings.Cut(e, "="); reporter[k] != "" {
			t.Errorf("a bridge run keeps %s", e)
		}
	}
}

// checkOwnModelSettings has the run's --settings set each of reporter's
// variables, and each tier's, to "", so an alias resolves to Anthropic's own
// model whatever magpie's environment or ~/.claude.json say.
func checkOwnModelSettings(t *testing.T, name string, lines []string, reporter map[string]string) {
	t.Helper()
	var set string
	for i, l := range lines {
		if l == "arg:--settings" && i+1 < len(lines) {
			set = strings.TrimPrefix(lines[i+1], "arg:")
		}
	}
	var s struct {
		DeepLink string             `json:"disableDeepLinkRegistration"`
		Env      map[string]*string `json:"env"`
	}
	if err := json.Unmarshal([]byte(set), &s); err != nil {
		t.Fatalf("%s: --settings %q: %v", name, set, err)
	}
	if s.DeepLink != "disable" {
		t.Errorf("%s: --settings %s lost disableDeepLinkRegistration", name, set)
	}
	want := []string{"ANTHROPIC_DEFAULT_FABLE_MODEL"}
	for k := range reporter {
		want = append(want, k)
	}
	for _, k := range want {
		if v, ok := s.Env[k]; !ok || v == nil || *v != "" {
			t.Errorf("%s: --settings %s doesn't set %s empty", name, set, k)
		}
	}
}
