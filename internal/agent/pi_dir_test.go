package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// #304: PI_CODING_AGENT_DIR moves Pi's agent folder (config.js,
// getAgentDir, "~" expanded), and a model set on Pi's row goes to
// settings.json and models.json there; ~/.pi is left alone. A relative
// one, or this machine's variable for a WSL distro's Pi, is not taken.
func TestPiAgentDir(t *testing.T) {
	home := syncHome(t)
	custom := filepath.Join(home, "dotfiles", "pi")
	for _, c := range []struct{ env, want string }{
		{"", filepath.Join(home, ".pi", "agent")},
		{custom, custom},
		{"~/dotfiles/pi", custom},
		{"rel/pi", filepath.Join(home, ".pi", "agent")},
	} {
		t.Setenv("PI_CODING_AGENT_DIR", c.env)
		if a := pi(home); a.Dir != c.want || a.Path != filepath.Join(c.want, "settings.json") {
			t.Errorf("%q: %s", c.env, a.Dir)
		}
	}

	t.Setenv("PI_CODING_AGENT_DIR", custom)
	var a *Agent
	for _, x := range All() {
		if x.ID == "pi" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("no pi in All()")
	}
	os.MkdirAll(custom, 0o755)
	os.WriteFile(filepath.Join(custom, "settings.json"), []byte(`{"theme": "dark", "defaultProvider": "xai", "defaultModel": "grok-4.6"}`), 0o644)
	if f := a.Field("model"); f.Get() != "xai/grok-4.6" {
		t.Fatalf("model %q", f.Get())
	}
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(custom, "settings.json")
	for k, want := range map[string]string{"defaultProvider": "magpie", "defaultModel": "relay/glm-4.6", "theme": "dark"} {
		if v, _ := edit.GetJSON(settings, k); v != want {
			t.Fatalf("%s = %q:\n%s", k, v, readFile(settings))
		}
	}
	if v, _ := edit.GetJSON(filepath.Join(custom, "models.json"), "providers.magpie.baseUrl"); v == "" {
		t.Fatalf("models.json:\n%s", readFile(filepath.Join(custom, "models.json")))
	}
	if c := a.Check(); c != "" {
		t.Fatal(c)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("~/.pi was touched: %v", err)
	}

	// a distro's Pi is at its home
	root := t.TempDir()
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.pi": true}, Mirrored: true, Running: true}
	if w := wslAgent(wslKindOf("pi"), d); w.Dir != filepath.Join(root, "home", "me", ".pi", "agent") {
		t.Fatalf("wsl: %s", w.Dir)
	}
}

// omp reads PI_CODING_AGENT_DIR too, but only with no profile, and as
// given; PI_CONFIG_DIR renames ~/.omp and OMP_PROFILE (else PI_PROFILE)
// picks profiles/<name>/agent in it (pi-utils dirs.ts).
func TestOmpAgentDir(t *testing.T) {
	home := syncHome(t)
	custom := filepath.Join(home, "omp-here")
	for _, c := range []struct{ agentDir, configDir, ompProfile, piProfile, want string }{
		{"", "", "", "", filepath.Join(home, ".omp", "agent")},
		{custom, "", "", "", custom},
		{"~/omp-here", "", "", "", filepath.Join(home, ".omp", "agent")},
		{"", ".omp-work", "", "", filepath.Join(home, ".omp-work", "agent")},
		{custom, "", "work", "", filepath.Join(home, ".omp", "profiles", "work", "agent")},
		{"", "", "", "home", filepath.Join(home, ".omp", "profiles", "home", "agent")},
		{"", "", "default", "", filepath.Join(home, ".omp", "agent")},
		{"", "", "Bad Name", "", filepath.Join(home, ".omp", "agent")},
	} {
		t.Setenv("PI_CODING_AGENT_DIR", c.agentDir)
		t.Setenv("PI_CONFIG_DIR", c.configDir)
		t.Setenv("OMP_PROFILE", c.ompProfile)
		if c.ompProfile == "" {
			os.Unsetenv("OMP_PROFILE")
		}
		t.Setenv("PI_PROFILE", c.piProfile)
		if a := omp(home); a.Dir != c.want || a.Path != filepath.Join(c.want, "config.yml") {
			t.Errorf("%+v: %s", c, a.Dir)
		}
	}
	// OMP_PROFILE set but empty is the default profile, PI_PROFILE aside
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("OMP_PROFILE", "")
	t.Setenv("PI_PROFILE", "home")
	if a := omp(home); a.Dir != filepath.Join(home, ".omp", "agent") {
		t.Errorf("empty OMP_PROFILE: %s", a.Dir)
	}

	t.Setenv("PI_CODING_AGENT_DIR", custom)
	os.Unsetenv("OMP_PROFILE")
	t.Setenv("PI_PROFILE", "")
	if err := omp(home).Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(filepath.Join(custom, "config.yml"), "modelRoles.default"); v != "magpie/relay/glm-4.6" {
		t.Fatalf("config.yml:\n%s", readFile(filepath.Join(custom, "config.yml")))
	}
	if _, err := os.Stat(filepath.Join(home, ".omp")); !os.IsNotExist(err) {
		t.Fatalf("~/.omp was touched: %v", err)
	}
}

// PI_CODING_AGENT_DIR may name Pi's folder, which is no sign of omp: its
// config.yml, its own folder or its command is.
func TestOmpDetectedWithPiDir(t *testing.T) {
	home := syncHome(t)
	pi := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(pi, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("PI_CODING_AGENT_DIR", pi)
	t.Setenv("PI_CONFIG_DIR", "")
	os.Unsetenv("OMP_PROFILE")
	t.Setenv("PI_PROFILE", "")
	if omp(home).Detected() {
		t.Error("Pi's folder is taken for omp's")
	}
	if err := os.WriteFile(filepath.Join(bin, "omp"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !omp(home).Detected() {
		t.Error("omp's command is not")
	}
	os.Remove(filepath.Join(bin, "omp"))
	if err := os.WriteFile(filepath.Join(pi, "config.yml"), []byte("modelRoles: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !omp(home).Detected() {
		t.Error("omp's config.yml is not")
	}

	// with no variable, ~/.omp/agent is omp's own
	os.Remove(filepath.Join(pi, "config.yml"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if omp(home).Detected() {
		t.Fatal("omp is here with nothing of it")
	}
	if err := os.MkdirAll(filepath.Join(home, ".omp", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !omp(home).Detected() {
		t.Error("~/.omp/agent is not taken for omp's")
	}
}
