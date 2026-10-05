package gui

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestLocalProviderDiscovery(t *testing.T) {
	home := sandboxHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	importAppsRoutes(mux)
	check := func(sources ...string) map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/importapps/discovery", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("discovery: %d %s", w.Code, w.Body)
		}
		var got []map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		// Exact response shape: app names and opaque fingerprints only.
		if got == nil || len(got) != len(sources) {
			t.Fatalf("discovery = %#v, want sources %v", got, sources)
		}
		fingerprints := map[string]string{}
		for i, c := range got {
			b, err := hex.DecodeString(c["fingerprint"])
			if len(c) != 2 || c["source"] != sources[i] || err != nil || len(b) != 32 {
				t.Fatalf("unexpected candidate: %#v", c)
			}
			fingerprints[c["source"]] = c["fingerprint"]
		}
		return fingerprints
	}
	manualItem := func(source, ref string) provider.AppImport {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/importapps", nil))
		var sources []provider.AppSource
		if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil {
			t.Fatal(err)
		}
		for _, s := range sources {
			if s.ID == source {
				for _, it := range s.Items {
					if it.Ref == ref && it.Skip == "" {
						return it
					}
				}
			}
		}
		t.Fatalf("%s/%s is missing from manual import", source, ref)
		return provider.AppImport{}
	}
	check()
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "config.toml")
	claudeConfig := `{"env":{"ANTHROPIC_BASE_URL":"https://claude.example/v1","ANTHROPIC_AUTH_TOKEN":"secret-claude"}}`
	codexConfig := `[model_providers.relay]
base_url = "https://relay.example/v1"
experimental_bearer_token = "secret-codex"
[model_providers.relay.http_headers]
Authorization = "secret-header"
[model_providers.magpie]
base_url = "http://127.0.0.1:3448/v1"
experimental_bearer_token = "magpie"
[model_providers.missing_key]
base_url = "https://missing.example/v1"
env_key = "UNSET_DISCOVERY_KEY"
`
	write(claude, claudeConfig)
	write(codex, codexConfig)
	original := check("Claude Code", "Codex")
	if got := check("Claude Code", "Codex"); !reflect.DeepEqual(got, original) {
		t.Fatal("unchanged configurations changed fingerprints")
	}
	// The detailed picker can select exactly the configurations offered by
	// discovery, even though its API keys have already been masked.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/importapps", nil))
	var details []struct {
		Name  string `json:"name"`
		Items []struct {
			provider.AppImport
			Fingerprint string `json:"fingerprint"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, s := range details {
		for _, it := range s.Items {
			if it.Skip != "" || it.Status == "same" {
				continue
			}
			if it.Fingerprint != original[s.Name] || strings.HasPrefix(it.Provider.Key, "secret-") || len(it.Provider.Keys) != 0 {
				t.Fatalf("picker fingerprint or key masking differs for %s", s.Name)
			}
			matched++
		}
	}
	if matched != len(original) {
		t.Fatalf("picker matched %d candidates, want %d", matched, len(original))
	}
	if _, err := os.Stat(provider.Path()); !os.IsNotExist(err) {
		t.Fatalf("discovery wrote providers.json: %v", err)
	}
	for path, want := range map[string]string{claude: claudeConfig, codex: codexConfig} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("discovery changed %s: %v", path, err)
		}
	}
	// A source read earlier takes the same generated ID. Dismissals must
	// follow the original configuration, not settle's temporary ID.
	ccSwitch := filepath.Join(home, ".cc-switch", "config.json")
	write(ccSwitch, `{"codex":{"providers":{"other":{"name":"relay","settingsConfig":{"auth":{"OPENAI_API_KEY":"secret-other"},"config":"[model_providers.relay]\nbase_url = \"https://other.example/v1\""}}}}}`)
	if got := check("CC Switch", "Claude Code", "Codex"); got["Codex"] != original["Codex"] {
		t.Fatal("adding a source with the same ID changed the existing fingerprint")
	}
	if err := os.Remove(ccSwitch); err != nil {
		t.Fatal(err)
	}
	write(codex, codexConfig+"\n# only a comment\n")
	if got := check("Claude Code", "Codex"); !reflect.DeepEqual(got, original) {
		t.Fatal("a comment changed a configuration fingerprint")
	}
	for _, change := range [][2]string{{"secret-codex", "secret-rotated"}, {"relay.example", "changed.example"}, {"secret-header", "changed-header"}} {
		write(codex, strings.ReplaceAll(codexConfig, change[0], change[1]))
		got := check("Claude Code", "Codex")
		if got["Codex"] == original["Codex"] || got["Claude Code"] != original["Claude Code"] {
			t.Fatal("configuration changes must affect only their own fingerprint")
		}
	}
	write(codex, codexConfig)
	// Importing one configuration does not change another's fingerprint.
	existing := `{"id":"existing","name":"Existing","key":"secret-claude","anthropic":"https://claude.example/v1"}`
	write(provider.Path(), `{"providers":[`+existing+`]}`)
	if got := check("Codex"); got["Codex"] != original["Codex"] {
		t.Fatal("importing another configuration changed the remaining fingerprint")
	}
	// A collision without KeyOf is not selected by default. It remains in
	// manual import, but must not produce a hint with a disabled Import button.
	write(provider.Path(), `{"providers":[`+existing+`,{"id":"relay","name":"Relay","key":"different","responses":"https://different.example/v1"}]}`)
	check()
	if it := manualItem("codex", "relay"); it.Status != "taken" || it.KeyOf != "" {
		t.Fatal("expected an ID collision without a matching provider for another key")
	}
	// A collision that can join as another key IS selected by the picker.
	write(provider.Path(), `{"providers":[`+existing+`,{"id":"relay","name":"Relay","key":"different","responses":"https://relay.example/v1","headers":{"Authorization":"secret-header"}}]}`)
	check("Codex")
	if it := manualItem("codex", "relay"); it.Status != "taken" || it.KeyOf != "relay" {
		t.Fatal("expected the collision to be offered as another key")
	}
	write(provider.Path(), `{"providers":[`+existing+`]}`)
	write(codex, "invalid configuration")
	check()
	write(claude, "{")
	write(codex, codexConfig)
	check("Codex") // one unreadable source does not hide another

	// A disabled Alma provider is also left unticked in manual import.
	// Exercise the real SQLite reader, including enabling it and a mixed scan.
	write(codex, "invalid configuration")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	alma := filepath.Join(cfg, "alma", "chat_threads.db")
	if err := os.MkdirAll(filepath.Dir(alma), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", alma)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE providers (id TEXT PRIMARY KEY, name TEXT, type TEXT, api_key TEXT, models TEXT, base_url TEXT, enabled INTEGER, created_at TEXT, api_format TEXT, is_response_api INTEGER, custom_headers TEXT)`)
	exec(`INSERT INTO providers VALUES ('off','Alma Relay','custom','secret-alma','[]','https://alma.example/v1',0,'1','openai-chat',0,NULL)`)
	check()
	if it := manualItem("alma", "off"); it.Off != "turned off in Alma" || it.Status != "new" {
		t.Fatal("expected a disabled Alma provider to remain available manually")
	}
	exec(`UPDATE providers SET enabled = 1 WHERE id = 'off'`)
	check("Alma")
	exec(`UPDATE providers SET enabled = 0 WHERE id = 'off'`)
	write(codex, codexConfig)
	check("Codex")
}
