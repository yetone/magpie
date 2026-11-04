package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// liveAgentDefault is config.json as K-brain's CLI writes config.Default
// (marshalConfig: its header, then MarshalIndent with two spaces).
const liveAgentDefault = `// k-brain configuration — JSONC: comments and trailing commas are allowed.
// providers use Pi's provider shape: name, api, models, baseUrl, and apiKey.
// defaultModel/defaultProvider pick a configured model route.
{
  "language": "en",
  "defaultModel": "model1",
  "compactModel": "model1",
  "providers": {
    "demo": {
      "name": "demo",
      "baseUrl": "",
      "api": "openai-completions",
      "apiKey": "",
      "activeModels": null,
      "models": [
        {
          "id": "model1",
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "model2",
          "contextWindow": 128000,
          "maxTokens": 8192
        }
      ]
    }
  }
}
`

// liveAgentSetup is a sandbox HOME with LiveAgent's root at
// ~/.liveagent, two models from a provider of the user's, and LiveAgent
// not running.
func liveAgentSetup(t *testing.T) (string, *Agent, []string) {
	t.Helper()
	home := syncHome(t)
	t.Setenv("LIVEAGENT_HOME", "")
	t.Setenv("K_BRAIN_HOME", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveAgentRunning = func() bool { return false } })
	liveAgentRunning = func() bool { return false }
	a := liveAgent(home)
	var want []string
	for _, m := range magpieModels("liveagent") {
		want = append(want, m.ID)
	}
	if len(want) < 2 {
		t.Fatalf("models %v", want)
	}
	return home, a, want
}

func liveAgentFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func liveAgentWrite(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// liveAgentJSON is the file as K-brain's parseConfigJSONC reads it: its
// comments gone, and an error for anything that isn't an object.
func liveAgentJSON(t *testing.T, s string) gjson.Result {
	t.Helper()
	b := jsonc.ToJSON([]byte(s))
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil || root == nil {
		t.Fatalf("not a JSON object K-brain reads: %v\n%s", err, s)
	}
	return gjson.ParseBytes(b)
}

func liveAgentIDs(r gjson.Result) []string {
	var ids []string
	for _, v := range r.Array() {
		if v.Type == gjson.String {
			ids = append(ids, v.String())
		} else {
			ids = append(ids, v.Get("id").String())
		}
	}
	return ids
}

// LiveAgent v2 lists the providers of K-brain's config.json in its model
// picker. magpie adds itself as "magpie" on the gateway's Anthropic
// Messages with the key naming LiveAgent, makes magpie's first model the
// pick when the pick is still K-brain's empty demo, keeps the rest of the
// file, writes nothing when nothing changed, keeps the user's name, models
// turned off, order and K-brain's fields through a sync, and off gives
// back the file it found.
func TestLiveAgentProvider(t *testing.T) {
	home, a, want := liveAgentSetup(t)
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "liveagent" && x.Name == "LiveAgent" }) {
		t.Fatal("LiveAgent is not among the agents")
	}
	if a.Path != filepath.Join(home, ".liveagent", "config.json") {
		t.Fatalf("path %s", a.Path)
	}
	if a.Detected() {
		t.Fatal("found with nothing there")
	}
	liveAgentWrite(t, a.Path, liveAgentDefault)
	if !a.Detected() {
		t.Fatal("not found with its data folder")
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

	gw := gateway.URL()
	got := liveAgentFile(t, a.Path)
	if !strings.HasPrefix(got, liveAgentDefault[:strings.Index(liveAgentDefault, "{")]) {
		t.Errorf("K-brain's header comments lost:\n%s", got)
	}
	r := liveAgentJSON(t, got)
	e := r.Get("providers.magpie")
	if e.Get("name").String() != "magpie" || e.Get("type").String() != "claude_code" || e.Get("api").String() != "anthropic-messages" ||
		e.Get("baseUrl").String() != gw || strings.HasSuffix(gw, "/v1") || e.Get("apiKey").String() != agentKeyAt("liveagent", gw) ||
		e.Get("activeModels").Type != gjson.Null || !e.Get("activeModels").Exists() {
		t.Errorf("entry: %s", e.Raw)
	}
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if ids := liveAgentIDs(e.Get("models")); !slices.Equal(ids, sorted) {
		t.Errorf("models %v, want %v", ids, sorted)
	}
	if ids := liveAgentIDs(e.Get("modelOrder")); !slices.Equal(ids, want) {
		t.Errorf("modelOrder %v, want %v", ids, want)
	}
	if r.Get("defaultModel").String() != want[0] || r.Get("defaultProvider").String() != "magpie" ||
		r.Get("compactModel").String() != want[0] || r.Get("compactProvider").String() != "magpie" {
		t.Errorf("picks still K-brain's empty demo: %s", got)
	}
	if r.Get("language").String() != "en" || r.Get("providers.demo.api").String() != "openai-completions" {
		t.Errorf("the rest of the file changed: %s", got)
	}

	// nothing changed: nothing written
	before, _ := os.Stat(a.Path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if again := liveAgentFile(t, a.Path); again != got {
		t.Errorf("rewritten with nothing changed:\n%s\n%s", got, again)
	}
	if after, _ := os.Stat(a.Path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("file touched with nothing changed")
	}

	// renamed, a model turned off, reordered and renamed in LiveAgent,
	// K-brain's metadata added, a model magpie has now missing: a sync
	// keeps the user's and K-brain's, and the new model is on and last
	last := want[len(want)-1]
	var models []json.RawMessage
	for _, m := range e.Get("models").Array() {
		switch m.Get("id").String() {
		case last:
			continue
		case want[0]:
			var mm map[string]any
			json.Unmarshal([]byte(m.Raw), &mm)
			mm["displayName"] = "Mine"
			models = append(models, rawJSON(mm))
		default:
			models = append(models, json.RawMessage(m.Raw))
		}
	}
	order := slices.Clone(want[:len(want)-1])
	slices.Reverse(order)
	mine, _ := json.Marshal(map[string]any{
		"name": "My magpie", "type": "claude_code", "baseUrl": gw, "api": "anthropic-messages", "apiKey": e.Get("apiKey").String(),
		"activeModels": []string{want[1]}, "modelOrder": order, "metadata": map[string]any{"providerOrder": 3}, "models": models,
	})
	var edited map[string]any
	json.Unmarshal(jsonc.ToJSON([]byte(got)), &edited)
	var mineV any
	json.Unmarshal(mine, &mineV)
	edited["providers"].(map[string]any)["magpie"] = mineV
	eb, _ := json.MarshalIndent(edited, "", "  ")
	liveAgentWrite(t, a.Path, string(eb))
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	e = liveAgentJSON(t, liveAgentFile(t, a.Path)).Get("providers.magpie")
	if e.Get("name").String() != "My magpie" || e.Get("metadata.providerOrder").Int() != 3 {
		t.Errorf("the user's name and K-brain's fields not kept: %s", e.Raw)
	}
	if ids := liveAgentIDs(e.Get("models")); !slices.Equal(ids, sorted) {
		t.Errorf("models after sync %v, want %v", ids, sorted)
	}
	if ids := liveAgentIDs(e.Get("activeModels")); !slices.Equal(ids, []string{want[1], last}) {
		t.Errorf("activeModels after sync %v, want %v", ids, []string{want[1], last})
	}
	if ids := liveAgentIDs(e.Get("modelOrder")); !slices.Equal(ids, append(order, last)) {
		t.Errorf("modelOrder after sync %v, want %v", ids, append(order, last))
	}
	if n := e.Get(`models.#(id=="` + want[0] + `").displayName`).String(); n != "Mine" {
		t.Errorf("the user's name for a model: %q", n)
	}

	// off: magpie's entry goes, the picks go back to K-brain's model1,
	// and the rest is the file it found
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("get after off: %q", f.Get())
	}
	if !jsonEqual(jsonc.ToJSON([]byte(liveAgentFile(t, a.Path))), jsonc.ToJSON([]byte(liveAgentDefault))) {
		t.Errorf("after off:\n%s\nwant\n%s", liveAgentFile(t, a.Path), liveAgentDefault)
	}
	// and on then off, untouched since, is the file byte for byte
	liveAgentWrite(t, a.Path, liveAgentDefault)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := liveAgentFile(t, a.Path); got != liveAgentDefault {
		t.Errorf("on then off:\n%s\nwant\n%s", got, liveAgentDefault)
	}
	// a sync with no entry there writes none
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := liveAgentFile(t, a.Path); got != liveAgentDefault {
		t.Errorf("a sync wrote magpie back:\n%s", got)
	}
}

