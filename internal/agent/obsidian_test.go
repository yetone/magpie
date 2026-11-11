package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// obsCopilotUser is a vault's .obsidian/plugins/copilot/data.json as Copilot
// for Obsidian 4.0.19 saves it (src/settings/model.ts, CopilotSettings, and
// src/modelManagement/types/persisted.ts): an OpenAI key in the keychain,
// an Ollama endpoint with no key, models of both, the chat backend's
// picker and an agent backend's, and settings magpie has no business with.
const obsCopilotUser = `{
  "userId": "8f1c2d3e-0000-4000-8000-000000000001",
  "isPlusUser": false,
  "defaultChainType": "llm_chain",
  "defaultModelKey": "1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01",
  "embeddingModelKey": "text-embedding-3-small|openai",
  "temperature": 0.1,
  "maxTokens": 6000,
  "contextTurns": 15,
  "defaultSaveFolder": "copilot/copilot-conversations",
  "enableEncryption": false,
  "debug": false,
  "settingsVersion": 16,
  "_keychainVaultId": "vault-9a8b7c",
  "agentMode": {
    "byok": {},
    "activeBackend": "opencode",
    "backends": {"opencode": {"defaultModel": null}},
    "debugFullFrames": false,
    "offerFeedbackCards": true,
    "notificationSound": false,
    "notificationSoundId": "chime",
    "skills": {"folder": "copilot/skills"}
  },
  "providers": {
    "openai": {
      "providerId": "openai",
      "providerType": "openai",
      "displayName": "OpenAI",
      "apiKeyKeychainId": "copilot-provider-openai",
      "origin": {"kind": "byok", "catalogProviderId": "openai"},
      "addedAt": 1781000000000
    },
    "6d1e2f3a-4b5c-4d6e-8f70-819203a4b5c6": {
      "providerId": "6d1e2f3a-4b5c-4d6e-8f70-819203a4b5c6",
      "providerType": "openai-compatible",
      "displayName": "Ollama",
      "baseUrl": "http://localhost:11434/v1",
      "enableCors": true,
      "apiKeyKeychainId": null,
      "requiresApiKey": false,
      "origin": {"kind": "byok"},
      "addedAt": 1781000001000
    }
  },
  "configuredModels": [
    {
      "configuredModelId": "1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01",
      "providerId": "openai",
      "info": {"id": "gpt-5.1", "displayName": "GPT-5.1", "limits": {"context": 400000, "output": 128000}, "reasoning": true},
      "configuredAt": 1781000000500
    },
    {
      "configuredModelId": "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02",
      "providerId": "6d1e2f3a-4b5c-4d6e-8f70-819203a4b5c6",
      "info": {"id": "qwen3:8b", "displayName": "qwen3:8b"},
      "configuredAt": 1781000001500
    }
  ],
  "backends": {
    "chat": {"enabledModels": ["1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01", "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"]},
    "opencode": {"enabledModels": ["2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"]}
  },
  "copilotPlusCatalog": {"models": [], "defaultEnabledIds": []},
  "connectors": [],
  "customConnectors": []
}
`

// obsCopilotManifest is Copilot's manifest.json at version v.
func obsCopilotManifest(v string) string {
	return `{"id": "copilot", "name": "Copilot", "version": "` + v + `", "minAppVersion": "1.7.2", "author": "Logan Yang"}`
}

// obsidianVault makes a vault at dir listed in the obsidian.json in cfgDir,
// with Copilot at version v (none when v is "") and its data.json data
// (none when data is "").
func obsidianVault(t *testing.T, cfgDir, id, dir, v, data string) string {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".obsidian"), 0o755)
	if v != "" {
		os.MkdirAll(obsCopilotDir(dir), 0o755)
		os.WriteFile(filepath.Join(obsCopilotDir(dir), "manifest.json"), []byte(obsCopilotManifest(v)), 0o644)
		if data != "" {
			os.WriteFile(obsCopilotData(dir), []byte(data), 0o644)
		}
	}
	os.MkdirAll(cfgDir, 0o755)
	p := filepath.Join(cfgDir, "obsidian.json")
	doc := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &doc)
	}
	vs, _ := doc["vaults"].(map[string]any)
	if vs == nil {
		vs = map[string]any{}
	}
	vs[id] = map[string]any{"path": dir, "ts": 1781000000000, "open": true}
	doc["vaults"] = vs
	b, _ := json.Marshal(doc)
	os.WriteFile(p, b, 0o644)
	return obsCopilotData(dir)
}

// obsCopilotDoc is the settings file at path, read as Copilot reads it.
type obsCopilotDoc struct {
	DefaultModelKey  string                     `json:"defaultModelKey"`
	Providers        map[string]json.RawMessage `json:"providers"`
	ConfiguredModels []obsCopilotModel          `json:"configuredModels"`
	Backends         map[string]struct {
		EnabledModels []string `json:"enabledModels"`
	} `json:"backends"`
}

