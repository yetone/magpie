package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// KoiLx0 on X: a Remote magpie editor's "Connect new agents to this
// remote" is saved on this computer (agent.SetConnectAgents), shown as
// saved, kept by a save that leaves it out, and refused another provider.
func TestProviderSaveConnectAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	shown := func(id string) bool {
		t.Helper()
		for _, p := range providersState().Providers {
			if p.ID == id {
				return p.ConnectAgents
			}
		}
		t.Fatalf("%s not listed", id)
		return false
	}
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: "http://192.168.1.20:3425/v1", Key: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "vllm", Name: "vLLM", Chat: "http://192.168.1.21:8000/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if shown("office") || agent.ConnectsAgents()["office"] {
		t.Fatal("on by default")
	}
	post(`{"id":"office","from":"office","name":"Office","preset":"remote-magpie","chat":"http://192.168.1.20:3425/v1","models":[],"headers":{},"connectAgents":true}`)
	if !agent.ConnectsAgents()["office"] || !shown("office") {
		t.Fatal("the switch wasn't saved or isn't shown")
	}
	post(`{"id":"office","from":"office","name":"Office","preset":"remote-magpie","chat":"http://192.168.1.20:3425/v1","models":[],"headers":{}}`)
	if !shown("office") {
		t.Fatal("a save that left it out turned it off")
	}
	post(`{"id":"vllm","from":"vllm","name":"vLLM","chat":"http://192.168.1.21:8000/v1","models":[],"headers":{},"connectAgents":true}`)
	if agent.ConnectsAgents()["vllm"] {
		t.Fatal("a provider that isn't a Remote magpie took the switch")
	}
	post(`{"id":"office","from":"office","name":"Office","preset":"remote-magpie","chat":"http://192.168.1.20:3425/v1","models":[],"headers":{},"connectAgents":false}`)
	if agent.ConnectsAgents()["office"] || shown("office") {
		t.Fatal("turning it off didn't")
	}
}
