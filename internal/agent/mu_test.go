package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/usage"
)

// mu (qybaihe/mu, xteam3301 on X) is Pi in ~/.mu/agent: a model through
// magpie sets settings.json's defaultProvider/defaultModel and magpie's
// provider in models.json there, beside the user's own. Its requests say
// pi/<version>, so the provider's key names it; Disconnect puts both files
// back byte for byte.
func TestMu(t *testing.T) {
	home := syncHome(t)
	var a *Agent
	for _, x := range All() {
		if x.ID == "mu" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("no mu in All()")
	}
	dir := filepath.Join(home, ".mu", "agent")
	settings, models := filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
	if a.Name != "mu" || a.Icon != "mu" || a.Bin != "mu" || a.Dir != dir || a.Path != settings || a.Detected() {
		t.Fatalf("%+v", a)
	}
	if f, err := Find("mu-agent"); err != nil || f.ID != "mu" {
		t.Fatalf("mu-agent: %v %v", f, err)
	}
	// as `mu setup` leaves them, with a judge of its own beside them
	userSettings := "{\n  \"defaultProvider\": \"deepseek\",\n  \"defaultModel\": \"deepseek-v4-pro\",\n  \"defaultThinkingLevel\": \"high\"\n}\n"
	userModels := "{\n  \"providers\": {\n    \"deepseek\": {\n      \"baseUrl\": \"https://api.deepseek.com/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"DEEPSEEK_API_KEY\",\n      \"models\": [{\"id\": \"deepseek-v4-pro\"}]\n    }\n  }\n}\n"
	judge := "{\"judge\": {\"provider\": \"deepseek\", \"model\": \"deepseek-v4-flash\"}}\n"
	writeFile(t, settings, userSettings)
	writeFile(t, models, userModels)
	writeFile(t, filepath.Join(home, ".mu", "mu.json"), judge)
	if !a.Detected() || a.Field("model").Get() != "deepseek/deepseek-v4-pro" {
		t.Fatalf("detected %v, model %q", a.Detected(), a.Field("model").Get())
	}

	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"defaultProvider": "magpie", "defaultModel": "relay/glm-4.6"} {
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
	if theirs := file.Providers["deepseek"]; theirs == nil || theirs["apiKey"] != "DEEPSEEK_API_KEY" {
		t.Fatalf("their provider:\n%s", readFile(models))
	}
	got := file.Providers["magpie"]
	want := magpieProviderJSON("pi").(map[string]any)
	want["apiKey"] = gateway.TokenFor("mu")
	wb, _ := json.Marshal(want)
	if b, _ := json.Marshal(got); string(b) != string(wb) || got["apiKey"] != "magpie-mu" {
		t.Fatalf("not Pi's block with mu's key:\n got %s\nwant %s", b, wb)
	}
	if c := a.Check(); c != "" {
		t.Fatal(c)
	}
	edit.SetJSON(models, edit.KV{Path: "providers.magpie.apiKey", Value: gateway.Token})
	if a.Check() == "" {
		t.Fatal("the shared key reads as mu's")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if k, _ := edit.GetJSON(models, "providers.magpie.apiKey"); k != "magpie-mu" || a.Check() != "" {
		t.Fatalf("sync left %q: %s", k, a.Check())
	}
	for _, other := range []string{".pi", ".omo", ".prime", ".kyrn"} {
		if _, err := os.Stat(filepath.Join(home, other)); !os.IsNotExist(err) {
			t.Fatalf("~/%s was touched: %v", other, err)
		}
	}
	if usage.AgentOf("mu") != "mu" || usage.AgentOf("mu-agent") != "mu" {
		t.Fatalf("usage: %q %q", usage.AgentOf("mu"), usage.AgentOf("mu-agent"))
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Fatal("still connected")
	}
	if b := readFile(settings); b != userSettings {
		t.Fatalf("settings.json not as it was:\n%s", b)
	}
	if b := readFile(models); b != userModels {
		t.Fatalf("models.json not as it was:\n%s", b)
	}
	if b := readFile(filepath.Join(home, ".mu", "mu.json")); b != judge {
		t.Fatalf("its judge changed:\n%s", b)
	}
}

// mu's agent folder is found as its launcher finds it (agentDirFor and
// muHome in kyrn/bin/mu.mjs): MU_AGENT_DIR, KYRN_AGENT_DIR,
// MU_CODING_AGENT_DIR, KYRN_CODING_AGENT_DIR in that order, else agent/ in
// ~/.mu, or in ~/.kyrn while that is a folder and ~/.mu isn't. A distro's
// is at ~/.mu/agent whatever this machine's says.
func TestMuDir(t *testing.T) {
	home := syncHome(t)
	def := filepath.Join(home, ".mu", "agent")
	if a := mu(home); a.Dir != def {
		t.Fatalf("default %s", a.Dir)
	}
	// a machine not moved from kyrn yet
	os.MkdirAll(filepath.Join(home, ".kyrn", "agent"), 0o755)
	if a := mu(home); a.Dir != filepath.Join(home, ".kyrn", "agent") || a.Path != filepath.Join(home, ".kyrn", "agent", "settings.json") {
		t.Fatalf("kyrn: %s", a.Dir)
	}
	os.MkdirAll(filepath.Join(home, ".mu"), 0o755)
	if a := mu(home); a.Dir != def {
		t.Fatalf("~/.mu beside ~/.kyrn: %s", a.Dir)
	}

	vars := []string{"MU_AGENT_DIR", "KYRN_AGENT_DIR", "MU_CODING_AGENT_DIR", "KYRN_CODING_AGENT_DIR"}
	for i, k := range vars {
		// each one wins over those after it
		for _, after := range vars[i+1:] {
			t.Setenv(after, filepath.Join(home, "not-"+after))
		}
		own := filepath.Join(home, "own-"+k)
		t.Setenv(k, own)
		if a := mu(home); a.Dir != own {
			t.Errorf("%s: %s", k, a.Dir)
		}
		t.Setenv(k, "~/m")
		if a := mu(home); a.Dir != filepath.Join(home, "m") {
			t.Errorf("%s=~/m: %s", k, a.Dir)
		}
		for _, v := range vars {
			t.Setenv(v, "")
		}
	}
	t.Setenv("MU_AGENT_DIR", "rel/dir")
	if a := mu(home); a.Dir != def {
		t.Errorf("relative: %s", a.Dir)
	}

	t.Setenv("MU_AGENT_DIR", filepath.Join(home, "own"))
	root := t.TempDir()
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.mu/agent": true}, Mirrored: true, Running: true}
	k := wslKindOf("mu")
	if k.bin != "" {
		t.Fatalf("a distro's mu found by its command: %q", k.bin)
	}
	a := wslAgent(k, d)
	if a.ID != "mu@wsl:Ubuntu" || a.Name != "mu · WSL Ubuntu" || a.Dir != filepath.Join(root, "home", "me", ".mu", "agent") {
		t.Fatalf("%+v", a)
	}
}

// mu is maildir-utils' command too (Homebrew's and the distros' mu, a
// compiled program): that one is not taken for mu-agent, which is its
// kyrn/bin/mu.mjs, through npm's link or a shim that names it.
func TestMuNotMaildirUtils(t *testing.T) {
	home := syncHome(t)
	bin := filepath.Join(home, "bin")
	maildir := filepath.Join(home, "brew", "mu")
	if runtime.GOOS == "windows" {
		maildir += ".exe"
	}
	// a compiled program, never run here
	os.MkdirAll(filepath.Dir(maildir), 0o755)
	if err := os.WriteFile(maildir, []byte("\x7fELF\x02\x01\x01 maildir-utils mu index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !notMuAgent(maildir) {
		t.Fatal("maildir-utils' mu taken for mu-agent")
	}
	pkg := filepath.Join(home, "lib", "node_modules", "mu-agent", "kyrn", "bin", "mu.mjs")
	writeFile(t, pkg, "#!/usr/bin/env node\nimport { main } from \"./main.mjs\";\n")
	link := filepath.Join(bin, "mu")
	os.MkdirAll(bin, 0o755)
	if runtime.GOOS != "windows" {
		if err := os.Symlink(pkg, link); err != nil {
			t.Fatal(err)
		}
		if notMuAgent(link) {
			t.Fatal("npm's link to mu.mjs not taken")
		}
	}
	// npm's Windows shim
	shim := filepath.Join(home, "npm", "mu.cmd")
	writeFile(t, shim, "@ECHO off\r\n\"%_prog%\" \"%dp0%\\node_modules\\mu-agent\\kyrn\\bin\\mu.mjs\" %*\r\n")
	if notMuAgent(shim) {
		t.Fatal("npm's mu.cmd not taken")
	}
	if !notMuAgent(filepath.Join(home, "nothing")) {
		t.Fatal("a mu that can't be read taken")
	}

	// on PATH, maildir-utils' mu alone leaves mu not installed
	t.Setenv("PATH", filepath.Dir(maildir))
	a := mu(home)
	if a.Detected() || a.binsOwn(binAt(a.Bin)) {
		t.Fatal("maildir-utils' mu detected as mu")
	}
}
