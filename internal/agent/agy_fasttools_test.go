package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/fasttools"
)

// agy's "fast tools" turns magpie's plugin for it on and off, starting the
// magpie it is; it is the user's own, so Disconnect leaves it.
func TestAgyFastTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	exe := filepath.Join(home, "bin", "magpie")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	was := agyFastToolsExe
	agyFastToolsExe = func() string { return exe }
	t.Cleanup(func() { agyFastToolsExe = was })
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("{}\n"), 0o644)

	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	f := a.Field("fasttools")
	if f == nil || !f.Own || !f.Quiet {
		t.Fatalf("field: %+v", f)
	}
	if f.Get() != "" || fasttools.On(home) {
		t.Fatal("on before asked")
	}
	if o := f.Options(nil); len(o) != 1 || o[0].Value != "on" || o[0].Note == "" {
		t.Fatalf("options: %+v", o)
	}
	if err := a.Apply("fasttools", "on"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "on" || !fasttools.On(home) {
		t.Fatal("not on")
	}
	if b, _ := os.ReadFile(filepath.Join(fasttools.Dir(home), "mcp_config.json")); !strings.Contains(string(b), `"fast-tools"`) {
		t.Fatalf("mcp_config.json: %s", b)
	}
	if err := f.Set("maybe"); err == nil {
		t.Fatal("took maybe")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a2, _ := Find("agy"); a2.Field("fasttools").Get() != "on" {
		t.Fatal("Disconnect turned fast tools off")
	}
	if err := f.Set("off"); err != nil || f.Get() != "" || fasttools.On(home) {
		t.Fatalf("off: %v", err)
	}
	if _, err := os.Stat(fasttools.Dir(home)); !os.IsNotExist(err) {
		t.Fatalf("the plugin folder is still there: %v", err)
	}
	if err := f.Set(""); err != nil {
		t.Fatalf("off again: %v", err)
	}
}
