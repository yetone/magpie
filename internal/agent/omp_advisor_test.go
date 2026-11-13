package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func ompAdvisorHome(t *testing.T, config string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "config.yml")
	if config != "" {
		os.WriteFile(path, []byte(config), 0o644)
	}
	return home, path
}

// #6408 (Awad_Yoo on X), #5019: omp runs no advisor while advisor.enabled
// is unset, its default being off (advisor/settings.ts), so a model picked
// for the advisor role in magpie reviewed nothing. The pick turns it on,
// the role's reset takes that back, and a value the user set stays.
func TestOmpAdvisorPickTurnsItOn(t *testing.T) {
	home, path := ompAdvisorHome(t, "modelRoles:\n  default: anthropic/claude-opus-5\n")
	a := omp(home)
	on := func() string { return ompAdvisorOn(path) }

	if err := a.Apply("advisor", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if on() != "true" {
		t.Fatalf("advisor picked, advisor.enabled = %q", on())
	}
	if got := a.Values()["advisor_review"]; got != "turn" {
		t.Fatalf("advisor_review = %q, want omp's default review, turn", got)
	}
	// its level is the role's value written again: still on
	if err := a.Apply("advisor_thinking", "high"); err != nil || on() != "true" {
		t.Fatalf("level: %v, enabled %q", err, on())
	}
	if err := a.Apply("advisor", ""); err != nil {
		t.Fatal(err)
	}
	if on() != "" {
		t.Fatalf("the reset left advisor.enabled = %q, which only magpie's pick had set", on())
	}

	// the user's own off stays off when a model is picked
	if err := a.Apply("advisor_review", "off"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("advisor", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if on() != "false" {
		t.Fatalf("the user's off became %q", on())
	}
	// and the user's on stays on when the role is reset
	if err := a.Apply("advisor_review", "agent-end"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("advisor", ""); err != nil {
		t.Fatal(err)
	}
	if on() != "true" {
		t.Fatalf("the user's on became %q after the role's reset", on())
	}
}

// The gating omp has, written as omp reads it: whether and when the
// advisor reviews (advisor.enabled with reviewMode), how long the main
// agent waits on it (syncBacklog: a string, as omp's enum settings accept
// only strings) and which note calls a finished agent back
// (continueSeverity).
func TestOmpAdvisorSettings(t *testing.T) {
	home, path := ompAdvisorHome(t, "# mine\nadvisor:\n  maxNotesPerUpdate: 2\n")
	a := omp(home)
	for _, k := range []string{"advisor_review", "advisor_wait", "advisor_wake"} {
		f := a.Field(k)
		if f == nil || !f.Quiet {
			t.Fatalf("%s: %+v", k, f)
		}
		if v := a.Values()[k]; v != "" {
			t.Fatalf("%s unset reads %q", k, v)
		}
	}
	for _, kv := range [][2]string{{"advisor_review", "agent-end"}, {"advisor_wait", "1"}, {"advisor_wake", "blocker"}} {
		if err := a.Apply(kv[0], kv[1]); err != nil {
			t.Fatalf("%s = %s: %v", kv[0], kv[1], err)
		}
		if got := a.Values()[kv[0]]; got != kv[1] {
			t.Fatalf("%s reads %q, want %q", kv[0], got, kv[1])
		}
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"# mine", "maxNotesPerUpdate: 2", "enabled: true", "reviewMode: agent-end", `syncBacklog: "1"`, "continueSeverity: blocker"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.yml lacks %q:\n%s", want, text)
		}
	}
	for _, kv := range [][2]string{{"advisor_review", "sometimes"}, {"advisor_wait", "30s"}, {"advisor_wake", "nitpick"}} {
		if err := a.Apply(kv[0], kv[1]); err == nil {
			t.Fatalf("%s took %q", kv[0], kv[1])
		}
	}
	// off keeps the review mode for the next time
	if err := a.Apply("advisor_review", "off"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(path, "advisor.reviewMode"); v != "agent-end" || a.Values()["advisor_review"] != "off" {
		t.Fatalf("off: reviewMode %q, field %q", v, a.Values()["advisor_review"])
	}
	for _, k := range []string{"advisor_review", "advisor_wait", "advisor_wake"} {
		if err := a.Apply(k, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"advisor.enabled", "advisor.syncBacklog", "advisor.continueSeverity"} {
		if v, ok := edit.GetYAML(path, k); ok {
			t.Fatalf("%s left at %q", k, v)
		}
	}
	if v, _ := edit.GetYAML(path, "advisor.maxNotesPerUpdate"); v != "2" {
		t.Fatalf("the user's maxNotesPerUpdate became %q", v)
	}
}
