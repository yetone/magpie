package provider

import (
	"path/filepath"
	"testing"
)

// YsiCing on X: with sync's library and usage the only boxes ticked, the
// account Codex is kept signed in to followed the other computer: a
// personal one at home, a work one at the office, set again each day.
// Which account the agent stays signed in to is this computer's own.
func TestMirrorKeepsKeepLogin(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	here := Provider{ID: "relay", Name: "Relay", Chat: "https://example.com/v1", Key: "sk-here",
		KeepLogin: true, KeepLoginAs: "home@example.com", RestartApp: true}
	if err := Save(here); err != nil {
		t.Fatal(err)
	}
	office := Provider{ID: "relay", Name: "Relay renamed", Chat: "https://example.com/v1", Key: "sk-here",
		KeepLoginAs: "work@example.com", SwitchOnly: true}
	// and whether the Codex app is restarted for magpie's own switches
	fresh := Provider{ID: "other", Name: "Other", Chat: "https://example.org/v1", Key: "sk-other",
		KeepLogin: true, KeepLoginAs: "work@example.com"}
	if err := Mirror([]Provider{office, fresh}, nil); err != nil {
		t.Fatal(err)
	}
	ps, _ := Stored()
	if len(ps) != 2 {
		t.Fatalf("providers: %+v", ps)
	}
	if got := ps[0]; got.Name != office.Name || !got.KeepLogin || got.KeepLoginAs != here.KeepLoginAs || got.SwitchOnly || !got.RestartApp {
		t.Fatalf("mirrored: name %q keepLogin %v as %q switchOnly %v restartApp %v, want the other's name and this computer's sign-in", got.Name, got.KeepLogin, got.KeepLoginAs, got.SwitchOnly, got.RestartApp)
	}
	if got := ps[1]; !got.KeepLogin || got.KeepLoginAs != "" {
		t.Fatalf("new provider: keepLogin %v as %q, want kept on and no account from the other computer", got.KeepLogin, got.KeepLoginAs)
	}
}