func obsReadCopilot(t *testing.T, path string) (obsCopilotDoc, map[string]json.RawMessage) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d obsCopilotDoc
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("not JSON Copilot reads: %v\n%s", err, b)
	}
	json.Unmarshal(b, &top)
	return d, top
}

// Copilot for Obsidian takes an OpenAI-compatible endpoint the user names.
// magpie adds itself as the provider "magpie", keyless, on the gateway's
// Chat Completions under /agent/obsidian, with every magpie model in the
// chat picker and the first as the default, in each vault with Copilot;
// it keeps every other setting as written, writes nothing when nothing
// changed, keeps what the user changes in Copilot, and off puts the
// default back and takes out only what is magpie's (shirotokiren on X).
func TestObsidianCopilot(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "obsidian" && x.Name == "Obsidian" }) {
		t.Fatal("Obsidian is not among the agents")
	}
	if a := obsidian(home, cfg); a.Detected() {
		t.Fatal("found with nothing of it here")
	}
	odir := obsidianDirs(home, cfg)[0]
	// Obsidian is here, with a vault that has no Copilot: nothing to set,
	// so no row (iEthan333 on X)
	obsidianVault(t, odir, "0000000000000000", filepath.Join(home, "NoAI"), "", "")
	if obsidian(home, cfg).Detected() {
		t.Fatal("found with only a vault that has no Copilot")
	}
	notes := filepath.Join(home, "Documents", "Notes")
	path := obsidianVault(t, odir, "a1b2c3d4e5f60718", notes, "4.0.19", obsCopilotUser)
	// a second vault without Copilot, and one listed that is gone
	obsidianVault(t, odir, "b2c3d4e5f6071829", filepath.Join(home, "Plain"), "", "")
	obsidianVault(t, odir, "c3d4e5f60718293a", filepath.Join(home, "Gone"), "", "")
	os.RemoveAll(filepath.Join(home, "Gone"))

	a := obsidian(home, cfg)
	if !a.Detected() {
		t.Fatal("not found with its config folder")
	}
	if a.Path != filepath.Join(odir, "obsidian.json") {
		t.Fatalf("path %s", a.Path)
	}
	f := a.Field("provider")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if f.Get() != magpieID {
		t.Fatalf("get after set: %q", f.Get())
	}
	if _, err := os.Stat(obsCopilotData(filepath.Join(home, "Plain"))); !os.IsNotExist(err) {
		t.Errorf("a vault without Copilot got its settings: %v", err)
	}

	running := Running
	t.Cleanup(func() { Running = running })
	Running = func(...string) bool { return true }
	if a.Notice() != noticeObsidian.String() {
		t.Errorf("notice while it runs: %q", a.Notice())
	}
	Running = func(...string) bool { return false }
	if a.Notice() != "" {
		t.Errorf("notice while it doesn't: %q", a.Notice())
	}

	d, top := obsReadCopilot(t, path)
	var p obsCopilotProvider
	if err := json.Unmarshal(d.Providers[magpieID], &p); err != nil {
		t.Fatalf("magpie's provider: %v %s", err, d.Providers[magpieID])
	}
	if p.ProviderID != magpieID || p.ProviderType != "openai-compatible" || p.BaseURL != gateway.URL()+"/agent/obsidian/v1" ||
		p.RequiresAPIKey || p.APIKeyKeychainID != nil || p.EnableCors || p.AddedAt == 0 || !sameJSON(string(p.Origin), map[string]string{"kind": "byok"}) {
		t.Errorf("provider: %+v", p)
	}
	var want, got, mine []string
	for _, m := range magpieModels("obsidian") {
		want = append(want, m.ID)
	}
	for _, m := range d.ConfiguredModels {
		if m.ProviderID == magpieID {
			got = append(got, m.Info.ID)
			mine = append(mine, m.ConfiguredModelID)
			if strings.Contains(m.ConfiguredModelID, "|") || m.Info.DisplayName == "" || m.ConfiguredAt == 0 {
				t.Errorf("model %+v", m)
			}
		}
	}
	if len(want) < 2 || !slices.Equal(got, want) {
		t.Errorf("models %v, want %v", got, want)
	}
	if d.DefaultModelKey != mine[0] {
		t.Errorf("default %q, want magpie's first %q", d.DefaultModelKey, mine[0])
	}
	chat := d.Backends["chat"].EnabledModels
	if !slices.Equal(chat, append([]string{"1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01", "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"}, mine...)) {
		t.Errorf("chat's picker: %v", chat)
	}
	if oc := d.Backends["opencode"].EnabledModels; !slices.Equal(oc, []string{"2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"}) {
		t.Errorf("an agent backend's picker: %v", oc)
	}
	// every other setting, and the user's providers and models, as written
	var user map[string]json.RawMessage
	json.Unmarshal([]byte(obsCopilotUser), &user)
	for k, v := range user {
		switch k {
		case "defaultModelKey", "providers", "configuredModels", "backends":
			continue
		}
		if !sameJSON(string(top[k]), v) {
			t.Errorf("%s: %s, was %s", k, top[k], v)
		}
	}
	var userProv map[string]json.RawMessage
	json.Unmarshal(user["providers"], &userProv)
	for k, v := range userProv {
		if !sameJSON(string(d.Providers[k]), v) {
			t.Errorf("the user's provider %s: %s", k, d.Providers[k])
		}
	}
	var userModels []json.RawMessage
	json.Unmarshal(user["configuredModels"], &userModels)
	var all []json.RawMessage
	json.Unmarshal(top["configuredModels"], &all)
	for i, m := range userModels {
		if !sameJSON(string(all[i]), m) {
			t.Errorf("the user's model %d: %s", i, all[i])
		}
	}

	// nothing changed: nothing written
	b, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(path); !bytes.Equal(b, b2) {
		t.Errorf("rewritten with nothing changed:\n%s\n%s", b, b2)
	}
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st.ModTime()) {
		t.Error("written with nothing changed")
	}

	// in Copilot the user renames magpie, turns its second model off in
	// the picker and picks their own default; magpie gets a model more.
	// A sync keeps all three and turns the new model on.
	var doc map[string]any
	json.Unmarshal(b, &doc)
	doc["providers"].(map[string]any)[magpieID].(map[string]any)["displayName"] = "My magpie"
	doc["defaultModelKey"] = "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"
	bk := doc["backends"].(map[string]any)["chat"].(map[string]any)
	bk["enabledModels"] = slices.DeleteFunc(bk["enabledModels"].([]any), func(x any) bool { return x == mine[1] })
	edited, _ := json.MarshalIndent(doc, "", "  ")
	os.WriteFile(path, edited, 0o644)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	d, _ = obsReadCopilot(t, path)
	json.Unmarshal(d.Providers[magpieID], &p)
	if p.DisplayName != "My magpie" {
		t.Errorf("the user's name not kept: %q", p.DisplayName)
	}
	if d.DefaultModelKey != "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02" {
		t.Errorf("the user's default not kept: %q", d.DefaultModelKey)
	}
	var now []string
	for _, m := range d.ConfiguredModels {
		if m.ProviderID == magpieID {
			now = append(now, m.ConfiguredModelID)
		}
	}
	if len(now) != len(mine)+1 {
		t.Fatalf("models after sync: %d, want %d", len(now), len(mine)+1)
	}
	chat = d.Backends["chat"].EnabledModels
	if slices.Contains(chat, mine[1]) {
		t.Errorf("a model the user turned off came back on: %v", chat)
	}
	if !slices.Contains(chat, now[len(now)-1]) || !slices.Contains(chat, mine[0]) {
		t.Errorf("the new model, or one left on, isn't on: %v", chat)
	}

	// off, with the default magpie's again: the old default comes back,
	// and only magpie's provider, models and their places go
	doc = nil
	b, _ = os.ReadFile(path)
	json.Unmarshal(b, &doc)
	doc["defaultModelKey"] = mine[0]
	edited, _ = json.MarshalIndent(doc, "", "  ")
	os.WriteFile(path, edited, 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("get after off: %q", f.Get())
	}
	d, top = obsReadCopilot(t, path)
	if d.DefaultModelKey != "1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01" {
		t.Errorf("default after off: %q", d.DefaultModelKey)
	}
	if !sameJSON(string(top["providers"]), user["providers"]) || !sameJSON(string(top["configuredModels"]), user["configuredModels"]) {
		t.Errorf("after off:\n%s\n%s", top["providers"], top["configuredModels"])
	}
	if c := d.Backends["chat"].EnabledModels; !slices.Equal(c, []string{"1b7c5a52-6d0e-4c1d-9b0e-3f6c2a1d0e01", "2c8d6b63-7e1f-4d2e-8c1f-4a7d3b2e1f02"}) {
		t.Errorf("chat's picker after off: %v", c)
	}
	// a sync with magpie off writes nothing
	b, _ = os.ReadFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(path); !bytes.Equal(b, b2) {
		t.Errorf("a sync off wrote:\n%s", b2)
	}
}

