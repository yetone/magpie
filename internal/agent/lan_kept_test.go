package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// An agent the user pointed at magpie on another machine (coeo91 on
// Discord: the LAN address, which went back to 127.0.0.1 after an update or
// a restart) keeps that address and the key it had there through the sync
// every start of magpie runs (SyncCatalog), for every agent magpie writes
// its models into; the sync still brings the models up to date.
func TestSyncKeepsAnAddressOnAnotherMachine(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	// Craft Agents, once it has run and saved its config
	craftFixture(t, home)
	t.Setenv("ASTRBOT_ROOT", "")
	writeFile(t, filepath.Join(home, ".astrbot", "data", "cmd_config.json"), astrbotConfig)
	// QwenPaw, once it has written its agents' settings
	qwenpawFixture(t, home)
	setPort(t, 3591)
	for _, a := range All() {
		// Alma is told through its running app, and Cursor's local
		// gateway is started by launchctl: neither is in the sandbox
		if a.Check == nil || a.Launch != nil || a.WSL != "" || a.Native != nil || a.ID == "alma" || a.ID == "cursorlocal" {
			continue
		}
		var key, want string
		for _, g := range a.Fields {
			for _, o := range g.Options(a.Values()) {
				if want == "" && (o.Ref == "fake/m1" || o.Value == magpieID) {
					key, want = g.Key, o.Value
				}
			}
		}
		if want == "" {
			t.Fatalf("%s: no field takes magpie's models", a.ID)
		}
		if err := a.Apply(key, want); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
	}
	// the user points each file at the NAS's magpie, with its key
	moved := filesWith(home, "127.0.0.1:3591")
	if len(moved) < 20 {
		t.Fatalf("only %d files have the gateway's address: %v", len(moved), moved)
	}
	keys := map[string]int{}
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		b = []byte(strings.ReplaceAll(string(b), "127.0.0.1:3591", "192.168.50.10:3591"))
		kvs := keyValues(b)
		for i := len(kvs) - 1; i >= 0; i-- {
			if kv := kvs[i]; keeping().ours(kv.value) {
				v := "nas-key"
				if strings.HasPrefix(kv.value, "Bearer ") {
					v = "Bearer nas-key"
				}
				b = append(b[:kv.start:kv.start], append([]byte(v), b[kv.end:]...)...)
			}
		}
		keys[p] = strings.Count(string(b), "nas-key")
		os.WriteFile(p, b, 0o644)
	}
	if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	synced := 0
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		s, name := string(b), strings.TrimPrefix(p, home)
		if strings.Contains(s, "m2") {
			synced++
		}
		if strings.Contains(s, "127.0.0.1:3591") {
			t.Errorf("%s is back on this machine's gateway", name)
		}
		if !strings.Contains(s, "192.168.50.10:3591") {
			t.Errorf("%s lost the NAS's address", name)
		}
		if n := strings.Count(s, "nas-key"); n < keys[p] {
			t.Errorf("%s has the NAS's key %d times, not %d", name, n, keys[p])
		}
		for _, kv := range keyValues(b) {
			if keeping().ours(kv.value) {
				t.Errorf("%s has magpie's own key back: %q", name, kv.value)
			}
		}
	}
	if synced < len(moved)/2 {
		t.Errorf("the sync wrote the new model into %d of %d files", synced, len(moved))
	}
}

// What the sync writes is left as it is where the file says nothing sure of
// another machine: the gateway's address on this machine (another port, or
// localhost) follows the gateway, as does one a WSL distro reached Windows
// at; with two addresses gone, which went where can't be told.
func TestKeptAwayOnlyWhatIsSure(t *testing.T) {
	syncHome(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3591")
	dir := t.TempDir()
	now := `{"p":{"baseUrl":"http://127.0.0.1:3591/v1","apiKey":"magpie"}}`
	for _, c := range []struct{ name, was, want string }{
		{"nas", `{"p":{"baseUrl":"http://192.168.1.20:3425/v1","apiKey":"nas-key"}}`,
			`{"p":{"baseUrl":"http://192.168.1.20:3425/v1","apiKey":"nas-key"}}`},
		{"nas, magpie's key", `{"p":{"baseUrl":"http://nas.local:3425/v1","apiKey":"magpie"}}`,
			`{"p":{"baseUrl":"http://nas.local:3425/v1","apiKey":"magpie"}}`},
		{"old port", `{"p":{"baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie"}}`, now},
		{"localhost", `{"p":{"baseUrl":"http://localhost:3591/v1","apiKey":"other"}}`, now},
		{"two gone", `{"p":{"baseUrl":"http://192.168.1.20:3425/v1","apiKey":"a"},"q":{"baseUrl":"http://192.168.1.21:3425/v1"}}`, now},
		{"wsl", `{"p":{"baseUrl":"http://172.29.0.1:3591/v1","apiKey":"lan"}}`, now},
	} {
		wsl.Lock()
		wsl.seen = map[string]*distro{"Ubuntu": {Gateway: "172.30.0.1", Was: []string{"172.29.0.1"}}}
		wsl.Unlock()
		p := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".json")
		os.WriteFile(p, []byte(c.was), 0o644)
		if got := string(keeping().away(p, []byte(now))); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// a model of the user's own on this machine (Ollama's) is no sign of
	// magpie here
	p := filepath.Join(dir, "ollama.json")
	os.WriteFile(p, []byte(`{"o":{"baseUrl":"http://localhost:11434/v1"},"p":{"baseUrl":"http://192.168.1.20:3425/v1","apiKey":"nas-key"}}`), 0o644)
	if got, want := string(keeping().away(p, []byte(`{"o":{"baseUrl":"http://localhost:11434/v1"},"p":{"baseUrl":"http://127.0.0.1:3591/v1","apiKey":"magpie"}}`))),
		`{"o":{"baseUrl":"http://localhost:11434/v1"},"p":{"baseUrl":"http://192.168.1.20:3425/v1","apiKey":"nas-key"}}`; got != want {
		t.Errorf("beside Ollama: %s, want %s", got, want)
	}
	// Qwen Code writes its key on its own, before the models
	p = filepath.Join(dir, "qwen.json")
	os.WriteFile(p, []byte(`{"env":{"MAGPIE_QWEN_API_KEY":"nas-key"},"m":{"baseUrl":"http://192.168.1.20:3425/v1"}}`), 0o644)
	if got := string(keeping().away(p, []byte(`{"env":{"MAGPIE_QWEN_API_KEY":"magpie"},"m":{"baseUrl":"http://192.168.1.20:3425/v1"}}`))); !strings.Contains(got, `"nas-key"`) {
		t.Errorf("the key alone went back to magpie's: %s", got)
	}
	wsl.Lock()
	wsl.seen = nil
	wsl.Unlock()
}

// The keys kept.away carries over are those a file keeps what the agent
// sends the gateway in, in each format magpie writes, and never a model's
// token limit.
func TestKeyValuesFormats(t *testing.T) {
	in := strings.Join([]string{
		`"apiKey": "magpie-qoder",`,
		`api_key = "magpie"`,
		`  apiKey: magpie`,
		`GEMINI_API_KEY=magpie`,
		`"Authorization": "Bearer magpie"`,
		`http_headers = { "x-openai-actor-authorization" = "magpie" }`,
		`"max_tokens": 8192,`,
		`"name": "magpie"`,
		`"token": {"x": 1},`,
	}, "\n")
	var got []string
	for _, kv := range keyValues([]byte(in)) {
		got = append(got, in[kv.start:kv.end])
	}
	want := []string{"magpie-qoder", "magpie", "magpie", "magpie", "Bearer magpie", "magpie"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}
