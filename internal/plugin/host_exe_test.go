package plugin

import (
	"os"
	"slices"
	"testing"
)

// A plugin is told this magpie's executable, for `tui-helper`, and one
// set in magpie's own environment is not passed on in its place.
func TestHostToldMagpieExe(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := hostEnv([]string{"PATH=/usr/bin", "MAGPIE_EXE=/somewhere/else"})
	if !slices.Contains(env, "MAGPIE_EXE="+exe) {
		t.Errorf("no MAGPIE_EXE=%s in %q", exe, env)
	}
	if slices.Contains(env, "MAGPIE_EXE=/somewhere/else") {
		t.Errorf("the environment's own MAGPIE_EXE passed on: %q", env)
	}
}
