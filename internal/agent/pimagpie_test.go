package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// With the pi-magpie extension installed, magpie's own providers.magpie
// block in models.json is taken out (a copy kept), the user's other
// providers stay, and a block that is not magpie's stays too.
func TestPiMagpieExtensionTakesOverTheBlock(t *testing.T) {
	home := syncHome(t)
	dir := filepath.Join(home, ".pi", "agent")
	os.MkdirAll(dir, 0o755)
	models := filepath.Join(dir, "models.json")
	settings := filepath.Join(dir, "settings.json")
	os.WriteFile(settings, []byte(`{"packages":[]}`), 0o644)
	os.WriteFile(models, []byte(`{"providers":{"mine":{"baseUrl":"http://x/v1"}}}`), 0o644)
	if err := provider.Save(provider.Provider{ID: "p", Name: "P", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	// plain Pi: the block is written
	if err := pi(home).Field("model").Set("magpie/p/m"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(models), `"magpie"`) {
		t.Fatalf("without the extension the block is written: %s", readFile(models))
	}
	// the extension installed: the next sync drops it
	os.WriteFile(settings, []byte(`{"packages":["npm:pi-magpie"],"defaultProvider":"magpie"}`), 0o644)
	if err := pi(home).Sync(); err != nil {
		t.Fatal(err)
	}
	got := readFile(models)
	if strings.Contains(got, `"magpie"`) || !strings.Contains(got, `"mine"`) {
		t.Fatalf("block not dropped or mine lost: %s", got)
	}
	if _, err := os.Stat(models + ".before-pi-magpie"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	if msg := pi(home).Check(); msg != "" {
		t.Fatalf("check: %s", msg)
	}
	// a block the user wrote (another address) is left alone
	os.WriteFile(models, []byte(`{"providers":{"magpie":{"baseUrl":"http://example.com/v1","apiKey":"theirs"}}}`), 0o644)
	if err := pi(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(models), "example.com") {
		t.Fatalf("user's block removed: %s", readFile(models))
	}
}
