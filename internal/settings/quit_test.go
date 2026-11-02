package settings

import "testing"

// A Quit asks first by default (zhujiu on Discord asked for a way not to
// be asked again); the question's "Don't ask again" sets QuitNoAsk alone,
// keeping what the user set elsewhere, and turns back off the same way.
func TestSetQuitNoAsk(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().QuitNoAsk {
		t.Fatal("a Quit doesn't ask by default")
	}
	s := Load()
	s.Theme, s.NoBird = "dark", true
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := SetQuitNoAsk(true); err != nil {
		t.Fatal(err)
	}
	if got := Load(); !got.QuitNoAsk || got.Theme != "dark" || !got.NoBird {
		t.Fatalf("after SetQuitNoAsk(true): quitNoAsk %v, theme %q, noBird %v", got.QuitNoAsk, got.Theme, got.NoBird)
	}
	if err := SetQuitNoAsk(false); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.QuitNoAsk || got.Theme != "dark" {
		t.Fatalf("after SetQuitNoAsk(false): quitNoAsk %v, theme %q", got.QuitNoAsk, got.Theme)
	}
}
