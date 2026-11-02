package cli

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/profile"
)

// magpie rename moves a profile's name; a flag or a missing or extra word
// is no name to write, and leaves the profiles as they were (#2721).
func TestRenameCommand(t *testing.T) {
	helpHome(t)
	if err := profile.Save("work", profile.Profile{Fields: map[string]string{"codex.model": "m"}}); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		ps, err := profile.Load()
		if err != nil {
			t.Fatal(err)
		}
		return profile.Names(ps)
	}
	for _, args := range [][]string{{"rename"}, {"rename", "work"}, {"rename", "work", "-x"}, {"rename", "work", "a", "b"}} {
		if _, err := runOut(t, args...); err == nil {
			t.Errorf("magpie %v went through", args)
		}
		if got := names(); !slices.Equal(got, []string{"work"}) {
			t.Fatalf("magpie %v: %v", args, got)
		}
	}
	if _, err := runOut(t, "rename", "work", "desk"); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"desk"}) {
		t.Fatalf("after magpie rename work desk: %v", got)
	}
}
