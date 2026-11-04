package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// droidUser is a settings.json as droid leaves it for someone with a BYOK
// model of their own on a Factory model, and settings magpie has nothing
// to do with.
const droidUser = `{
  "customModels": [
    {
      "model": "kimi-k2-instruct",
      "id": "custom:Kimi-K2-[Groq]-0",
      "index": 0,
      "baseUrl": "https://api.groq.com/openai/v1",
      "apiKey": "${GROQ_API_KEY}",
      "displayName": "Kimi K2 [Groq]",
      "provider": "generic-chat-completion-api"
    }
  ],
  "sessionDefaultSettings": {
    "model": "claude-opus-4-7",
    "autonomyLevel": "low"
  },
  "diffMode": "unified"
}
`

func droidHome(t *testing.T) (home, path string) {
	t.Helper()
	home = syncHome(t)
	t.Setenv("FACTORY_HOME_OVERRIDE", "")
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro", "flash"}},
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	path = filepath.Join(home, ".factory", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	return home, path
}

// droidFile is settings.json read back: its custom models by id, and the
// model sessions start on.
func droidFile(t *testing.T, path string) (map[string]map[string]any, []string, map[string]any) {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	byID := map[string]map[string]any{}
	var order []string
	ms, _ := f["customModels"].([]any)
	for _, m := range ms {
		e := m.(map[string]any)
		id, _ := e["id"].(string)
		byID[id] = e
		order = append(order, id)
	}
	return byID, order, f
}

func TestDroid(t *testing.T) {
	home, path := droidHome(t)
	os.WriteFile(path, []byte(droidUser), 0o644)
	a, err := Find("droid")
	if err != nil || a.Path != path {
		t.Fatalf("find: %v %v", a, err)
	}
	if !a.Detected() {
		t.Fatal("not detected with ~/.factory")
	}
	if got := a.Values()["model"]; got != "claude-opus-4-7" {
		t.Fatalf("model: %q", got)
	}
	// the picker: the user's own BYOK model by the id droid knows it by,
	// the Factory model they're on, then magpie's catalog
	opts := a.Field("model").Options(a.Values())
	if opts[0].Value != "claude-opus-4-7" || opts[1].Value != "custom:Kimi-K2-[Groq]-0" || opts[1].Label != "Kimi K2 [Groq]" || opts[1].Group != "Droid" {
		t.Fatalf("own options: %+v", opts[:2])
	}
	var via bool
	for _, o := range opts {
		via = via || o.Value == "magpie/deepseek/pro"
	}
	if !via {
		t.Fatalf("no magpie/deepseek/pro in %+v", opts)
	}

	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := droidFile(t, path)
	if order[0] != "custom:Kimi-K2-[Groq]-0" {
		t.Fatalf("the user's model moved: %v", order)
	}
	// the user's entry is as it was, byte for byte
	if !strings.Contains(readFile(path), droidUser[strings.Index(droidUser, "    {"):strings.Index(droidUser, "    }\n")+5]) {
		t.Fatalf("user's entry rewritten:\n%s", readFile(path))
	}
	sd := f["sessionDefaultSettings"].(map[string]any)
	if sd["model"] != "custom:magpie/deepseek/pro" || sd["autonomyLevel"] != "low" || f["diffMode"] != "unified" {
		t.Fatalf("settings: %v", f)
	}
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model read back: %q", got)
	}
	// each on the API its provider speaks, so the gateway relays it as it is
	for id, want := range map[string][2]string{
		"custom:magpie/deepseek/pro":         {"generic-chat-completion-api", gatewayV1()},
		"custom:magpie/deepseek/flash":       {"generic-chat-completion-api", gatewayV1()},
		"custom:magpie/resp/grok-5":          {"openai", gatewayV1()},
		"custom:magpie/anth/claude-sonnet-5": {"anthropic", gateway.URL()},
	} {
		e := ms[id]
		if e == nil {
			t.Errorf("%s missing: %v", id, order)
			continue
		}
		if e["provider"] != want[0] || e["baseUrl"] != want[1] || e["apiKey"] != gateway.Token {
			t.Errorf("%s: %v", id, e)
		}
		if _, ok := e["noImageSupport"]; !ok {
			t.Errorf("%s: noImageSupport left to droid's default: %v", id, e)
		}
		if m := strings.TrimPrefix(id, "custom:magpie/"); e["model"] != m {
			t.Errorf("%s: model %v", id, e["model"])
		}
	}
	if n := ms["custom:magpie/deepseek/pro"]["displayName"]; n != "pro · DeepSeek" {
		t.Errorf("displayName %v", n)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
	// another of magpie's: the stash keeps the user's own
	if err := a.Apply("model", "magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	// a provider added since joins the picker on Sync
	provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if ms, _, _ = droidFile(t, path); ms["custom:magpie/later/m1"] == nil {
		t.Fatalf("sync: %v", ms)
	}
	// something else rewrote magpie's entry
	cur := readFile(path)
	at := strings.Index(cur, `"custom:magpie/resp/grok-5"`)
	edited := cur[:at] + strings.Replace(cur[at:], gatewayV1(), "http://127.0.0.1:9/v1", 1)
	os.WriteFile(path, []byte(edited), 0o644)
	if d := a.Check(); !strings.Contains(d, "baseUrl") {
		t.Fatalf("check after a moved baseUrl: %q", d)
	}
	a.Sync()
	if d := a.Check(); d != "" {
		t.Fatalf("check after sync: %s", d)
	}

	// reset: the user's model back, magpie's entries gone, the rest as it was
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != droidUser {
		t.Fatalf("not restored:\n%s", got)
	}
	_ = home
}

// Picking one of the user's own models while on magpie's takes magpie out.
func TestDroidOwnModel(t *testing.T) {
	_, path := droidHome(t)
	os.WriteFile(path, []byte(droidUser), 0o644)
	a := droid(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "custom:Kimi-K2-[Groq]-0"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := droidFile(t, path)
	if len(order) != 1 || ms["custom:Kimi-K2-[Groq]-0"] == nil {
		t.Fatalf("models: %v", order)
	}
	if got := f["sessionDefaultSettings"].(map[string]any)["model"]; got != "custom:Kimi-K2-[Groq]-0" {
		t.Fatalf("model: %v", got)
	}
	// and a reset after that leaves the user's choice, not the stash
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	a.Apply("model", "")
	if got := a.Values()["model"]; got != "custom:Kimi-K2-[Groq]-0" {
		t.Fatalf("reset: %q", got)
	}
}

// No settings.json at all, or one with droid's documented top-level
// "model": magpie adds only its own, and a reset leaves the file as it was.
func TestDroidFresh(t *testing.T) {
	home, path := droidHome(t)
	a := droid(home)
	if a.Values()["model"] != "" {
		t.Fatal("model without a file")
	}
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if _, order, _ := droidFile(t, path); len(order) != 5 {
		t.Fatalf("models: %v", order)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(path)); got != "{}" {
		t.Fatalf("left: %s", got)
	}

	top := "{\n  \"model\": \"gpt-5.5\",\n  \"customModels\": []\n}\n"
	os.WriteFile(path, []byte(top), 0o644)
	if a.Values()["model"] != "gpt-5.5" {
		t.Fatalf("top-level model: %q", a.Values()["model"])
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// droid reads sessionDefaultSettings.model before the top-level one
	if _, _, f := droidFile(t, path); f["model"] != "gpt-5.5" || f["sessionDefaultSettings"].(map[string]any)["model"] != "custom:magpie/deepseek/pro" {
		t.Fatalf("settings: %v", f)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != "{\n  \"model\": \"gpt-5.5\"\n}\n" || a.Values()["model"] != "gpt-5.5" {
		t.Fatalf("reset:\n%s", got)
	}
}

// $FACTORY_HOME_OVERRIDE stands for the home droid keeps .factory in; the
// legacy config.json's custom_models are offered, never written.
func TestDroidOverrideAndLegacy(t *testing.T) {
	home, _ := droidHome(t)
	other := filepath.Join(home, "elsewhere")
	t.Setenv("FACTORY_HOME_OVERRIDE", other)
	a := droid(home)
	if a.Path != filepath.Join(other, ".factory", "settings.json") {
		t.Fatalf("path: %s", a.Path)
	}
	legacy := filepath.Join(other, ".factory", "config.json")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	cfg := `{"custom_models":[{"model_display_name":"Local Qwen","model":"qwen3:4b","base_url":"http://localhost:11434/v1","api_key":"x","provider":"generic-chat-completion-api"},{"model":"qwen3:4b","model_display_name":"Local Qwen","base_url":"http://localhost:11435/v1","api_key":"x","provider":"generic-chat-completion-api"}]}`
	os.WriteFile(legacy, []byte(cfg), 0o644)
	var got []string
	for _, o := range a.Field("model").Options(a.Values()) {
		if o.Group == "Droid" {
			got = append(got, o.Value)
		}
	}
	if strings.Join(got, ",") != "custom:Local-Qwen-0,custom:Local-Qwen-1" {
		t.Fatalf("legacy options: %v", got)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if readFile(legacy) != cfg {
		t.Fatal("config.json written")
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatal(err)
	}
}

// droidWants holds droid's own reading of a model's name (droid 0.231.0's
// xs and g2): /\bgpt[-_.]?\d/i, /codex/i, /\bo[1-9](?:[-_.]|$)/i for
// OpenAI, then /claude|haiku|sonnet|opus/i for Claude.
func TestDroidWantsItsOwnRule(t *testing.T) {
	for m, want := range map[string]string{
		"ultrafast/gpt-6.1-sol":    "openai",
		"x/GPT5":                   "openai",
		"x/gpt_4o":                 "openai",
		"codex/gpt-5.5":            "openai",
		"x/gpt-5.3-codex":          "openai",
		"x/my-codex-proxy-model":   "openai",
		"x/o3":                     "openai",
		"x/o4-mini":                "openai",
		"x/o1.preview":             "openai",
		"x/gpt-oss-120b":           "",
		"x/proto1":                 "",
		"x/o10x":                   "",
		"x/claude-opus-4-7":        "anthropic",
		"or/anthropic/sonnet-next": "anthropic",
		"x/HAIKU":                  "anthropic",
		"deepseek/pro":             "",
		"x/kimi-k2.5":              "",
	} {
		if got := droidWants(m); got != want {
			t.Errorf("%s: %q, want %q", m, got, want)
		}
	}
}

// A GPT or a Claude served on Chat is written on the provider droid asks
// for, so droid doesn't warn about it at every session start (ji233 on
// Discord: "ultrafast/gpt-6.1-sol appears to be an OpenAI model"), and an
// entry an older magpie wrote on Chat is moved on the next sync.
func TestDroidProviderByName(t *testing.T) {
	_, path := droidHome(t)
	provider.Save(provider.Provider{ID: "ultrafast", Name: "Ultrafast", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"gpt-6.1-sol", "o3", "claude-opus-4-7", "kimi-k2.5"}})
	a := droid(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/ultrafast/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"custom:magpie/ultrafast/gpt-6.1-sol":     {"openai", gatewayV1()},
		"custom:magpie/ultrafast/o3":              {"openai", gatewayV1()},
		"custom:magpie/ultrafast/claude-opus-4-7": {"anthropic", gateway.URL()},
		"custom:magpie/ultrafast/kimi-k2.5":       {"generic-chat-completion-api", gatewayV1()},
		"custom:magpie/deepseek/pro":              {"generic-chat-completion-api", gatewayV1()},
		"custom:magpie/anth/claude-sonnet-5":      {"anthropic", gateway.URL()},
		"custom:magpie/resp/grok-5":               {"openai", gatewayV1()},
	}
	check := func(when string) {
		t.Helper()
		ms, _, _ := droidFile(t, path)
		for id, w := range want {
			if e := ms[id]; e == nil || e["provider"] != w[0] || e["baseUrl"] != w[1] {
				t.Errorf("%s: %s: %v", when, id, e)
			}
		}
		if d := a.Check(); d != "" {
			t.Errorf("%s: check: %s", when, d)
		}
	}
	check("apply")

	// as an older magpie left it: every Chat model on
	// generic-chat-completion-api, and no record of what it wrote
	old := strings.ReplaceAll(readFile(path), `"provider": "openai"`, `"provider": "generic-chat-completion-api"`)
	old = strings.Replace(old, `"model": "ultrafast/claude-opus-4-7",`, `"model": "ultrafast/claude-opus-4-7", "x": 1,`, 1)
	os.WriteFile(path, []byte(old), 0o644)
	forget("droid:" + path + ":wrote")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	check("sync over an older magpie's")
}

// A field of one of magpie's entries the user changed stays as they set
// it through a sync and a new pick; the rest follows the catalog, and the
// baseUrl follows the provider they picked.
func TestDroidKeepsAnEntryEditedByHand(t *testing.T) {
	_, path := droidHome(t)
	provider.Save(provider.Provider{ID: "ultrafast", Name: "Ultrafast", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"gpt-6.1-sol", "kimi-k2.5"}})
	a := droid(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/ultrafast/kimi-k2.5"); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	for _, m := range f["customModels"].([]any) {
		e := m.(map[string]any)
		switch e["id"] {
		case "custom:magpie/ultrafast/gpt-6.1-sol":
			// back on Chat, by their own choice
			e["provider"] = "generic-chat-completion-api"
			e["maxContextLimit"] = 300000
			e["extraArgs"] = map[string]any{"reasoning_effort": "high"}
			e["index"] = 7
		case "custom:magpie/ultrafast/kimi-k2.5":
			e["provider"] = "anthropic"
			e["displayName"] = "Kimi (mine)"
			delete(e, "noImageSupport")
		}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	os.WriteFile(path, b, 0o644)
	for _, step := range []func() error{a.Sync, func() error { return a.Apply("model", "magpie/deepseek/pro") }} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
		ms, _, _ := droidFile(t, path)
		g := ms["custom:magpie/ultrafast/gpt-6.1-sol"]
		if g["provider"] != "generic-chat-completion-api" || g["baseUrl"] != gatewayV1() || g["maxContextLimit"] != float64(300000) ||
			g["extraArgs"] == nil || g["index"] != nil || g["apiKey"] != gateway.Token || g["displayName"] == nil {
			t.Errorf("gpt: %v", g)
		}
		k := ms["custom:magpie/ultrafast/kimi-k2.5"]
		if _, ok := k["noImageSupport"]; ok || k["provider"] != "anthropic" || k["baseUrl"] != gateway.URL() || k["displayName"] != "Kimi (mine)" {
			t.Errorf("kimi: %v", k)
		}
		if p := ms["custom:magpie/deepseek/pro"]; p["provider"] != "generic-chat-completion-api" || p["displayName"] != "pro · DeepSeek" {
			t.Errorf("an entry not edited: %v", p)
		}
	}
	// disconnecting drops them with the record
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := stashLoad()["droid:"+path+":wrote"]; ok {
		t.Error("record kept after disconnect")
	}
}

// tao5010 on Discord: droid offered none of magpie's models any thinking
// level. droid gives a custom model it doesn't know levels only when its
// entry has reasoningEffort (droid 1.3.0), and takes a built-in's own
// through baseModelId.
func TestDroidReasoningEffort(t *testing.T) {
	_, path := droidHome(t)
	provider.Save(provider.Provider{ID: "oa", Name: "OA", Key: "k", Responses: "http://127.0.0.1:1/v1",
		Models: []string{"gpt-5.4", "plain"}})
	for ref, levels := range map[string][]string{
		"deepseek/pro":   {"low", "medium", "high"},
		"deepseek/flash": {"low", "high"},
		"resp/grok-5":    {"xhigh"},
		"oa/gpt-5.4":     {"low", "medium", "high"},
	} {
		if err := provider.SetModelEfforts(ref, levels); err != nil {
			t.Fatal(ref, err)
		}
	}
	if err := provider.SetModelReasonless("oa/plain", true); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "gpt-5.4", Name: "Both", Members: []string{"deepseek/pro", "oa/gpt-5.4"}},
		{ID: "apart", Name: "Apart", Members: []string{"deepseek/flash", "resp/grok-5"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	a := droid(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]any{ // reasoningEffort, baseModelId
		"custom:magpie/deepseek/pro":   {"medium", nil},
		"custom:magpie/deepseek/flash": {"high", nil},
		"custom:magpie/resp/grok-5":    {"xhigh", nil},
		"custom:magpie/oa/gpt-5.4":     {"medium", "gpt-5.4"},
		"custom:magpie/oa/plain":       {nil, nil},
		"custom:magpie/group/gpt-5.4":  {"medium", nil}, // never a built-in
		"custom:magpie/group/apart":    {nil, nil},
	}
	check := func(when string, want map[string][2]any) {
		t.Helper()
		ms, _, _ := droidFile(t, path)
		for id, w := range want {
			e, ok := ms[id]
			if !ok {
				t.Errorf("%s: %s missing", when, id)
				continue
			}
			if e["reasoningEffort"] != w[0] || e["baseModelId"] != w[1] {
				t.Errorf("%s: %s reasoningEffort %v baseModelId %v, want %v %v", when, id, e["reasoningEffort"], e["baseModelId"], w[0], w[1])
			}
		}
	}
	check("written", want)

	// what the user sets stays theirs, a field they took out stays out
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	for _, m := range f["customModels"].([]any) {
		e := m.(map[string]any)
		switch e["id"] {
		case "custom:magpie/deepseek/pro":
			e["reasoningEffort"] = "high"
			e["baseModelId"] = "deepseek-v4"
		case "custom:magpie/oa/gpt-5.4":
			delete(e, "baseModelId")
		}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	os.WriteFile(path, b, 0o644)
	want["custom:magpie/deepseek/pro"] = [2]any{"high", "deepseek-v4"}
	want["custom:magpie/oa/gpt-5.4"] = [2]any{"medium", nil}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	check("after sync", want)
	if err := a.Apply("model", "magpie/oa/gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	check("after apply", want)
}