// A vault whose Copilot has never saved has no data.json yet: magpie makes
// one Copilot reads, with magpie's provider, models and chat picker.
func TestObsidianCopilotNoSettingsYet(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := obsidianVault(t, obsidianDirs(home, cfg)[0], "v1", filepath.Join(home, "V"), "4.1.0", "")
	if err := obsidian(home, cfg).Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	d, _ := obsReadCopilot(t, path)
	if len(d.ConfiguredModels) == 0 || d.DefaultModelKey != d.ConfiguredModels[0].ConfiguredModelID || len(d.Backends["chat"].EnabledModels) != len(d.ConfiguredModels) {
		t.Errorf("made: %+v", d)
	}
}

// A provider the user added by hand under the name magpie is theirs (the
// HandWired rule): it doesn't read as connected, Disconnect leaves it, and
// Connect says so and writes nothing.
func TestObsidianCopilotHandWiredMagpieIsTheirs(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}})
	own := `{"providers": {"magpie": {"providerId": "magpie", "providerType": "openai-compatible", "displayName": "magpie", "baseUrl": "https://magpie.example.com/v1", "enableCors": true, "apiKeyKeychainId": "k1", "requiresApiKey": true, "origin": {"kind": "byok"}, "addedAt": 1}}, "configuredModels": [{"configuredModelId": "m1", "providerId": "magpie", "info": {"id": "x"}, "configuredAt": 1}], "defaultModelKey": "m1"}`
	path := obsidianVault(t, obsidianDirs(home, cfg)[0], "v", filepath.Join(home, "V"), "4.0.19", own)
	a := obsidian(home, cfg)
	if a.Wired() {
		t.Error("the user's own provider named magpie reads as connected")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != own {
		t.Errorf("Disconnect wrote the user's provider: %s", b)
	}
	if err := a.Field("provider").Set(magpieID); err == nil || !strings.Contains(err.Error(), "of its own named magpie") {
		t.Errorf("Connect over the user's provider: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != own {
		t.Errorf("Connect wrote the user's provider: %s", b)
	}
}

// Copilot 3 keeps its models in activeModels, which magpie doesn't write,
// and no vault with Copilot is no place to put magpie: each is an error
// that says what to do, and nothing is written.
func TestObsidianCopilotRefuses(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}})
	odir := obsidianDirs(home, cfg)[0]
	obsidianVault(t, odir, "p", filepath.Join(home, "P"), "", "")
	if err := obsidian(home, cfg).Field("provider").Set(magpieID); err == nil || !strings.Contains(err.Error(), "install Copilot") {
		t.Errorf("no Copilot: %v", err)
	}
	old := `{"activeModels": [], "defaultModelKey": "gpt-4o|openai"}`
	path := obsidianVault(t, odir, "q", filepath.Join(home, "Q"), "2.9.5", old)
	if err := obsidian(home, cfg).Field("provider").Set(magpieID); err == nil || !strings.Contains(err.Error(), "update it") {
		t.Errorf("Copilot 2: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Errorf("Copilot 2's settings written: %s", b)
	}
	os.WriteFile(filepath.Join(obsCopilotDir(filepath.Join(home, "Q")), "manifest.json"), []byte(obsCopilotManifest("4.0.19")), 0o644)
	os.WriteFile(path, []byte(`{"providers": [`), 0o644)
	if err := obsidian(home, cfg).Field("provider").Set(magpieID); err == nil {
		t.Error("a file Copilot can't read: set")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"providers": [` {
		t.Errorf("written: %s", b)
	}
}

// obsidian.json is where Obsidian keeps it on each system: its Electron
// userData folder, and on Linux the Flatpak's and the Snap's too.
func TestObsidianDirsPerOS(t *testing.T) {
	home := filepath.Join("h", "me")
	cfg := filepath.Join(home, ".cfg")
	for _, c := range []struct {
		goos, appData string
		want          []string
	}{
		{"darwin", "", []string{filepath.Join(home, "Library", "Application Support", "obsidian")}},
		{"windows", filepath.Join("R", "Roaming"), []string{filepath.Join("R", "Roaming", "obsidian")}},
		{"windows", "", []string{filepath.Join(home, "AppData", "Roaming", "obsidian")}},
		{"linux", "", []string{
			filepath.Join(cfg, "obsidian"),
			filepath.Join(home, ".var", "app", "md.obsidian.Obsidian", "config", "obsidian"),
			filepath.Join(home, "snap", "obsidian", "current", ".config", "obsidian"),
		}},
	} {
		if got := obsidianDirsOn(c.goos, home, cfg, c.appData); !slices.Equal(got, c.want) {
			t.Errorf("%s (APPDATA %q): %v, want %v", c.goos, c.appData, got, c.want)
		}
	}
	if got := obsidianDirs(home, cfg); !slices.Equal(got, obsidianDirsOn(runtime.GOOS, home, cfg, os.Getenv("APPDATA"))) {
		t.Errorf("here: %v", got)
	}
}
