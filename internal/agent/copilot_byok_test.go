package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// providers is providers.json at path, parsed.
func copilotParsed(t *testing.T, path string) (provs, models []map[string]any, top map[string]any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFile(path)), &top); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, readFile(path))
	}
	for _, k := range []struct {
		key string
		to  *[]map[string]any
	}{{"providers", &provs}, {"models", &models}} {
		if l, ok := top[k.key].([]any); ok {
			for _, e := range l {
				*k.to = append(*k.to, e.(map[string]any))
			}
		}
	}
	return
}

func copilotSandbox(t *testing.T) (*Agent, string) {
	t.Helper()
	home := effortHome(t)
	t.Setenv("COPILOT_HOME", "")
	t.Setenv("COPILOT_PROVIDERS_CONFIG", "")
	return copilot(home, filepath.Join(home, ".config")), filepath.Join(home, ".copilot", "providers.json")
}

// Connecting Copilot CLI to magpie's model writes the provider Copilot reads
// from providers.json (BYOK, no GitHub sign-in) and picks it in settings.json
// as <provider>/<model>, the way Copilot's /model does (tony9759 on Discord).
func TestCopilotModelWritesProvidersJSON(t *testing.T) {
	cp, provs := copilotSandbox(t)
	if err := cp.Pick("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := cp.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("model reads %q", got)
	}
	ps, ms, _ := copilotParsed(t, provs)
	if len(ps) != 1 || ps[0]["name"] != "magpie" || ps[0]["type"] != "openai" ||
		ps[0]["baseUrl"] != gateway.URL()+"/v1" || ps[0]["apiKey"] != agentKey("copilot") {
		t.Fatalf("provider: %v", ps)
	}
	found := map[string]bool{}
	for _, m := range ms {
		if m["provider"] != "magpie" {
			t.Fatalf("model of another provider: %v", m)
		}
		found[m["id"].(string)] = true
	}
	if !found["deepseek/pro"] || !found["deepseek/flash"] {
		t.Fatalf("models: %v", found)
	}
	if !cp.Wired() {
		t.Fatal("not wired")
	}
	if msg := cp.Check(); msg != "" {
		t.Fatalf("check: %s", msg)
	}
}

// What the user keeps in providers.json, a provider of theirs, a model of
// theirs, a key magpie doesn't know, is left as it was by connect, by sync
// and by disconnect; their own model in settings.json is not touched until
// magpie's is picked, and Disconnect puts it back.
func TestCopilotKeepsTheUsersOwn(t *testing.T) {
	cp, provs := copilotSandbox(t)
	writeFile(t, provs, `{"theme":"x","providers":[{"name":"mine","type":"openai","baseUrl":"https://mine.example/v1","apiKey":"K"}],`+
		`"models":[{"id":"m1","provider":"mine","name":"Mine"}]}`)
	writeFile(t, cp.Path, `{"model":"mine/m1","effortLevel":"high"}`)
	if err := cp.Connect(); err != nil {
		t.Fatal(err)
	}
	if v := cp.Field("model").Get(); !strings.HasPrefix(v, "magpie/") {
		t.Fatalf("connect left the model on %q", v)
	}
	ps, ms, top := copilotParsed(t, provs)
	if top["theme"] != "x" || len(ps) != 2 || ps[0]["name"] != "mine" || ps[0]["apiKey"] != "K" {
		t.Fatalf("the user's own is lost:\n%s", readFile(provs))
	}
	if ms[0]["id"] != "m1" || ms[0]["provider"] != "mine" {
		t.Fatalf("the user's model is lost:\n%s", readFile(provs))
	}
	if v, _ := edit.GetJSON(cp.Path, "effortLevel"); v != "high" {
		t.Fatalf("effortLevel %q", v)
	}
	if err := cp.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := cp.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v := cp.Field("model").Get(); v != "mine/m1" {
		t.Fatalf("disconnect left the model on %q, not the one before", v)
	}
	ps, ms, top = copilotParsed(t, provs)
	if top["theme"] != "x" || len(ps) != 1 || ps[0]["name"] != "mine" || len(ms) != 1 || ms[0]["id"] != "m1" {
		t.Fatalf("after disconnect:\n%s", readFile(provs))
	}
	if cp.Wired() {
		t.Fatal("still wired")
	}
}

// A providers.json that isn't JSON is the user's, however it got that way:
// magpie says so and writes nothing.
func TestCopilotUnreadableProvidersJSONIsNotWrittenOver(t *testing.T) {
	cp, provs := copilotSandbox(t)
	writeFile(t, provs, `{"providers": [oops`)
	if err := cp.Field("model").Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("no error for a file that does not parse")
	}
	if got := readFile(provs); got != `{"providers": [oops` {
		t.Fatalf("the file was written over:\n%s", got)
	}
	if v, _ := edit.GetJSON(cp.Path, "model"); v != "" {
		t.Fatalf("settings.json picked %q for a provider that was not written", v)
	}
}

