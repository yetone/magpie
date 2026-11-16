package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// An agent plugin with no model key is connect only (#2374, a Raycast
// plugin): its row is magpie as its provider, on or off, with no model to
// pick, connect is called without a model and writes the gateway and the
// model list, the list follows the catalog, and switching it off puts back
// what the user had. Its id is one no built-in has: Raycast is built in
// (ef6f47ed2), and a built-in comes before a plugin of the same id.
func TestPluggedConnectOnly(t *testing.T) {
	home := pluggedHome(t, `
export const agent = { id: "launcher", name: "Launcher", config: "~/.launcher/providers.json" };
export function connect({ gateway, model, models }) {
  return {
    "providers.magpie.base": gateway.v1,
    "providers.magpie.key": gateway.key,
    "providers.magpie.models": models.map((m) => m.id),
    "providers.magpie.given": typeof model,
  };
}`)
	path := filepath.Join(home, ".launcher", "providers.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"dark","providers":{"magpie":{"key":"mine"}}}`), 0o644)
	read := func() (map[string]any, map[string]any) {
		t.Helper()
		var c map[string]any
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		ps, _ := c["providers"].(map[string]any)
		m, _ := ps["magpie"].(map[string]any)
		return c, m
	}

	a, err := Find("launcher")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fields) != 1 || a.Fields[0].Key != "provider" || !a.ListsModels {
		t.Fatalf("fields %+v, lists models %v", a.Fields, a.ListsModels)
	}
	f := a.Field("provider")
	if opts := f.Options(nil); len(opts) != 1 || opts[0].Value != magpieID {
		t.Fatalf("a model is offered to pick: %+v", opts)
	}
	if f.Get() != "" || a.Wired() {
		t.Fatal("connected before it was")
	}

	how, err := a.ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	c, m := read()
	if how.How != "magpie" || m["base"] != gateway.URL()+"/v1" || m["key"] != gateway.TokenFor("launcher") || m["given"] != "undefined" || c["theme"] != "dark" {
		t.Fatalf("connected %+v: %v", how, c)
	}
	if _, ok := c["model"]; ok {
		t.Fatalf("a model was written: %v", c)
	}
	ids := func() []string {
		_, m := read()
		var out []string
		for _, v := range m["models"].([]any) {
			out = append(out, v.(string))
		}
		return out
	}
	if !slices.Contains(ids(), "deepseek/pro") || !slices.Contains(ids(), "deepseek/flash") {
		t.Fatalf("models: %v", ids())
	}
	if f.Get() != magpieID || !a.Wired() || a.Check() != "" {
		t.Fatalf("get %q, wired %v, check %q", f.Get(), a.Wired(), a.Check())
	}

	// the catalog changed: the list it wrote follows
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids(), "deepseek/max") {
		t.Fatalf("the list didn't follow the catalog: %v", ids())
	}

	// the key moved by hand: Check says so
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gateway.URL(), "http://elsewhere", 1)), 0o644)
	if a.Check() == "" {
		t.Fatal("Check missed the base moved")
	}

	// off: what the user had is back, and what wasn't there is gone
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, m = read()
	if m["key"] != "mine" || m["base"] != nil || m["models"] != nil || c["theme"] != "dark" || a.Wired() || f.Get() != "" {
		t.Fatalf("off: %v", c)
	}
}

// A connect-only module needs a connect, and one that writes something:
// with neither a model key nor keys of connect's, the row would say
// connected while nothing in the agent's file was magpie's.
func TestPluggedConnectOnlyWritesSomething(t *testing.T) {
	pluggedHome(t,
		`export const agent = { id: "bare", config: "~/.bare.json" };`,
		`export const agent = { id: "empty", config: "~/.empty.json" };
export function connect() { return {}; }`,
	)
	errs := PluginErrors()
	if len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	for _, e := range errs {
		if !strings.Contains(e, "agent.model names no key") || !strings.Contains(e, "connect") {
			t.Fatalf("error: %q", e)
		}
	}
	a, err := Find("empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConnectHow(); err == nil || !strings.Contains(err.Error(), "no keys") {
		t.Fatalf("connect: %v", err)
	}
	if a.Wired() {
		t.Fatal("connected with nothing written")
	}
	if _, err := os.Stat(a.Path); err == nil {
		t.Fatal("written though connect returned nothing")
	}
}

// A connect-only plugin may return nothing but an object magpie merges
// (Lorca's custom:magpie, plugins#1230): its members are what is written,
// so it connects, reads as connected, and switching it off leaves the file
// as it was. connect's result was counted after merged() took the object
// out, so such a plugin was refused with "no keys to write".
func TestPluggedConnectOnlyMergedOnly(t *testing.T) {
	home := pluggedHome(t, `
export const agent = { id: "launcher", config: "~/.launcher/creds.json", format: "json", merge: ["custom.custom:magpie"] };
export function connect({ gateway, models }) {
  return { "custom.custom:magpie": { base_url: gateway.v1, api_key: gateway.key, models: models.map((m) => ({ id: m.id })) } };
}`)
	path := filepath.Join(home, ".launcher", "creds.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	before := `{"custom":{"custom:mine":{"base_url":"x"}},"theme":"dark"}`
	os.WriteFile(path, []byte(before), 0o644)

	a, err := Find("launcher")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var c struct {
		Custom map[string]struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		}
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if m := c.Custom["custom:magpie"]; m.BaseURL != gateway.URL()+"/v1" || m.APIKey != gateway.TokenFor("launcher") || c.Custom["custom:mine"].BaseURL != "x" {
		t.Fatalf("connected: %s", b)
	}
	if f := a.Field("provider"); f.Get() != magpieID || !a.Wired() || a.Check() != "" {
		t.Fatalf("get %q, wired %v, check %q", f.Get(), a.Wired(), a.Check())
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	var got, want any
	json.Unmarshal(b, &got)
	json.Unmarshal([]byte(before), &want)
	if gb, _ := json.Marshal(got); string(gb) != func() string { wb, _ := json.Marshal(want); return string(wb) }() {
		t.Fatalf("unwired: %s, want %s", b, before)
	}
	if a.Wired() {
		t.Fatal("wired after unwire")
	}
}