// With no config.json, or the {} LiveAgent's backend makes on its first
// start, on writes magpie's provider and pick, and off takes away the
// file it made. A user's own provider and pick stay through on and off,
// and a file left with no provider gets K-brain's demo back, which
// K-brain's CLI reads as a config, where none would bring back its .bak.
func TestLiveAgentMakesItsFile(t *testing.T) {
	_, a, want := liveAgentSetup(t)
	f := a.Field("provider")
	for _, start := range []string{"", "{}"} {
		os.Remove(a.Path)
		if start != "" {
			liveAgentWrite(t, a.Path, start)
		}
		if err := f.Set(magpieID); err != nil {
			t.Fatal(err)
		}
		r := liveAgentJSON(t, liveAgentFile(t, a.Path))
		if r.Get("defaultModel").String() != want[0] || r.Get("defaultProvider").String() != "magpie" ||
			!r.Get("providers.magpie.models").IsArray() || r.Get("compactModel").Exists() {
			t.Errorf("from %q: %s", start, r.Raw)
		}
		if err := f.Set(""); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
			t.Errorf("from %q, off left %s", start, liveAgentFile(t, a.Path))
		}
	}

	// the user's provider and pick, and a setting: theirs stay
	const theirs = `{"language":"zh","defaultModel":"deepseek-chat","defaultProvider":"ds","providers":{"ds":{"name":"ds","baseUrl":"https://api.deepseek.com","api":"openai-completions","apiKey":"sk-u","activeModels":null,"models":[{"id":"deepseek-chat"}]}}}`
	liveAgentWrite(t, a.Path, theirs)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if r := liveAgentJSON(t, liveAgentFile(t, a.Path)); r.Get("defaultModel").String() != "deepseek-chat" || r.Get("defaultProvider").String() != "ds" {
		t.Errorf("the user's pick changed: %s", r.Raw)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := liveAgentFile(t, a.Path); got != theirs {
		t.Errorf("on then off:\n%s\nwant\n%s", got, theirs)
	}

	// picked in LiveAgent (default, task), then off: the picks go back to
	// K-brain's, the first model of the providers left
	liveAgentWrite(t, a.Path, theirs)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(liveAgentFile(t, a.Path), `"defaultModel":"deepseek-chat","defaultProvider":"ds"`, `"defaultModel":"`+want[1]+`","defaultProvider":"magpie","taskModel":"`+want[0]+`","taskProvider":"magpie"`, 1)
	liveAgentWrite(t, a.Path, s)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	r := liveAgentJSON(t, liveAgentFile(t, a.Path))
	if r.Get("providers.magpie").Exists() || r.Get("defaultModel").String() != "" || !r.Get("defaultModel").Exists() ||
		r.Get("defaultProvider").Exists() || r.Get("taskModel").Exists() || r.Get("taskProvider").Exists() ||
		r.Get("providers.ds.apiKey").String() != "sk-u" || r.Get("language").String() != "zh" {
		t.Errorf("after off: %s", r.Raw)
	}

	// only magpie's provider left, and a setting: off gives K-brain's
	// demo back, and its model1 as the pick
	liveAgentWrite(t, a.Path, `{"language":"zh"}`)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	r = liveAgentJSON(t, liveAgentFile(t, a.Path))
	if r.Get("providers.magpie").Exists() || r.Get("defaultModel").String() != "model1" || r.Get("defaultProvider").Exists() ||
		r.Get("providers.demo.api").String() != "openai-completions" || r.Get("language").String() != "zh" {
		t.Errorf("emptied file: %s", r.Raw)
	}
}

