package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func museHome(t *testing.T) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, ".config", "muse", "settings.json")
}

func TestMuse(t *testing.T) {
	home, path := museHome(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	// a user's own: Meta's endpoint through a proxy, their model, an MCP
	// server and a TUI setting
	orig := `{
  "schema_version": 1,
  "model": "muse-spark",
  "endpoint_transport": {"base_url": "https://api.meta.ai/v1", "proxy": "http://127.0.0.1:8888"},
  "mcpServers": {"docs": {"type": "streamable-http", "url": "https://example/mcp", "mode": "optional"}},
  "tui": {"theme": "dark"}
}
`
	os.WriteFile(path, []byte(orig), 0o644)
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

	a := muse(filepath.Join(home, ".config"))
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "muse-spark" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "muse-spark magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	m := read()
	if !reflect.DeepEqual(m["endpoint_transport"], map[string]any{"base_url": gatewayV1(), "auth": "none"}) || m["model"] != "deepseek/pro" {
		t.Fatalf("set: %v", m)
	}
	if m["schema_version"] != 1.0 || !reflect.DeepEqual(m["mcpServers"], want["mcpServers"]) || !reflect.DeepEqual(m["tui"], want["tui"]) {
		t.Fatalf("the user's keys: %v", m)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("on magpie: %q %q", f.Get(), a.Check())
	}
	// another of magpie's models keeps what was stashed
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie/deepseek/flash" {
		t.Fatalf("again: %q", f.Get())
	}

	// auth moved back to the Meta sign-in's token: no longer as magpie set it
	edit.SetJSON(path, edit.KV{Path: "endpoint_transport.auth", Value: "bearer"})
	if d := a.Check(); !strings.Contains(d, "auth") {
		t.Fatalf("auth bearer: %q", d)
	}
	edit.SetJSON(path, edit.KV{Path: "endpoint_transport.auth", Value: "none"})
	if d := a.Check(); d != "" {
		t.Fatalf("auth none: %q", d)
	}

	// the endpoint moved elsewhere under magpie's model: still magpie's, and
	// setting it again keeps the user's stashed endpoint for the reset
	edit.SetJSON(path, edit.KV{Path: "endpoint_transport.base_url", Value: "http://127.0.0.1:9/v1"})
	if d := a.Check(); !strings.Contains(d, "base_url") || f.Get() != "magpie/deepseek/flash" {
		t.Fatalf("moved: %q %q", d, f.Get())
	}
	if err := f.Set("magpie/deepseek/flash"); err != nil || a.Check() != "" {
		t.Fatalf("again: %v %q", err, a.Check())
	}

	// back: the user's endpoint and model as they were
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if m := read(); !reflect.DeepEqual(m, want) {
		t.Fatalf("reset: %v\nwant %v", m, want)
	}
	if f.Get() != "muse-spark" {
		t.Fatalf("after reset: %q", f.Get())
	}

	// the requests are Muse Code's
	if got := usage.AgentOf("muse-build/1.4.2 (non-interactive; macos-aarch64; build 0)"); got != "muse" {
		t.Fatalf("user agent: %q", got)
	}
}

// With no settings file, magpie makes one Muse reads (it refuses one
// without its schema version) and takes it away again on reset.
func TestMuseNoSettings(t *testing.T) {
	home, path := museHome(t)
	a := muse(filepath.Join(home, ".config"))
	f := a.Field("model")
	if f.Get() != "" {
		t.Fatalf("none: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &m); err != nil || m["schema_version"] != 1.0 || m["model"] != "deepseek/pro" {
		t.Fatalf("made: %v %s", err, b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(path)
		t.Fatalf("left behind: %s", b)
	}

	// a model of the user's own picked on magpie's: endpoint taken out,
	// the file (theirs now) kept
	f.Set("magpie/deepseek/pro")
	if err := f.Set("muse-spark"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	m = nil
	if err := json.Unmarshal(b, &m); err != nil || m["model"] != "muse-spark" || m["endpoint_transport"] != nil {
		t.Fatalf("own model: %v %s", err, b)
	}
}
