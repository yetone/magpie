package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNanobot(t *testing.T) {
	home, _ := museHome(t)
	path := filepath.Join(home, ".nanobot", "config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// a user's own, as nanobot's onboard writes it: an endpoint of theirs
	// named with a slash in its model, a preset that would go over the
	// model, the window in snake_case (nanobot reads both), and a channel
	orig := `{
  "agents": {"defaults": {"workspace": "~/.nanobot/workspace", "model": "relay/glm-4.6", "provider": "relay", "context_window_tokens": 65536, "modelPreset": "fast"}},
  "modelPresets": {"fast": {"model": "openai/gpt-5-mini", "provider": "openai"}},
  "providers": {"relay": {"apiKey": "sk-relay", "apiBase": "https://relay.example/v1"}},
  "channels": {"telegram": {"enabled": true}}
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

	a := nanobot(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "relay/glm-4.6" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "relay/glm-4.6 magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: what the user had is stashed once
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	m := read()
	ps := m["providers"].(map[string]any)
	if !reflect.DeepEqual(ps["relay"], want["providers"].(map[string]any)["relay"]) {
		t.Fatalf("user's provider: %v", ps)
	}
	mp, _ := ps["magpie"].(map[string]any)
	if mp["apiBase"] != gatewayV1() || mp["apiKey"] == nil ||
		!reflect.DeepEqual(mp["extraHeaders"], map[string]any{"User-Agent": "nanobot"}) {
		t.Fatalf("magpie provider: %v", mp)
	}
	d := m["agents"].(map[string]any)["defaults"].(map[string]any)
	if d["model"] != "magpie/deepseek/pro" || d["provider"] != "magpie" || d["workspace"] != "~/.nanobot/workspace" {
		t.Fatalf("defaults: %v", d)
	}
	// neither the preset nor the user's window may go over magpie's model
	for _, k := range []string{"modelPreset", "model_preset", "context_window_tokens"} {
		if _, ok := d[k]; ok {
			t.Fatalf("%s left: %v", k, d)
		}
	}
	if !reflect.DeepEqual(m["modelPresets"], want["modelPresets"]) || !reflect.DeepEqual(m["channels"], want["channels"]) {
		t.Fatalf("other keys: %v", m)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("on: %q %q", f.Get(), a.Check())
	}

	// magpie's provider pointed elsewhere
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gatewayV1(), "http://elsewhere/v1", 1)), 0o644)
	if !strings.Contains(a.Check(), "apiBase") {
		t.Fatalf("check: %q", a.Check())
	}
	os.WriteFile(path, b, 0o644)

	// stepping out puts back what the user had; the window comes back as a
	// number, under the camelCase name nanobot writes
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	m = read()
	d = m["agents"].(map[string]any)["defaults"].(map[string]any)
	if d["model"] != "relay/glm-4.6" || d["provider"] != "relay" || d["modelPreset"] != "fast" ||
		d["contextWindowTokens"] != float64(65536) || d["workspace"] != "~/.nanobot/workspace" {
		t.Fatalf("restore defaults: %v", d)
	}
	if _, ok := m["providers"].(map[string]any)["magpie"]; ok {
		t.Fatalf("magpie provider left: %v", m["providers"])
	}

	// a model of the user's own while magpie's is on steps magpie out too
	f.Set("magpie/deepseek/flash")
	if err := f.Set("relay/glm-4.5"); err != nil {
		t.Fatal(err)
	}
	m = read()
	d = m["agents"].(map[string]any)["defaults"].(map[string]any)
	if d["model"] != "relay/glm-4.5" || d["provider"] != "relay" || d["modelPreset"] != "fast" {
		t.Fatalf("own over magpie: %v", d)
	}
	if _, ok := m["providers"].(map[string]any)["magpie"]; ok {
		t.Fatalf("magpie provider left: %v", m["providers"])
	}

	// a file magpie made goes again
	os.Remove(path)
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie/deepseek/flash" {
		t.Fatalf("new: %q", f.Get())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(path)
		t.Fatalf("made file left: %s", b)
	}
}
