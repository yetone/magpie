package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPaimon(t *testing.T) {
	home, _ := museHome(t)
	// one model whose window magpie knows, one it doesn't
	if err := catalog.SaveLive("deepseek", "https://api.deepseek.com/v1", []catalog.Model{
		{ID: "pro", Context: 200000}, {ID: "flash"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "paimon", "config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// a user's own, as Paimon's first launch and `paimon login` write it:
	// a model on Z.ai, a litellm proxy of their own, a window of their own,
	// and settings magpie has no business with
	orig := `{
  "model": "zai:glm-5.2",
  "providers": {
    "zai": {"api_key": "sk-zai"},
    "litellm": {"api_base": "http://proxy.example/v1", "api_key": "sk-proxy"}
  },
  "theme": "dracula",
  "compaction": {"enabled": true, "context_window": 64000},
  "skills": ["~/.claude/skills"]
}
`
	os.WriteFile(path, []byte(orig), 0o600)
	read := func() map[string]any {
		t.Helper()
		var m map[string]any
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return m
	}
	var want map[string]any
	json.Unmarshal([]byte(orig), &want)

	a := paimon(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "zai:glm-5.2" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "zai:glm-5.2 magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: what the user had is stashed once
		if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	m := read()
	if m["model"] != "litellm:deepseek/pro" {
		t.Fatalf("model: %v", m["model"])
	}
	ps := m["providers"].(map[string]any)
	lp, _ := ps["litellm"].(map[string]any)
	if lp["api_base"] != gatewayV1() || lp["api_key"] != agentKey("paimon") || len(lp) != 2 {
		t.Fatalf("magpie's provider: %v", lp)
	}
	if !reflect.DeepEqual(ps["zai"], want["providers"].(map[string]any)["zai"]) {
		t.Fatalf("user's provider: %v", ps)
	}
	c := m["compaction"].(map[string]any)
	if c["enabled"] != true || c["context_window"] != float64(200000) {
		t.Fatalf("compaction: %v", c)
	}
	if m["theme"] != "dracula" || !reflect.DeepEqual(m["skills"], want["skills"]) {
		t.Fatalf("other keys: %v", m)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" || !a.Wired() {
		t.Fatalf("on: %q %q %v", f.Get(), a.Check(), a.Wired())
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v", st.Mode())
	}

	// magpie's provider pointed elsewhere
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gatewayV1(), "http://elsewhere/v1", 1)), 0o600)
	if !strings.Contains(a.Check(), "api_base") {
		t.Fatalf("check: %q", a.Check())
	}
	os.WriteFile(path, b, 0o600)

	// another of magpie's models keeps what was stashed
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if read()["model"] != "litellm:deepseek/flash" {
		t.Fatalf("switch: %v", read()["model"])
	}
	// whose window magpie doesn't know: the user's, not pro's 200000
	if w := read()["compaction"].(map[string]any)["context_window"]; w != float64(64000) {
		t.Fatalf("switch window: %v", w)
	}

	// stepping out puts back what the user had, the window as a number
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restore:\n got %v\nwant %v", got, want)
	}

	// a model of the user's own while magpie's is on steps magpie out too
	f.Set("magpie/deepseek/flash")
	if err := f.Set("zai:glm-5.3"); err != nil {
		t.Fatal(err)
	}
	m = read()
	if m["model"] != "zai:glm-5.3" || !reflect.DeepEqual(m["providers"], want["providers"]) ||
		!reflect.DeepEqual(m["compaction"], want["compaction"]) {
		t.Fatalf("own over magpie: %v", m)
	}

	// a model picked in Paimon itself while magpie's provider stays: that
	// provider is still magpie's, never stashed as the user's
	f.Set("magpie/deepseek/flash")
	edJSON(t, path, "model", "zai:glm-5.2")
	if f.Get() != "zai:glm-5.2" || a.Wired() {
		t.Fatalf("picked in Paimon: %q %v", f.Get(), a.Wired())
	}
	f.Set("magpie/deepseek/pro")
	f.Set("")
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restore after a pick in Paimon:\n got %v\nwant %v", got, want)
	}

	// a file magpie made goes again
	os.Remove(path)
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie/deepseek/flash" {
		t.Fatalf("new: %q", f.Get())
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("made mode: %v", st.Mode())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(path)
		t.Fatalf("made file left: %s", b)
	}
}

// $PAIMON_CONFIG_HOME moves the file, as Paimon's config_root.
func TestPaimonConfigHome(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, "elsewhere")
	t.Setenv("PAIMON_CONFIG_HOME", dir)
	if a := paimon(home); a.Path != filepath.Join(dir, "config.json") {
		t.Fatalf("path: %s", a.Path)
	}
	t.Setenv("PAIMON_CONFIG_HOME", "relative")
	if a := paimon(home); a.Path != filepath.Join(home, ".config", "paimon", "config.json") {
		t.Fatalf("relative: %s", a.Path)
	}
}

// edJSON sets one key of a JSON file, as the agent itself would.
func edJSON(t *testing.T, path, k string, v any) {
	t.Helper()
	b, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m[k] = v
	b, _ = json.MarshalIndent(m, "", "  ")
	os.WriteFile(path, b, 0o600)
}
