package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// openBotUser is openbot-custom-providers-v1.json as OpenBot saves it
// (CustomProviderStore, v0.58.0): an LM Studio endpoint with no key, and
// one with a key sealed in secret that runs on Pi.
const openBotUser = `{
  "version": 1,
  "providers": [
    {
      "id": "lm-studio",
      "name": "LM Studio",
      "baseUrl": "http://127.0.0.1:1234/v1",
      "models": [{"id": "qwen3-coder-30b", "name": "Qwen3 Coder 30B", "contextWindow": 262144}],
      "secret": null
    },
    {
      "id": "work",
      "name": "Work proxy",
      "baseUrl": "https://llm.example.com/v1",
      "models": [{"id": "gpt-5", "name": "gpt-5"}],
      "secret": "djEwAAAAsealedbyelectronsafestorage==",
      "harness": "pi"
    }
  ]
}
`

// openBotEntryDoc is a providers entry with OpenBot's fields alone
// (entrySchema), so a field of magpie's own fails to decode.
type openBotEntryDoc struct {
	ID      *string `json:"id"`
	Name    *string `json:"name"`
	BaseURL *string `json:"baseUrl"`
	Models  *[]struct {
		ID            *string `json:"id"`
		Name          *string `json:"name"`
		ContextWindow *int    `json:"contextWindow"`
	} `json:"models"`
	Secret  *string `json:"secret"`
	Harness *string `json:"harness"`
}

