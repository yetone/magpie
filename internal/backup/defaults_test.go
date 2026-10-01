package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// Defaults survive encryption and restore, while a legacy backup's
// missing model does not reset the model already chosen here.
func TestAgentDefaultsRoundTrip(t *testing.T) {
	home(t)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	path := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := b.Agents["claude.model"]; !ok || v != "" {
		t.Errorf("backup omitted the default model: %+v", b.Agents)
	}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	b, err = Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "opus"); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(b, Parts{Agents: true})
	if err != nil || r.Agents != 1 || len(r.Skipped) != 0 || a.Field("model").Get() != "" {
		t.Errorf("restored %+v %v; model %q", r, err, a.Field("model").Get())
	}
	if _, ok := edit.GetJSON(path, "model"); ok {
		t.Error("restoring the default kept the model key")
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Error("restoring the default changed unrelated settings")
	}
	if err := a.Apply("model", "opus"); err != nil {
		t.Fatal(err)
	}
	legacy := Bundle{Version: 1, Agents: map[string]string{"claude.effort": "low"}}
	if r, err := Restore(legacy, Parts{Agents: true}); err != nil || r.Agents != 1 || a.Field("model").Get() != "opus" {
		t.Fatalf("legacy restore: %+v %v; model %q", r, err, a.Field("model").Get())
	}
}
