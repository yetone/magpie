package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// coworkUser is a ~/.codely/settings.json as Tuanjie Cowork 2.1.6 saves
// one: a provider and a model of the user's, and settings of its own.
const coworkUser = `{
  "migratedToSingleProvider": true,
  "migratedToMultiProvider": true,
  "customModelProviders": [
    {
      "id": "custom-provider",
      "name": "My Relay",
      "baseUrl": "https://relay.example/v1",
      "apiKey": "sk-own",
      "authType": "openai",
      "flashModelId": "glm-4.5"
    }
  ],
  "customModels": [
    {
      "id": "glm-4.5",
      "model": "glm-4.5",
      "providerId": "custom-provider",
      "displayName": "glm-4.5",
      "roles": ["model", "flashModel"],
      "wireApi": "chat",
      "supportsMultimodal": false
    }
  ],
  "language": "zh-CN"
}
`

func coworkRead(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return m
}

// coworkEntry is the entry of list in m whose id is id, nil for none.
func coworkEntry(m map[string]any, list, id string) map[string]any {
	xs, _ := m[list].([]any)
	for _, x := range xs {
		if x, _ := x.(map[string]any); x["id"] == id {
			return x
		}
	}
	return nil
}

// TestTuanjieCowork: magpie adds a provider and a model of its own to
// Tuanjie Cowork's Custom Models, at the gateway's /v1 with Cowork's key
// on chat completions (Charles24209792 on X), the user's own left as they
// are; stepping out takes them out again.
func TestTuanjieCowork(t *testing.T) {
	home, _ := museHome(t)
	path := filepath.Join(home, ".codely", "settings.json")
	writeFile(t, path, coworkUser)
	want := coworkRead(t, path)
	a := tuanjieCowork(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	if !a.Detected() {
		t.Fatal("not detected with ~/.codely")
	}
	f := a.Field("model")
	if v := f.Get(); v != "" {
		t.Fatalf("before: %q", v)
	}
	at := here(home)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	m := coworkRead(t, path)
	p := coworkEntry(m, "customModelProviders", "magpie")
	if p == nil || p["baseUrl"] != at.v1() || p["apiKey"] != agentKeyAt("tuanjie-cowork", at.gw()) || p["authType"] != "openai" || p["name"] != "magpie" {
		t.Fatalf("provider: %v", p)
	}
	mm := coworkEntry(m, "customModels", "magpie")
	if mm == nil || mm["providerId"] != "magpie" || mm["model"] != "deepseek/pro" || mm["displayName"] != "deepseek/pro" ||
		mm["wireApi"] != "chat" || !reflect.DeepEqual(mm["roles"], []any{"model"}) {
		t.Fatalf("model: %v", mm)
	}
	// the user's own, and the rest of the file, as written
	if !reflect.DeepEqual(coworkEntry(m, "customModelProviders", "custom-provider"), coworkEntry(want, "customModelProviders", "custom-provider")) ||
		!reflect.DeepEqual(coworkEntry(m, "customModels", "glm-4.5"), coworkEntry(want, "customModels", "glm-4.5")) ||
		m["language"] != "zh-CN" || m["migratedToMultiProvider"] != true {
		t.Fatalf("the user's settings changed: %v", m)
	}
	if v := f.Get(); v != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", v)
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %s", c)
	}

	// renamed and given a role in Cowork: kept when the model changes,
	// one model still, its id the one Cowork's pick holds
	edit.SetJSON(path, edit.KV{Path: "customModelProviders.1.name", Value: "Mine"},
		edit.KV{Path: "customModels.1.roles", Value: []string{"model", "multimodalModel"}})
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	m = coworkRead(t, path)
	if p := coworkEntry(m, "customModelProviders", "magpie"); p["name"] != "Mine" {
		t.Fatalf("name: %v", p)
	}
	if mm := coworkEntry(m, "customModels", "magpie"); mm["model"] != "deepseek/flash" || !reflect.DeepEqual(mm["roles"], []any{"model", "multimodalModel"}) {
		t.Fatalf("model: %v", mm)
	}
	if n := len(m["customModels"].([]any)); n != 2 {
		t.Fatalf("%d models", n)
	}

	// the gateway moved: Sync writes the new address
	edit.SetJSON(path, edit.KV{Path: "customModelProviders.1.baseUrl", Value: "http://127.0.0.1:1/v1"})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := coworkEntry(coworkRead(t, path), "customModelProviders", "magpie"); p["baseUrl"] != at.v1() {
		t.Fatalf("after Sync: %v", p)
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := coworkRead(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("not put back:\n%v\nwant\n%v", got, want)
	}
	if v := f.Get(); v != "" {
		t.Fatalf("after: %q", v)
	}
	// a model of Cowork's own is its pick, not magpie's to set
	if err := f.Set("glm-4.5"); err == nil || !strings.Contains(err.Error(), "model menu") {
		t.Fatalf("own model: %v", err)
	}
	if got := coworkRead(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("an own model wrote: %v", got)
	}
}

// TestTuanjieCoworkMadeFile: with no settings.json magpie makes one, kept
// private as Cowork's own, and takes it away again on off.
func TestTuanjieCoworkMadeFile(t *testing.T) {
	home, _ := museHome(t)
	path := filepath.Join(home, ".codely", "settings.json")
	a := tuanjieCowork(home)
	if a.Detected() {
		t.Fatal("detected with no ~/.codely and no app")
	}
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if v := f.Get(); v != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", v)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if isFile(path) {
		b, _ := os.ReadFile(path)
		t.Fatalf("left behind: %s", b)
	}
}

// TestTuanjieCoworkLeavesTheUsersMagpie: an id magpie of the user's own,
// a provider or a model, is theirs, and magpie writes nothing over it.
func TestTuanjieCoworkLeavesTheUsersMagpie(t *testing.T) {
	for name, file := range map[string]string{
		"provider": `{"customModelProviders":[{"id":"magpie","name":"m","baseUrl":"https://relay.example/v1","apiKey":"sk-own","authType":"openai"}],"customModels":[]}`,
		"model":    `{"customModelProviders":[{"id":"custom-provider","name":"m","baseUrl":"https://relay.example/v1","apiKey":"sk-own","authType":"openai"}],"customModels":[{"id":"magpie","model":"x","providerId":"custom-provider","roles":["model"],"wireApi":"chat"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, _ := museHome(t)
			path := filepath.Join(home, ".codely", "settings.json")
			writeFile(t, path, file)
			a := tuanjieCowork(home)
			if err := a.Field("model").Set("magpie/deepseek/pro"); err == nil || !strings.Contains(err.Error(), "of its own") {
				t.Fatalf("set: %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != file {
				t.Fatalf("written: %s", b)
			}
			if v := a.Field("model").Get(); v != "" {
				t.Fatalf("get: %q", v)
			}
			if err := a.Field("model").Set(""); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(path); string(b) != file {
				t.Fatalf("written on off: %s", b)
			}
		})
	}
}
