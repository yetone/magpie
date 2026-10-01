package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/usage"
)

// OmO (#264) is an agent of its own, wired as Pi is but in ~/.omo/agent:
// a model through magpie sets settings.json's defaultProvider/defaultModel
// and magpie's provider in models.json there, the same block Pi gets, and
// Pi's ~/.pi is left alone; its own model takes magpie out again.
func TestOmo(t *testing.T) {
	home := syncHome(t)
	var a *Agent
	for _, x := range All() {
		if x.ID == "omo" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("no omo in All()")
	}
	dir := filepath.Join(home, ".omo", "agent")
	settings, models := filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
	if a.Name != "OmO" || a.Icon != "omo" || a.Bin != "omo" || a.Dir != dir || a.Path != settings || a.Detected() {
		t.Fatalf("%+v", a)
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(settings, []byte(`{"theme": "dark", "defaultProvider": "xai", "defaultModel": "grok-4.6"}`), 0o644)
	if !a.Detected() || a.Field("model").Get() != "xai/grok-4.6" {
		t.Fatalf("detected %v, model %q", a.Detected(), a.Field("model").Get())
	}

	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("high"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"defaultProvider": "magpie", "defaultModel": "relay/glm-4.6", "defaultThinkingLevel": "high", "theme": "dark"} {
		if v, _ := edit.GetJSON(settings, k); v != want {
			t.Fatalf("%s = %q:\n%s", k, v, readFile(settings))
		}
	}
	var file struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(readFile(models)), &file); err != nil {
		t.Fatal(err)
	}
	got := file.Providers["magpie"]
	if got["baseUrl"] != gateway.URL()+"/v1" || got["apiKey"] != gateway.Token {
		t.Fatalf("models:\n%s", readFile(models))
	}
	want, _ := json.Marshal(magpieProviderJSON("pi"))
	if b, _ := json.Marshal(got); string(b) != string(want) {
		t.Fatalf("not Pi's block:\n got %s\nwant %s", b, want)
	}
	if c := a.Check(); c != "" {
		t.Fatal(c)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("~/.pi was touched: %v", err)
	}

	if err := a.Field("model").Set("xai/grok-4.6"); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetJSON(settings, "defaultProvider"); p != "xai" {
		t.Fatalf("own:\n%s", readFile(settings))
	}

	// its requests say omo/<version>
	if got := usage.AgentOf("omo/5.1.4 (darwin; bun/1.3.0; arm64)"); got != "omo" {
		t.Fatalf("user agent: %q", got)
	}
}

// OMO_CODING_AGENT_DIR, else SENPI_CODING_AGENT_DIR, moves OmO's folder,
// as OmO's own agent-dir.js has it; a relative one is not taken.
func TestOmoDir(t *testing.T) {
	home := syncHome(t)
	own, senpi := filepath.Join(home, "own"), filepath.Join(home, "senpi")
	for _, c := range []struct{ omo, senpi, want string }{
		{"", "", filepath.Join(home, ".omo", "agent")},
		{own, senpi, own},
		{"", senpi, senpi},
		{"rel/dir", "", filepath.Join(home, ".omo", "agent")},
	} {
		t.Setenv("OMO_CODING_AGENT_DIR", c.omo)
		t.Setenv("SENPI_CODING_AGENT_DIR", c.senpi)
		if a := omo(home); a.Dir != c.want || a.Path != filepath.Join(c.want, "settings.json") {
			t.Errorf("%+v: %s", c, a.Dir)
		}
	}
	// a distro's OmO is at its home, whatever this machine's says
	t.Setenv("OMO_CODING_AGENT_DIR", own)
	root := t.TempDir()
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.omo": true}, Mirrored: true, Running: true}
	a := wslAgent(wslKindOf("omo"), d)
	if a.ID != "omo@wsl:Ubuntu" || a.Name != "OmO · WSL Ubuntu" || a.Dir != filepath.Join(root, "home", "me", ".omo", "agent") {
		t.Fatalf("%+v", a)
	}
}

// The WSL probe asks after OmO too.
func TestWSLProbeFindsOmo(t *testing.T) {
	for _, want := range []string{`[ -d "$HOME/.omo" ] && echo dir:.omo`, `p=$(command -v omo 2>/dev/null) && echo "bin:omo $p"`} {
		if !strings.Contains(wslProbeScript, want) {
			t.Errorf("probe lacks %q", want)
		}
	}
	if d := parseProbe("U", "home:/home/me\nbin:omo\n"); d == nil || !wslFound(*d) {
		t.Fatalf("%+v", d)
	}
}

// omo --version names the engine after OmO's own version; omo update is
// its updater, however it was installed.
func TestOmoCLI(t *testing.T) {
	if v := parseVersion("omo 5.1.4 (engine: senpi 2026.9.29-5)\n"); v != "5.1.4" {
		t.Fatalf("version %q", v)
	}
	u := howInstalled(cliSpecs["omo"], "/home/me/.bun/bin/omo")
	if u == nil || u.via != "self" || u.pkg != "omo-ai" || strings.Join(u.cmd, " ") != "/home/me/.bun/bin/omo update" {
		t.Fatalf("%+v", u)
	}
}
