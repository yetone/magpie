package cli

import "testing"

// Only a magpie that serves pages runs plugins' page services; a command
// that does its work and ends leaves them to the app (#5187).
func TestServesPages(t *testing.T) {
	for _, args := range [][]string{nil, {"tui"}, {"web"}, {"serve"}, {"tray"}, {"app"}, {"gui", "settings"}, {"panel"}, {"-Embedding"}} {
		if !servesPages(args) {
			t.Errorf("%q serves no pages, want it to", args)
		}
	}
	for _, args := range [][]string{{"plugin", "update"}, {"plugin", "list"}, {"plugin", "add", "x"}, {"claude"}, {"daemon", "install"}, {"autostart"}, {"providers"}} {
		if servesPages(args) {
			t.Errorf("%q serves pages, want it not to", args)
		}
	}
	if servesPages([]string{"magpie://import?x"}) != hasGUI {
		t.Errorf("a magpie: link serves pages %v, want it to as the app opens it (%v)", !hasGUI, hasGUI)
	}
}