// Sync follows the catalog, drift is told, and COPILOT_PROVIDERS_CONFIG /
// COPILOT_HOME move the files as they move Copilot's.
func TestCopilotSyncCheckAndEnv(t *testing.T) {
	cp, provs := copilotSandbox(t)
	cp.Field("model").Set("magpie/deepseek/pro")
	// a model dropped by hand comes back with the sync; the user's pick stays
	edit.WriteAtomic(provs, []byte(`{"providers":[{"name":"magpie","type":"openai","baseUrl":"http://127.0.0.1:1/v1","apiKey":"x"}],"models":[]}`))
	if cp.Check() == "" {
		t.Fatal("a provider pointing elsewhere is not told")
	}
	if err := cp.Sync(); err != nil {
		t.Fatal(err)
	}
	_, ms, _ := copilotParsed(t, provs)
	if len(ms) < 2 || cp.Check() != "" {
		t.Fatalf("sync left %d models, check %q", len(ms), cp.Check())
	}

	other := filepath.Join(t.TempDir(), "mine.json")
	t.Setenv("COPILOT_PROVIDERS_CONFIG", other)
	home := t.TempDir()
	cp2 := copilot(home, filepath.Join(home, ".config"))
	if err := cp2.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if _, ms, _ := copilotParsed(t, other); len(ms) < 2 {
		t.Fatalf("COPILOT_PROVIDERS_CONFIG not written: %s", readFile(other))
	}
	t.Setenv("COPILOT_PROVIDERS_CONFIG", "")
	t.Setenv("COPILOT_HOME", filepath.Join(home, "ch"))
	if got := copilot(home, filepath.Join(home, ".config")).Path; got != filepath.Join(home, "ch", "settings.json") {
		t.Fatalf("COPILOT_HOME ignored: %s", got)
	}
}

// copilotCaps saves a provider whose models think at levels in vendors' own
// words or take images, and picks one in Copilot.
func copilotCaps(t *testing.T) (*Agent, string) {
	t.Helper()
	cp, provs := copilotSandbox(t)
	// images only where the model takes them itself, not where magpie's
	// vision model describes them (those say true as well)
	was := provider.Described
	provider.Described = func() bool { return false }
	t.Cleanup(func() { provider.Described = was })
	if err := provider.Save(provider.Provider{ID: "see", Name: "See", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"thinks", "looks"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("see", "https://example.test/v1", []catalog.Model{
		{ID: "thinks", Reasoning: true, Efforts: []string{"none", "light", "medium", "high", "xhigh", "ultra"}, InputModalities: []string{"text"}},
		{ID: "looks", Images: true, InputModalities: []string{"text", "image"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Field("model").Set("magpie/see/thinks"); err != nil {
		t.Fatal(err)
	}
	return cp, provs
}

// copilotSupports is capabilities.supports of magpie's model id in the file.
func copilotSupports(t *testing.T, provs, id string) map[string]any {
	t.Helper()
	_, ms, _ := copilotParsed(t, provs)
	for _, m := range ms {
		if m["id"] == id && m["provider"] == "magpie" {
			c, _ := m["capabilities"].(map[string]any)
			s, _ := c["supports"].(map[string]any)
			return s
		}
	}
	t.Fatalf("no %s in:\n%s", id, readFile(provs))
	return nil
}

// Copilot's /model lets the user pick a model's reasoning effort (←/→) only
// when its entry lists the levels in capabilities.supports.reasoning_effort;
// without them it shows "—" and sends none (TonyRL on Discord). Each of
// magpie's models carries its levels in Copilot's words, and whether it
// takes images.
func TestCopilotModelsSayTheirEffortsAndImages(t *testing.T) {
	_, provs := copilotCaps(t)
	th := copilotSupports(t, provs, "see/thinks")
	b, _ := json.Marshal(th["reasoning_effort"])
	if string(b) != `["none","low","medium","high","xhigh"]` || th["vision"] != false {
		t.Fatalf("see/thinks supports %v in:\n%s", th, readFile(provs))
	}
	lk := copilotSupports(t, provs, "see/looks")
	if _, ok := lk["reasoning_effort"]; ok || lk["vision"] != true {
		t.Fatalf("see/looks supports %v in:\n%s", lk, readFile(provs))
	}
}

// What the user adds to one of magpie's models (Copilot's /model Edit
// writes capabilities.supports.toolCalls; wireModel by hand) stays through a
// sync, while what magpie writes follows the catalog: levels magpie wrote
// for a model that no longer has them go.
func TestCopilotSyncKeepsWhatTheUserAddedToAModel(t *testing.T) {
	cp, provs := copilotCaps(t)
	var top map[string]any
	json.Unmarshal([]byte(readFile(provs)), &top)
	for _, e := range top["models"].([]any) {
		m := e.(map[string]any)
		sup := m["capabilities"].(map[string]any)["supports"].(map[string]any)
		switch m["id"] {
		case "see/thinks":
			m["wireModel"] = "see/thinks-2"
			m["metadata"] = map[string]any{"n": json.Number("9007199254740993")} // 2^53+1: a float64 rounds it
			sup["toolCalls"] = false
			sup["vision"] = true
		case "see/looks":
			sup["reasoning_effort"] = []string{"high"}
		}
	}
	b, _ := json.Marshal(top)
	writeFile(t, provs, string(b))
	if err := cp.Sync(); err != nil {
		t.Fatal(err)
	}
	_, ms, _ := copilotParsed(t, provs)
	wire := ""
	for _, m := range ms {
		if m["id"] == "see/thinks" {
			wire, _ = m["wireModel"].(string)
		}
	}
	th := copilotSupports(t, provs, "see/thinks")
	if wire != "see/thinks-2" || th["toolCalls"] != false || th["vision"] != false || th["reasoning_effort"] == nil {
		t.Fatalf("see/thinks after sync: wireModel %q, supports %v\n%s", wire, th, readFile(provs))
	}
	if !strings.Contains(readFile(provs), `"n": 9007199254740993`) {
		t.Fatalf("the user's number is not as they wrote it:\n%s", readFile(provs))
	}
	if lk := copilotSupports(t, provs, "see/looks"); lk["reasoning_effort"] != nil {
		t.Fatalf("see/looks kept levels it has none of: %v", lk)
	}
}