// openBotAgentModel is isAgentModel (ipc-agent-identity.ts): what a
// composed provider/model id must be, or OpenBot drops the whole list.
var openBotAgentModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\],=-]*$`)

// openBotRead is the file's magpie entry, checked as OpenBot reads one
// (fileSchema, then isCustomProviderSummary, which fails closed on the
// whole list): version 1, the required fields there and nothing else, at
// most 64 models with ids and names it takes and windows in its range;
// and the user's entries, as written.
func openBotRead(t *testing.T, path string) (magpie openBotEntryDoc, others []json.RawMessage) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version   *int              `json:"version"`
		Providers []json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Version == nil || *doc.Version != 1 || doc.Providers == nil {
		t.Fatalf("not a file OpenBot reads (version 1 and providers required): %v\n%s", err, b)
	}
	found := false
	for _, raw := range doc.Providers {
		var e openBotEntryDoc
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&e); err != nil {
			t.Fatalf("not OpenBot's entry: %v\n%s", err, raw)
		}
		if e.ID == nil || e.Name == nil || e.BaseURL == nil || e.Models == nil {
			t.Fatalf("entry without a required field: %s", raw)
		}
		if *e.ID != magpieID {
			others = append(others, raw)
			continue
		}
		if found {
			t.Fatalf("two entries of magpie's: %s", b)
		}
		found, magpie = true, e
	}
	if !found {
		t.Fatalf("no entry of magpie's: %s", b)
	}
	if len(*magpie.Name) > 80 {
		t.Errorf("name %q", *magpie.Name)
	}
	if n := len(*magpie.Models); n > 64 {
		t.Errorf("%d models, OpenBot takes 64", n)
	}
	for _, m := range *magpie.Models {
		if m.ID == nil || m.Name == nil {
			t.Fatalf("model without id or name: %s", b)
		}
		if id := magpieID + "/" + *m.ID; len(*m.ID) > 128 || len(id) > 160 || !openBotAgentModel.MatchString(id) {
			t.Errorf("model id %q OpenBot refuses", *m.ID)
		}
		if len([]rune(*m.Name)) > 160 {
			t.Errorf("model name %q", *m.Name)
		}
		if w := m.ContextWindow; w != nil && (*w < 1024 || *w > 100_000_000) {
			t.Errorf("%s: window %d", *m.ID, *w)
		}
	}
	return magpie, others
}

// OpenBot (nightly-labs/openbot) lists the user's own endpoints in its
// model picker, from openbot-custom-providers-v1.json in its userData
// folder. magpie adds itself as the endpoint "magpie" on the gateway's
// Chat Completions, keyless, keeps the user's entries as they were and the
// name, harness and key they give its entry, writes nothing when nothing
// changed, and off takes only its own entry out.
func TestOpenBotProvider(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	a := openBot(home, cfg)
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "openbot" && x.Name == "OpenBot" }) {
		t.Fatal("OpenBot is not among the agents")
	}
	if a.Detected() {
		t.Fatal("found with nothing of it here")
	}
	os.MkdirAll(a.Dir, 0o755)
	if !a.Detected() {
		t.Fatal("not found with its userData folder")
	}
	path := a.Path
	if filepath.Base(path) != "openbot-custom-providers-v1.json" || filepath.Dir(path) != a.Dir {
		t.Fatalf("path %s", path)
	}
	os.WriteFile(path, []byte(openBotUser), 0o600)
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
	// the restart it needs, said while it runs
	running := Running
	t.Cleanup(func() { Running = running })
	Running = func(...string) bool { return true }
	if a.Notice() != noticeOpenBot.String() {
		t.Errorf("notice while it runs: %q", a.Notice())
	}
	Running = func(...string) bool { return false }
	if a.Notice() != "" {
		t.Errorf("notice while it doesn't: %q", a.Notice())
	}

	gw := gateway.URL()
	e, others := openBotRead(t, path)
	if *e.BaseURL != gw+"/agent/openbot/v1" || *e.Name != "magpie" || e.Secret != nil || e.Harness != nil {
		t.Errorf("entry: base %q name %q secret %v harness %v", *e.BaseURL, *e.Name, e.Secret, e.Harness)
	}
	var got, want []string
	for _, m := range *e.Models {
		got = append(got, *m.ID)
	}
	for _, m := range magpieModels("openbot") {
		want = append(want, m.ID)
	}
	if len(want) < 2 || !slices.Equal(got, want) {
		t.Errorf("models %v, want %v", got, want)
	}
	var user struct {
		Providers []json.RawMessage `json:"providers"`
	}
	json.Unmarshal([]byte(openBotUser), &user)
	if len(others) != 2 || !sameJSON(string(others[0]), user.Providers[0]) || !sameJSON(string(others[1]), user.Providers[1]) {
		t.Errorf("the user's entries: %s", others)
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

	// renamed, given a key and put on Pi in OpenBot, and a model gone: a
	// sync keeps the three and puts the model back
	var doc map[string]any
	json.Unmarshal(b, &doc)
	for _, x := range doc["providers"].([]any) {
		if m := x.(map[string]any); m["id"] == magpieID {
			m["name"], m["secret"], m["harness"] = "My magpie", "c2VhbGVk", "pi"
			m["models"] = m["models"].([]any)[:1]
		}
	}
	edited, _ := json.MarshalIndent(doc, "", "  ")
	os.WriteFile(path, edited, 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	e, _ = openBotRead(t, path)
	if *e.Name != "My magpie" || e.Secret == nil || *e.Secret != "c2VhbGVk" || e.Harness == nil || *e.Harness != "pi" {
		t.Errorf("the user's name, key and harness not kept: %+v", e)
	}
	if len(*e.Models) != len(want) {
		t.Errorf("models after sync: %d, want %d", len(*e.Models), len(want))
	}

	// off: only magpie's entry goes, the file stays
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("get after off: %q", f.Get())
	}
	b, _ = os.ReadFile(path)
	var after struct {
		Version   int               `json:"version"`
		Providers []json.RawMessage `json:"providers"`
	}
	json.Unmarshal(b, &after)
	if after.Version != 1 || len(after.Providers) != 2 || !sameJSON(string(after.Providers[0]), user.Providers[0]) || !sameJSON(string(after.Providers[1]), user.Providers[1]) {
		t.Errorf("after off: %s", b)
	}
	// a sync of an entry that isn't there writes none
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(path); !bytes.Equal(b, b2) {
		t.Errorf("a sync off wrote:\n%s", b2)
	}
}

// OpenBot not run yet: magpie makes the file with the version 1 OpenBot
// needs, and off takes the file away again.
func TestOpenBotMakesItsFile(t *testing.T) {
	home := syncHome(t)
	a := openBot(home, filepath.Join(home, ".config"))
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	_, others := openBotRead(t, a.Path)
	if len(others) != 0 {
		t.Errorf("others: %s", others)
	}
	b, _ := os.ReadFile(a.Path)
	var top map[string]json.RawMessage
	json.Unmarshal(b, &top)
	if len(top) != 2 || string(top["version"]) != "1" {
		t.Errorf("made: %s", b)
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Errorf("magpie's own file left behind: %v", err)
	}
}

// A file OpenBot can't read leaves it with no endpoints and every save
// refused, so it may be a newer OpenBot's: magpie leaves it as it is.
func TestOpenBotLeavesAlone(t *testing.T) {
	home := syncHome(t)
	a := openBot(home, filepath.Join(home, ".config"))
	os.MkdirAll(a.Dir, 0o755)
	for _, s := range []string{
		`{"version": 2, "providers": []}`,
		`{"providers": []}`,
		`{"version": "1", "providers": []}`,
		`{"version": 1}`,
		`{"version": 1, "providers": {}}`,
		`[]`,
		`{"version": 1,`,
	} {
		os.WriteFile(a.Path, []byte(s), 0o600)
		if err := a.Field("provider").Set(magpieID); err == nil {
			t.Errorf("%s: set", s)
		}
		if b, _ := os.ReadFile(a.Path); string(b) != s {
			t.Errorf("%s: written: %s", s, b)
		}
	}
}

// OpenBot drops every model of an endpoint when one would not do: more than
// 64, an id it won't compose as magpie/<id>, a window out of its range. So
// magpie writes the first 64 it takes, and a window only inside the range.
func TestOpenBotModelsItTakes(t *testing.T) {
	syncHome(t)
	ms := []string{"has space", "ünï"}
	for i := range 70 {
		ms = append(ms, fmt.Sprintf("m%02d", i))
	}
	if err := provider.Save(provider.Provider{ID: "many", Name: "Many", Chat: "https://api.example/v1", Key: "k", Models: ms}); err != nil {
		t.Fatal(err)
	}
	all := magpieModels("openbot")
	if len(all) <= openBotMaxModels || !slices.ContainsFunc(all, func(m catalog.Model) bool { return m.ID == "has space" || m.ID == "many/has space" }) {
		t.Fatalf("fixture: %d models", len(all))
	}
	e := openBotEntry(gateway.URL())
	if len(e.Models) != openBotMaxModels {
		t.Errorf("%d models", len(e.Models))
	}
	for _, m := range e.Models {
		if !openBotAgentModel.MatchString(magpieID + "/" + m.ID) {
			t.Errorf("model %q OpenBot refuses", m.ID)
		}
		if m.ContextWindow != 0 && (m.ContextWindow < 1024 || m.ContextWindow > 100_000_000) {
			t.Errorf("%s: window %d", m.ID, m.ContextWindow)
		}
	}
}

// The file is where Electron puts OpenBot's userData on each system: app
// data joined with its product name.
func TestOpenBotDirPerOS(t *testing.T) {
	home := filepath.Join("h", "me")
	cfg := filepath.Join(home, ".cfg")
	for _, c := range []struct{ goos, appData, want string }{
		{"darwin", "", filepath.Join(home, "Library", "Application Support", "OpenBot")},
		{"linux", "", filepath.Join(cfg, "OpenBot")},
		{"windows", filepath.Join("R", "Roaming"), filepath.Join("R", "Roaming", "OpenBot")},
		{"windows", "", filepath.Join(home, "AppData", "Roaming", "OpenBot")},
	} {
		if got := openBotDirOn(c.goos, home, cfg, c.appData); got != c.want {
			t.Errorf("%s (APPDATA %q): %s, want %s", c.goos, c.appData, got, c.want)
		}
	}
	if got := openBotDir(home, cfg); got != openBotDirOn(runtime.GOOS, home, cfg, os.Getenv("APPDATA")) {
		t.Errorf("here: %s", got)
	}
}