// A config.json K-brain wouldn't read as its providers, or one LiveAgent
// holds while it runs, is left as it is, on and off.
func TestLiveAgentLeavesAlone(t *testing.T) {
	_, a, _ := liveAgentSetup(t)
	f := a.Field("provider")
	for _, s := range []string{
		`{"providers":`,
		`{"models":{"x":{}},"providers":{}}`,
		`{"name":"mine","api":"openai-completions","baseUrl":"https://x","apiKey":"k","models":[]}`,
		`{"providers":[{"name":"mine"}]}`,
		`[]`,
	} {
		liveAgentWrite(t, a.Path, s)
		if err := f.Set(magpieID); err == nil {
			t.Errorf("set on %s: no error", s)
		}
		if err := a.Sync(); err != nil {
			t.Errorf("sync on %s: %v", s, err)
		}
		if got := liveAgentFile(t, a.Path); got != s {
			t.Errorf("%s changed to %s", s, got)
		}
	}

	liveAgentWrite(t, a.Path, liveAgentDefault)
	liveAgentRunning = func() bool { return true }
	if err := f.Set(magpieID); err != errLiveAgentRunning {
		t.Errorf("on while running: %v", err)
	}
	if got := liveAgentFile(t, a.Path); got != liveAgentDefault {
		t.Errorf("written while running: %s", got)
	}
	liveAgentRunning = func() bool { return false }
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	on := liveAgentFile(t, a.Path)
	liveAgentRunning = func() bool { return true }
	if err := f.Set(""); err != errLiveAgentRunning {
		t.Errorf("off while running: %v", err)
	}
	if got := liveAgentFile(t, a.Path); got != on {
		t.Errorf("written while running: %s", got)
	}
}

// LiveAgent's root: $LIVEAGENT_HOME, then $K_BRAIN_HOME, then the folder
// ~/.liveagent/data-dir.json points to, then ~/.liveagent; a relative
// path is not a root.
func TestLiveAgentDir(t *testing.T) {
	home := syncHome(t)
	def := filepath.Join(home, ".liveagent")
	moved := filepath.Join(home, "Data", "LiveAgent")
	for _, c := range []struct {
		live, kbrain, pointer, want string
	}{
		{"", "", "", def},
		{moved, "", "", moved},
		{"", moved, "", moved},
		{moved, filepath.Join(home, "other"), "", moved},
		{"rel", "", "", def},
		{"", "", `{"path":` + strings.TrimSpace(string(rawJSON(moved))) + `}`, moved},
		{"", "", `{"path":` + strings.TrimSpace(string(rawJSON(def))) + `}`, def},
		{"", "", `{"path":"rel"}`, def},
		{"", "", `not json`, def},
	} {
		t.Setenv("LIVEAGENT_HOME", c.live)
		t.Setenv("K_BRAIN_HOME", c.kbrain)
		os.Remove(filepath.Join(def, "data-dir.json"))
		if c.pointer != "" {
			liveAgentWrite(t, filepath.Join(def, "data-dir.json"), c.pointer)
		}
		if got := liveAgentDir(home); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
		if got := liveAgent(home).Path; got != filepath.Join(c.want, "config.json") {
			t.Errorf("%+v: path %s", c, got)
		}
	}
}

// LiveAgent's processes: the app and the k-brain backend it starts,
// wherever the bundle is.
func TestLiveAgentProcess(t *testing.T) {
	match := func(cmd string) bool {
		for _, p := range liveAgentProc {
			if regexp.MustCompile(p).MatchString(cmd) {
				return true
			}
		}
		return false
	}
	for _, cmd := range []string{
		"/Applications/LiveAgent.app/Contents/MacOS/liveagent",
		"/Users/u/Apps/My Apps/LiveAgent.app/Contents/MacOS/k-brain backend -listen 127.0.0.1:0 -data-dir /Users/u/.liveagent -parent-stdio",
		"/usr/bin/liveagent",
		"k-brain backend -listen 127.0.0.1:0",
	} {
		if !match(cmd) {
			t.Errorf("not LiveAgent: %s", cmd)
		}
	}
	for _, cmd := range []string{"vim /Users/u/.liveagent/config.json", "/usr/bin/liveagentd", "/opt/k-brain-tools/x", "grep liveagent"} {
		if match(cmd) {
			t.Errorf("taken for LiveAgent: %s", cmd)
		}
	}
}
