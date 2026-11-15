package settings

import (
	"os"
	"testing"
)

// The main window's pin (NovaStudiosss on X) is saved alone, keeping what
// the user set elsewhere, and turns back off the same way.
func TestSetOnTop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().OnTop {
		t.Fatal("the window is pinned by default")
	}
	s := Load()
	s.Theme, s.NoBird = "dark", true
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := SetOnTop(true); err != nil {
		t.Fatal(err)
	}
	if got := Load(); !got.OnTop || got.Theme != "dark" || !got.NoBird {
		t.Fatalf("after SetOnTop(true): onTop %v, theme %q, noBird %v", got.OnTop, got.Theme, got.NoBird)
	}
	if err := SetOnTop(false); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.OnTop || got.Theme != "dark" {
		t.Fatalf("after SetOnTop(false): onTop %v, theme %q", got.OnTop, got.Theme)
	}
}

// A settings file that can't be read is not written over with defaults by
// the pin: the save fails and the file stays as it was.
func TestSetOnTopLeavesAnUnreadFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	p := Path()
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetOnTop(true); err == nil {
		t.Fatal("SetOnTop saved over a file it couldn't read")
	}
	if b, _ := os.ReadFile(p); string(b) != "{not json" {
		t.Fatalf("the unread file was written: %q", b)
	}
}

// The pin is this computer's own, as the window's size and place are: a
// synced or restored bundle neither brings another's nor drops this one's.
func TestOnTopOwn(t *testing.T) {
	from := Settings{Theme: "dark"}
	from.KeepOwn(Settings{OnTop: true})
	if !from.OnTop || from.Theme != "dark" {
		t.Fatalf("KeepOwn dropped it: %+v", from.OnTop)
	}
	from = Settings{OnTop: true}
	from.KeepOwn(Settings{})
	if from.OnTop {
		t.Fatal("another computer's pin here")
	}
}
