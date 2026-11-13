package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's advisor for magpie's models (Awad_Yoo on X): Claude Code
// sends it only for Anthropic's own models unless
// CLAUDE_CODE_ENABLE_EXPERIMENTAL_ADVISOR_TOOL is set (2.1.316). The
// advisor field writes advisorModel, as /advisor does, with that switch,
// only while magpie routes Claude Code and only for a model magpie serves;
// clearing it, or magpie stepping out, takes both away, but never a switch
// the user wrote.
func TestClaudeAdvisorModel(t *testing.T) {
	a, path, env := claudeAutoModeHome(t, `{}`)
	f := a.Field("advisor")
	if f == nil {
		t.Fatal("no advisor field")
	}
	get := func() string { v, _ := edit.GetJSON(path, "advisorModel"); return v }
	if err := f.Set("relay/claude-opus-5-5"); err == nil {
		t.Fatal("an advisor before Claude Code is routed should fail")
	}
	if err := a.Field("model").Set("relay/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	// unset by default: no advisor, no switch
	if f.Get() != "" || get() != "" || env(claudeAdvisorEnv) != "" {
		t.Fatalf("advisor on by default: %q %q", get(), env(claudeAdvisorEnv))
	}
	if !containsOption(f.Options(nil), "relay/claude-opus-5-5") {
		t.Fatalf("options miss magpie's models: %v", f.Options(nil))
	}
	if err := f.Set("claude-opus-5-5"); err == nil || get() != "" {
		t.Fatalf("a model magpie doesn't serve taken: %v %q", err, get())
	}
	if err := f.Set("relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != provider.ClaudeSpelled("relay/claude-opus-5-5") || env(claudeAdvisorEnv) != "1" {
		t.Fatalf("advisorModel %q, switch %q", got, env(claudeAdvisorEnv))
	}
	if f.Get() != "relay/claude-opus-5-5" {
		t.Fatalf("get %q", f.Get())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if get() != "" || env(claudeAdvisorEnv) != "" {
		t.Fatalf("clearing left %q %q", get(), env(claudeAdvisorEnv))
	}
	// magpie stepping out takes its advisor away
	if err := f.Set("relay/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if get() != "" || env(claudeAdvisorEnv) != "" {
		b, _ := os.ReadFile(path)
		t.Fatalf("disconnect left the advisor: %s", b)
	}
}

// A switch the user turned on themselves stays theirs: clearing magpie's
// advisor, or magpie stepping out, leaves it.
func TestClaudeAdvisorUserSwitch(t *testing.T) {
	a, path, env := claudeAutoModeHome(t, `{"env":{"CLAUDE_CODE_ENABLE_EXPERIMENTAL_ADVISOR_TOOL":"1"},"advisorModel":"opus"}`)
	if err := a.Field("model").Set("relay/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	f := a.Field("advisor")
	if err := f.Set("relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if env(claudeAdvisorEnv) != "1" {
		b, _ := os.ReadFile(filepath.Clean(path))
		t.Fatalf("the user's own switch taken: %s", b)
	}
	if err := f.Set("relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if env(claudeAdvisorEnv) != "1" {
		b, _ := os.ReadFile(path)
		t.Fatalf("disconnect took the user's own switch: %s", b)
	}
}
