package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model of DeepSeek's own picked in dsh's /model, which dsh saves in its
// settings over the patch list, leaves dsh connected: magpie's route is
// still in its list, beside DeepSeek's (Fate on Discord: DSH went back to
// Not connected). Disconnect then takes the route, magpie's start and its
// key out, and the model picked in dsh stays.
func TestDshOwnPickStaysConnected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "glm", Name: "GLM", Chat: "https://glm.test/v1", Key: "k", Models: []string{"glm-5.3"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".dsh")
	template := "# Your patch layer for this dsh profile.\n[]\n"
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte(template), 0o644)
	settings := filepath.Join(dir, "settings.yaml")
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	a := dsh(home)
	if a.Wired() {
		t.Fatal("wired before anything")
	}
	if err := a.Pick("model", "magpie/glm/glm-5.3"); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatal("not wired on magpie's model")
	}

	// picked in dsh: DeepSeek's own
	os.WriteFile(settings, []byte("ui:\n  theme: dark\nagent-default-model:\n  provider: deepseek-official\n  model: deepseek-v4-flash\n"), 0o644)
	if got := a.Field("model").Get(); got != "deepseek-v4-flash" {
		t.Fatalf("model: %q", got)
	}
	if !a.Wired() {
		t.Fatal("dsh went back to Not connected with magpie's route still in its list")
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Fatal("still wired after Disconnect")
	}
	if got := read(web); got != template {
		t.Fatalf("the patch list after Disconnect:\n%q", got)
	}
	if strings.Contains(read(filepath.Join(dir, ".env")), dshKeyRef) {
		t.Fatal("the key outlived the route")
	}
	if s := read(settings); !strings.Contains(s, "model: deepseek-v4-flash") || !strings.Contains(s, "theme: dark") {
		t.Fatalf("the pick made in dsh was lost:\n%s", s)
	}
	if got := a.Field("model").Get(); got != "deepseek-v4-flash" {
		t.Fatalf("model after Disconnect: %q", got)
	}
}
