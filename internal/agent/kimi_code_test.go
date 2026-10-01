package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// #290: a kimi-cli user who moved to the new Kimi Code has both ~/.kimi and
// ~/.kimi-code; the new one reads only ~/.kimi-code, so that is where the
// model picked for it goes — ~/.kimi is left as it was.
func TestKimiCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://example.com/v1", Key: "k", Models: []string{"gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	cfg := `default_model = "kimi-code/kimi-for-coding"

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
api_key = ""

[models."kimi-code/kimi-for-coding"]
provider = "managed:kimi-code"
model = "kimi-for-coding"
max_context_size = 262144
`
	old := filepath.Join(home, ".kimi", "config.toml")
	path := filepath.Join(home, ".kimi-code", "config.toml")
	for _, p := range []string{old, path} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(cfg), 0o644)
	}
	a := kimi(home)
	if a.Path != path {
		t.Fatalf("path: %s, want %s", a.Path, path)
	}
	f := a.Field("model")
	if err := f.Set("magpie/relay/gemini-3.8-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(path, "default_model"); v != "magpie/relay/gemini-3.8-flash" {
		t.Fatalf("default_model in ~/.kimi-code: %q", v)
	}
	m, err := edit.GetTOMLTable(path, `models."magpie/relay/gemini-3.8-flash"`)
	if err != nil || m == nil || m["provider"] != "magpie" || !kimiToolUse(path) {
		t.Fatalf("model table: %v %v", m, err)
	}
	if b, _ := os.ReadFile(old); string(b) != cfg {
		t.Fatalf("~/.kimi changed:\n%s", b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "magpie") || !strings.Contains(string(b), `default_model = "kimi-code/kimi-for-coding"`) {
		t.Fatalf("reset:\n%s", b)
	}

	// $KIMI_CODE_HOME moves it; kimi-cli's alone is still kimi-cli's, and
	// with neither there the new one's is where a first start puts it
	t.Setenv("KIMI_CODE_HOME", filepath.Join(home, "kc"))
	if d, legacy := KimiDir(home); d != filepath.Join(home, "kc") || legacy {
		t.Fatalf("KIMI_CODE_HOME: %s %v", d, legacy)
	}
	t.Setenv("KIMI_CODE_HOME", "")
	os.RemoveAll(filepath.Join(home, ".kimi-code"))
	if d, legacy := KimiDir(home); d != filepath.Join(home, ".kimi") || !legacy {
		t.Fatalf("kimi-cli only: %s %v", d, legacy)
	}
	// kimi-cli refuses a capability it doesn't know
	if err := kimi(home).Field("model").Set("magpie/relay/gemini-3.8-flash"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetTOMLTable(old, `models."magpie/relay/gemini-3.8-flash"`); m == nil || kimiToolUse(old) {
		t.Fatalf("kimi-cli's model table: %v", m)
	}
	os.RemoveAll(filepath.Join(home, ".kimi"))
	if d, legacy := KimiDir(home); d != filepath.Join(home, ".kimi-code") || legacy {
		t.Fatalf("neither: %s %v", d, legacy)
	}
}

// kimiToolUse is whether a model table in the file says it calls tools.
func kimiToolUse(path string) bool {
	b, _ := os.ReadFile(path)
	return strings.Contains(string(b), `"tool_use"`)
}
