package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"gopkg.in/yaml.v3"
)

// `magpie omp auto` asks for omp's own thinking level, auto (omp picks one
// each turn), and so goes to the thinking field: taken as a model, it made
// modelRoles.default the literal "auto", which omp has no model by.
func TestOmpAutoIsAThinkingLevel(t *testing.T) {
	groupsHome(t)
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	dir := filepath.Join(os.Getenv("HOME"), ".omp", "agent")
	os.MkdirAll(dir, 0o755)
	config := filepath.Join(dir, "config.yml")
	os.WriteFile(config, []byte("modelRoles:\n  default: anthropic/claude-opus-5-5:max\ndefaultThinkingLevel: high\n"), 0o644)
	a, err := agent.Find("omp")
	if err != nil {
		t.Fatal(err)
	}
	f := fieldForValue(a, "auto")
	if f == nil || f.Key != "effort" {
		t.Fatalf("auto went to %v, not the thinking level", f)
	}
	if err := set(a, f.Key, "auto"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(config)
	var c struct {
		ModelRoles           map[string]string `yaml:"modelRoles"`
		DefaultThinkingLevel string            `yaml:"defaultThinkingLevel"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.DefaultThinkingLevel != "auto" || c.ModelRoles["default"] != "anthropic/claude-opus-5-5:max" {
		t.Fatalf("config:\n%s", b)
	}
}
