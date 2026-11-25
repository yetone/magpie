package gui

import (
	"os"
	"slices"
	"testing"
)

// The owner, 11-25: the built-in Antigravity has no place in the Add
// sheet for a user with no account on it. Its plugin is withdrawn
// (plugin.WithdrawnOf), so the page is told it is given to nobody new;
// a built-in whose plugin anyone can still install is not.
func TestNoNewNamesWithdrawnBuiltins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := providersState()
	if !slices.Equal(s.NoNew, []string{"antigravity"}) {
		t.Fatalf("NoNew = %q, want only antigravity (movable: %q)", s.NoNew, s.Movable)
	}
}
