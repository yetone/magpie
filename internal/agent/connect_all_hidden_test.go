package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// znuaibot's case (#1989): OpenCode is shown only the one model picked for
// it, and that model's provider is switched off. magpie has models, but
// none is left in OpenCode's lists: Connect says so, with a code the GUI
// says it by in the reader's language, not that OpenCode can't be
// connected.
func TestConnectWithEveryModelTakenOut(t *testing.T) {
	home, _ := codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.SetModelLists(map[string]provider.ModelList{"opencode": {Only: true, Picked: []string{"gem/gemini-3.8-flash"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetOff("gem", true); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".config")
	_, err := opencode(home, cfg).ConnectHow()
	var ah *AllHiddenError
	if !errors.As(err, &ah) || ah.Agent != "OpenCode" || !strings.Contains(err.Error(), "Model list › Pick") {
		t.Fatalf("%v", err)
	}
	var nm *NoModelsError
	if errors.As(err, &nm) {
		t.Fatalf("said there are no models: %v", err)
	}
	// the provider on again, its picked model is there to connect to
	if err := provider.SetOff("gem", false); err != nil {
		t.Fatal(err)
	}
	if _, err := opencode(home, cfg).ConnectHow(); err != nil {
		t.Fatalf("with the picked model's provider on: %v", err)
	}
}
