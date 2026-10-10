package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/usage"
)

// Prime Agent (#1528) is an agent of its own, wired as Pi is but in
// ~/.prime/agent: a model through magpie sets settings.json's
// defaultProvider/defaultModel and magpie's provider in models.json there,
// beside the user's own providers and settings. The provider's key names
// Prime Agent, whose requests carry no User-Agent; Disconnect puts both
// files back as they were, byte for byte.
func TestPrimeAgent(t *testing.T) {
	home := syncHome(t)
	var a *Agent
	for _, x := range All() {
		if x.ID == "prime-agent" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("no prime-agent in All()")
	}
	dir := filepath.Join(home, ".prime", "agent")
	settings, models := filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
	if a.Name != "Prime Agent" || a.Icon != "prime-agent" || a.Bin != "prime-agent" || a.Dir != dir || a.Path != settings || a.Detected() {
		t.Fatalf("%+v", a)
	}
	os.MkdirAll(dir, 0o755)
	// the user's own, as they wrote them: comments aside, Prime Agent
	// takes Pi's JSON
	userSettings := "{\n  \"theme\": \"dark\",\n  \"defaultProvider\": \"xai\",\n  \"defaultModel\": \"grok-4.6\",\n  \"allowedModels\": [\"xai/grok-4.6\", \"magpie/relay/glm-4.6\"],\n  \"subagentDefaultModel\": \"xai/grok-4.6\"\n}\n"
	userModels := "{\n  \"providers\": {\n    \"mine\": {\n      \"baseUrl\": \"http://127.0.0.1:9/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"MINE_KEY\",\n      \"models\": [{\"id\": \"m1\", \"contextWindow\": 64000}]\n    }\n  }\n}\n"
	writeFile(t, settings, userSettings)
	writeFile(t, models, userModels)
	if !a.Detected() || a.Field("model").Get() != "xai/grok-4.6" {
		t.Fatalf("detected %v, model %q", a.Detected(), a.Field("model").Get())
	}

	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"defaultProvider": "magpie", "defaultModel": "relay/glm-4.6",
		"theme": "dark", "subagentDefaultModel": "xai/grok-4.6", "allowedModels.0": "xai/grok-4.6"} {
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
	if mine := file.Providers["mine"]; mine == nil || mine["apiKey"] != "MINE_KEY" {
		t.Fatalf("their provider:\n%s", readFile(models))
	}
	got := file.Providers["magpie"]
	if got["baseUrl"] != gateway.URL()+"/v1" || got["api"] != "openai-completions" || got["apiKey"] != "magpie-prime-agent" {
		t.Fatalf("models:\n%s", readFile(models))
	}
	// Pi's block, but for the key: the fields Prime Agent's models.json
	// takes, of the types it takes (pa-core models/custom.rs)
	want := magpieProviderJSON("pi").(map[string]any)
	want["apiKey"] = gateway.TokenFor("prime-agent")
	wb, _ := json.Marshal(want)
	if b, _ := json.Marshal(got); string(b) != string(wb) {
		t.Fatalf("not Pi's block:\n got %s\nwant %s", b, wb)
	}
	if c := a.Check(); c != "" {
		t.Fatal(c)
	}
	// a key magpie no longer gives it is off
	edit.SetJSON(models, edit.KV{Path: "providers.magpie.apiKey", Value: gateway.Token})
	if a.Check() == "" {
		t.Fatal("the shared key reads as Prime Agent's")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if k, _ := edit.GetJSON(models, "providers.magpie.apiKey"); k != "magpie-prime-agent" || a.Check() != "" {
		t.Fatalf("sync left %q: %s", k, a.Check())
	}
	for _, other := range []string{".pi", ".omo"} {
		if _, err := os.Stat(filepath.Join(home, other)); !os.IsNotExist(err) {
			t.Fatalf("~/%s was touched: %v", other, err)
		}
	}

	// its requests, which say nothing of it but the key, are its: the
	// gateway reads the agent off magpie-<id> (gateway.agentOf)
	if got := usage.AgentOf("prime-agent"); got != "prime-agent" {
		t.Fatalf("usage: %q", got)
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
}

// Connect and Disconnect from the Agents page, with nothing of the user's
// in models.json, leave it as it was too.
func TestPrimeAgentRoundTrip(t *testing.T) {
	home := syncHome(t)
	a := primeAgent(home)
	dir := filepath.Join(home, ".prime", "agent")
	userSettings := []byte("{\n  \"defaultProvider\": \"prime-inference\",\n  \"defaultModel\": \"glm-5.3\",\n  \"enabledModels\": [\"prime-inference/*\"]\n}\n")
	writeFile(t, filepath.Join(dir, "settings.json"), string(userSettings))
	for i := 0; i < 2; i++ {
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		if !a.Wired() || a.Check() != "" {
			t.Fatalf("not connected:\n%s", readFile(a.Path))
		}
		// a model outside enabledModels would never start
		if v, _ := edit.GetJSON(a.Path, "enabledModels"); !bytes.Contains([]byte(v), []byte("magpie/")) {
			t.Fatalf("enabledModels: %s", v)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(a.Path); !bytes.Equal(b, userSettings) {
			t.Fatalf("round %d:\n%s", i, b)
		}
		// models.json wasn't there, and isn't again; nor is a copy left
		for _, f := range []string{"models.json", "models.json.before-magpie", "settings.json.before-magpie"} {
			if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
				t.Fatalf("round %d: %s left: %v", i, f, err)
			}
		}
	}
}

// What the user changes while it is connected stands: Disconnect takes
// magpie out of it and leaves their change, rather than putting back the
// copy from before.
func TestPrimeAgentKeepsWhatChangedWhileConnected(t *testing.T) {
	home := syncHome(t)
	a := primeAgent(home)
	settings := filepath.Join(home, ".prime", "agent", "settings.json")
	writeFile(t, settings, `{"defaultProvider": "prime-inference", "defaultModel": "glm-5.3"}`)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(settings, edit.KV{Path: "theme", Value: "light"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"theme": "light", "defaultProvider": "prime-inference", "defaultModel": "glm-5.3"} {
		if v, _ := edit.GetJSON(settings, k); v != want {
			t.Fatalf("%s = %q:\n%s", k, v, readFile(settings))
		}
	}
	if _, err := os.Stat(settings + ".before-magpie"); !os.IsNotExist(err) {
		t.Fatalf("copy left: %v", err)
	}
}

// PRIME_AGENT_CODING_AGENT_DIR moves its folder, "~" in it standing for
// home, as its own agent_dir does; a relative one is not taken, and a
// distro's Prime Agent is at its home whatever this machine's says.
func TestPrimeAgentDir(t *testing.T) {
	home := syncHome(t)
	own := filepath.Join(home, "own")
	def := filepath.Join(home, ".prime", "agent")
	for _, c := range []struct{ env, want string }{
		{"", def},
		{own, own},
		{"~", home},
		{"~/pa", filepath.Join(home, "pa")},
		{"rel/dir", def},
	} {
		t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", c.env)
		if a := primeAgent(home); a.Dir != c.want || a.Path != filepath.Join(c.want, "settings.json") {
			t.Errorf("%+v: %s", c, a.Dir)
		}
	}
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", own)
	root := t.TempDir()
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.prime/agent": true}, Mirrored: true, Running: true}
	a := wslAgent(wslKindOf("prime-agent"), d)
	if a.ID != "prime-agent@wsl:Ubuntu" || a.Name != "Prime Agent · WSL Ubuntu" || a.Dir != filepath.Join(root, "home", "me", ".prime", "agent") {
		t.Fatalf("%+v", a)
	}
}
