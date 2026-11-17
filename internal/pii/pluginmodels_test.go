package pii

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

func TestPrivacyVersionsChecked(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "w")
	for _, c := range []struct {
		name   string
		models any
		ok     bool
	}{
		{"none", nil, true},
		{"one", []any{map[string]any{"id": "v1", "name": "Mine", "path": abs}}, true},
		{"score", []any{map[string]any{"id": "v1", "name": "Mine", "path": abs, "minScore": 0.8, "revision": "r2"}}, true},
		{"relative path", []any{map[string]any{"id": "v1", "name": "Mine", "path": "w"}}, false},
		{"no name", []any{map[string]any{"id": "v1", "name": " ", "path": abs}}, false},
		{"bad id", []any{map[string]any{"id": "V/1", "name": "Mine", "path": abs}}, false},
		{"same id", []any{map[string]any{"id": "v1", "name": "A", "path": abs}, map[string]any{"id": "v1", "name": "B", "path": abs}}, false},
		{"score under floor", []any{map[string]any{"id": "v1", "name": "Mine", "path": abs, "minScore": Floor / 2}}, false},
		{"score of one", []any{map[string]any{"id": "v1", "name": "Mine", "path": abs, "minScore": 1}}, false},
		{"unknown field", []any{map[string]any{"id": "v1", "name": "Mine", "path": abs, "command": "/bin/sh"}}, false},
		{"not a list", "v1", false},
	} {
		opts := map[string]any{}
		if c.models != nil {
			opts["models"] = c.models
		}
		if _, err := parsePrivacyVersions(opts); (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// privacyPlugin is a plugin in a folder of the test's own whose launcher is
// the stand-in pf-serve (fakeServe, mode ok), with plugins.json listing it
// with models; it gives the plugin's folder and the stand-in's log.
func privacyPlugin(t *testing.T, models ...map[string]any) (string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pkg := map[string]any{"name": "privacy-test", "version": "1.0.0", "magpie": map[string]any{"privacyModels": map[string]any{"command": self}}}
	b, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "log")
	t.Setenv(fakeVar, "ok")
	t.Setenv(fakeLog, logPath)
	setPluginModels(t, dir, false, models...)
	t.Cleanup(func() {
		os.Remove(plugin.ListPath())
		for _, m := range registeredPluginModels() {
			m.Stop()
		}
	})
	return dir, logPath
}

// setPluginModels writes plugins.json with the plugin at dir, off or on, and
// models as its options.
func setPluginModels(t *testing.T, dir string, off bool, models ...map[string]any) {
	t.Helper()
	l := plugin.List{Plugins: []plugin.Entry{{Spec: dir, Off: off, Options: map[string]any{"models": models}}}}
	b, _ := json.Marshal(l)
	before := plugin.ListStamp()
	if err := os.MkdirAll(filepath.Dir(plugin.ListPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	for plugin.ListStamp() == before {
		// a write within the clock's tick of the last keeps its stamp
		if err := os.WriteFile(plugin.ListPath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if plugin.ListStamp() == before {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func weightsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "weights.gguf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func pluginModelIDs() []string {
	var ids []string
	for _, m := range Models() {
		if strings.HasPrefix(m.ID(), pluginModelPrefix) {
			ids = append(ids, m.ID())
		}
	}
	return ids
}

func countLines(t *testing.T, path, prefix string) int {
	t.Helper()
	b, _ := os.ReadFile(path)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// A plugin's model runs its launcher with the version's path last, finds
// what the launcher finds, and keeps running while only its name or
// minScore changes; a new path stops it and starts one on the new file.
func TestPluginModelRunsItsLauncher(t *testing.T) {
	w1 := weightsFile(t, "one")
	dir, logPath := privacyPlugin(t, map[string]any{"id": "v1", "name": "Mine", "path": w1})
	ids := pluginModelIDs()
	if len(ids) != 1 || !strings.HasSuffix(ids[0], "/v1") {
		t.Fatalf("plugin models %v, want one ending /v1", ids)
	}
	m := Of(ids[0])
	if !m.Installed() || m.ManagesWeights() || m.Now().Name != "Mine" || m.MinScore() != Floor {
		t.Fatalf("installed %v, manages %v, name %q, minScore %v", m.Installed(), m.ManagesWeights(), m.Now().Name, m.MinScore())
	}
	find := func(m Model, text string) {
		t.Helper()
		got, err := m.Find(context.Background(), []string{text})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || len(got[0]) != 1 || text[got[0][0].Start:got[0][0].End] != "Alice" {
			t.Fatalf("found %+v in %q", got, text)
		}
	}
	find(m, "hi Alice, one")
	if n := countLines(t, logPath, "model "+w1); n != 1 {
		t.Fatalf("launcher given %s %d times, want once:\n%s", w1, n, readFile(logPath))
	}
	gen := m.Generation()

	setPluginModels(t, dir, false, map[string]any{"id": "v1", "name": "Renamed", "path": w1, "minScore": 0.9})
	m2 := Of(ids[0])
	if m2.Generation() != gen || m2.Now().Name != "Renamed" || m2.MinScore() != 0.9 {
		t.Fatalf("after a rename: generation changed %v, name %q, minScore %v", m2.Generation() != gen, m2.Now().Name, m2.MinScore())
	}
	find(m2, "hi Alice, two")
	if n := countLines(t, logPath, "start "); n != 1 {
		t.Fatalf("a rename started the launcher again (%d starts)", n)
	}

	w2 := weightsFile(t, "two")
	setPluginModels(t, dir, false, map[string]any{"id": "v1", "name": "Renamed", "path": w2})
	m3 := Of(ids[0])
	if m3.Generation() == gen {
		t.Fatal("a new path kept the generation")
	}
	if !m.(*externalModel).retired.Load() {
		t.Fatal("the old model isn't retired")
	}
	find(m3, "hi Alice, three")
	if n := countLines(t, logPath, "model "+w2); n != 1 {
		t.Fatalf("launcher given %s %d times, want once:\n%s", w2, n, readFile(logPath))
	}
	if m.(*externalModel).backend.path() == m3.(*externalModel).backend.path() {
		t.Fatal("two generations share a disk cache")
	}
}

// Weights changed on disk with the options as they were are a new
// generation too.
func TestPluginModelWeightsEdited(t *testing.T) {
	w := weightsFile(t, "one")
	_, _ = privacyPlugin(t, map[string]any{"id": "v1", "name": "Mine", "path": w})
	id := pluginModelIDs()[0]
	m := Of(id).(*externalModel)
	gen := m.Generation()
	if err := os.WriteFile(w, []byte("longer weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.stamped.Store(0) // past the once a second it looks
	if got := Of(id); got.Generation() == gen || !m.retired.Load() {
		t.Fatalf("edited weights: generation changed %v, old retired %v", got.Generation() != gen, m.retired.Load())
	}
}

// A plugin turned off leaves its model unavailable, saying so, and a
// request on it isn't run.
func TestPluginModelOff(t *testing.T) {
	w := weightsFile(t, "one")
	dir, logPath := privacyPlugin(t, map[string]any{"id": "v1", "name": "Mine", "path": w})
	id := pluginModelIDs()[0]
	setPluginModels(t, dir, true, map[string]any{"id": "v1", "name": "Mine", "path": w})
	m := Of(id)
	if m.Installed() {
		t.Fatal("a plugin turned off left its model installed")
	}
	if s := m.Now(); s.Failed == "" || s.Installed {
		t.Fatalf("status %+v", s)
	}
	if _, err := m.Find(context.Background(), []string{"hi Alice"}); err == nil {
		t.Fatal("a request on it ran")
	}
	if n := countLines(t, logPath, "start "); n != 0 {
		t.Fatalf("launcher started %d times", n)
	}
	if len(pluginModelIDs()) != 0 {
		t.Fatal("still listed")
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
