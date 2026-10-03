package agent

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// crushHome is effortHome with Crush's data folder in the sandbox too, and
// the path of the data file Crush reads there: its GlobalConfigData, which
// is ~/.local/share/crush/crush.json, or %LOCALAPPDATA%\crush\crush.json on
// Windows.
func crushHome(t *testing.T) (home, data string) {
	t.Helper()
	home = effortHome(t)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	data = filepath.Join(home, ".local", "share", "crush", "crush.json")
	if runtime.GOOS == "windows" {
		data = filepath.Join(home, "AppData", "Local", "crush", "crush.json")
	}
	return home, data
}

// Crush saves what its own model picker chooses to its data file, and reads
// that after crush.json, so a pick there wins over one in crush.json. Once
// somebody has picked a model in Crush (its first run asks for one), the
// model magpie sets has to go where Crush reads it last, and what magpie
// shows has to be what Crush will use.
func TestCrushPickWhereCrushReadsIt(t *testing.T) {
	home, data := crushHome(t)
	cr := crush(home, filepath.Join(home, ".config"))
	writeFile(t, data, `{"models":{"large":{"model":"m-mine","provider":"mine"}}}`)

	large := cr.Field("model")
	if got := large.Get(); got != "mine/m-mine" {
		t.Fatalf("large reads %q, not the pick Crush uses", got)
	}
	if err := large.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetJSON(data, "models.large.provider"); p != "magpie" {
		t.Fatalf("Crush still picks %q from its data file:\n%s", p, readFile(data))
	}
	if m, _ := edit.GetJSON(data, "models.large.model"); m != "deepseek/pro" {
		t.Fatalf("data file model %q:\n%s", m, readFile(data))
	}
	if got := large.Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("large reads %q", got)
	}
	// the provider stays where it was, with the library's MCP servers
	if _, ok := edit.GetJSON(cr.Path, "providers.magpie.base_url"); !ok {
		t.Fatalf("no magpie provider in %s:\n%s", cr.Path, readFile(cr.Path))
	}
	if cr.Check() != "" {
		t.Fatalf("check: %s", cr.Check())
	}

	// the effort is kept with the pick
	effort := cr.Field("effort")
	if err := effort.Set("high"); err != nil {
		t.Fatal(err)
	}
	if e, _ := edit.GetJSON(data, "models.large.reasoning_effort"); e != "high" || effort.Get() != "high" {
		t.Fatalf("effort %q:\n%s", effort.Get(), readFile(data))
	}

	small := cr.Field("small")
	if err := small.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	// unset, a pick is gone from both files, and the provider goes with
	// the last pick on it
	if err := large.Set(""); err != nil {
		t.Fatal(err)
	}
	if large.Get() != "" || effort.Get() != "" {
		t.Fatalf("large %q, effort %q after unset:\n%s", large.Get(), effort.Get(), readFile(data))
	}
	if _, ok := edit.GetJSON(cr.Path, "providers.magpie"); !ok {
		t.Fatal("the provider went while small still uses it")
	}
	if err := small.Set(""); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{cr.Path, data} {
		if _, ok := edit.GetJSON(f, "models.small"); ok {
			t.Fatalf("a small pick is left in %s:\n%s", f, readFile(f))
		}
	}
	if _, ok := edit.GetJSON(cr.Path, "providers.magpie"); ok {
		t.Fatalf("the provider is left with no pick on it:\n%s", readFile(cr.Path))
	}
}

// A pick magpie wrote to crush.json before it wrote Crush's data file still
// reads as set where Crush has picked nothing itself, and one Crush's own
// pick hides reads as that pick, since it is the one Crush uses.
func TestCrushPickInCrushJSON(t *testing.T) {
	home, data := crushHome(t)
	cr := crush(home, filepath.Join(home, ".config"))
	if cr.Path == data {
		t.Skip("crush.json is Crush's data file here")
	}
	writeFile(t, cr.Path, `{"providers":{"magpie":{"base_url":"x"}},"models":{"large":{"provider":"magpie","model":"deepseek/pro"}}}`)
	if got := cr.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("large reads %q", got)
	}
	writeFile(t, data, `{"models":{"large":{"model":"m-mine","provider":"mine"}}}`)
	if got := cr.Field("model").Get(); got != "mine/m-mine" {
		t.Fatalf("large reads %q, not the pick Crush uses", got)
	}
}
