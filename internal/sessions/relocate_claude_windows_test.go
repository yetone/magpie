package sessions

import "testing"

func TestClaudeRelocationWSLPaths(t *testing.T) {
	old := WSLHomes
	t.Cleanup(func() { WSLHomes = old })
	WSLHomes = func() []WSLHome {
		return []WSLHome{{Distro: "Ubuntu", Home: `\\wsl.localhost\Ubuntu\home\me`, Running: true}}
	}
	in := ClaudeRelocation{From: "/home/me/old", To: "/home/me/new", WSL: "Ubuntu"}
	root, target, err := claudeRelocationRoot(in)
	if err != nil || root != `\\wsl.localhost\Ubuntu\home\me\.claude` ||
		target != `\\wsl.localhost\Ubuntu\home\me\new` {
		t.Fatalf("root=%q target=%q err=%v", root, target, err)
	}
	in.WSL = "Debian"
	if _, _, err := claudeRelocationRoot(in); err == nil {
		t.Fatal("unknown distro accepted")
	}
}
