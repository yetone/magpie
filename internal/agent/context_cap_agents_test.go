package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The longest context (settings.ContextCap) is told to every agent that is
// written a context window (ul3140773077392 on X, #6259: dsh followed it,
// another harness kept 200K). Each agent is connected to a provider whose
// model has a 987654-token window under a cap of 99999; no file the agent
// reads may hold the longer one.
func TestEveryAgentFollowsContextCap(t *testing.T) {
	const long, capAt = "987654", 99999
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	startAlma(t)
	startShelley(t)
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "plain-model-of-no-window"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 987654}, {ID: "plain-model-of-no-window"}}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{ContextCap: 99999}); err != nil {
		t.Fatal(err)
	}
	catalog.Touched()
	for _, a := range All() {
		if a.Launch != nil || a.Native != nil || a.WSL != "" || len(a.Fields) == 0 {
			continue
		}
		t.Run(a.ID, func(t *testing.T) {
			if _, err := a.ConnectHow(); err != nil {
				t.Skip("can't be connected here:", err)
			}
			if !a.Wired() {
				t.Skip("not wired by Connect here")
			}
			SyncCatalog()
			for p, b := range agentFiles(t, home) {
				for _, d := range []string{"128000", "128_000", "200000", "250000", "262144", "256000", "131072"} {
					if strings.Contains(b, d) {
						t.Errorf("%s holds the default window %s past the cap %d", p, d, capAt)
					}
				}
				if strings.Contains(b, long) {
					t.Errorf("%s holds the window %s past the cap %d", p, long, capAt)
				}
			}
		})
	}
}
