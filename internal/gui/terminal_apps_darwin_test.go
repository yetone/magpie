//go:build darwin && cgo

package gui

import "testing"

func TestDiscoverTerminals(t *testing.T) {
	found, err := discoverTerminals()
	if err != nil {
		t.Fatal(err)
	}
	terminalFound := false
	for _, app := range found.Apps {
		if app.ID == "com.apple.TextEdit" {
			t.Fatal("a text editor was offered as a session terminal")
		}
		if app.ID == terminalBundleID && app.Path != "" {
			terminalFound = true
		}
	}
	if !terminalFound {
		t.Fatalf("Terminal.app missing from .command handlers: %+v", found.Apps)
	}
}
