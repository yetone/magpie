package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestWorkBuddy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".workbuddy", "models.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// WorkBuddy's own local model, which stays
	os.WriteFile(path, []byte(`[{"id":"qwen-local","local":true,"url":"http://127.0.0.1:8080/v1/chat/completions"}]`), 0o600)
	read := func() []map[string]any {
		var ms []map[string]any
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &ms); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return ms
	}

	a := workbuddy(home)
	if !a.Detected() {
		t.Fatal("not detected")
	}
	f := a.Field("provider")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	ms := read()
	if len(ms) != 2 || ms[0]["id"] != "qwen-local" || ms[0]["local"] != true {
		t.Fatalf("models: %v", ms)
	}
	pro := ms[1]
	if pro["id"] != "deepseek/pro" || pro["vendor"] != "magpie" || pro["apiKey"] != "magpie-workbuddy" ||
		pro["url"] != gatewayV1()+"/chat/completions" || pro["supportsToolCall"] != true || pro["maxInputTokens"] == nil {
		t.Fatalf("magpie model: %v", pro)
	}
	if f.Get() != "magpie" {
		t.Fatalf("get: %q", f.Get())
	}

	// turned off in WorkBuddy, it stays off through a sync
	ms[1]["disabled"] = true
	b, _ := json.Marshal(ms)
	os.WriteFile(path, b, 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if ms = read(); len(ms) != 2 || ms[1]["disabled"] != true {
		t.Fatalf("after sync: %v", ms)
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if ms = read(); len(ms) != 1 || ms[0]["id"] != "qwen-local" {
		t.Fatalf("after off: %v", ms)
	}
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	// off, a sync leaves it off
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(read()) != 1 {
		t.Fatal("sync put magpie back")
	}

	// the CLI's object form, with a list of the models to show
	os.WriteFile(path, []byte(`{"models":[{"id":"mine","url":"https://x/v1/chat/completions"}],"availableModels":["mine"],"other":1}`), 0o600)
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models    []map[string]any `json:"models"`
		Available []string         `json:"availableModels"`
		Other     int              `json:"other"`
	}
	b, _ = os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 2 || doc.Other != 1 || len(doc.Available) != 2 || doc.Available[1] != "deepseek/pro" {
		t.Fatalf("object form: %s", b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	doc.Available = nil
	json.Unmarshal(b, &doc)
	if len(doc.Models) != 1 || len(doc.Available) != 1 {
		t.Fatalf("object form off: %s", b)
	}
}

func TestWorkBuddyEfforts(t *testing.T) {
	e := workbuddyModel("x/y", "Y", 0, 500000, true, []string{"none", "low", "high"})
	r, _ := e["reasoning"].(map[string]any)
	if e["maxInputTokens"] != 200000 || e["maxOutputTokens"] != zcodeMaxOutput || e["supportsReasoning"] != true ||
		r["canDisableThinking"] != true || r["defaultEffort"] != "high" || len(r["supportedEfforts"].([]string)) != 2 {
		t.Fatalf("%v", e)
	}
	if e := workbuddyModel("x/z", "Z", 1000, 0, false, nil); e["reasoning"] != nil || e["supportsReasoning"] != false || e["maxOutputTokens"] != nil {
		t.Fatalf("%v", e)
	}
}
