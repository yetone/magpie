package gui

import (
	"reflect"
	"strings"
	"testing"
)

func TestTerminalOpenArgs(t *testing.T) {
	found := terminalDiscovery{
		Apps: []terminalApp{
			{ID: terminalBundleID, Name: "Terminal", Path: "/System/Applications/Utilities/Terminal.app"},
			{ID: "com.mitchellh.ghostty", Name: "Ghostty", Path: "/Applications/Ghostty.app"},
		},
		Default: "com.mitchellh.ghostty",
	}
	for _, tc := range []struct {
		name, choice string
		want         []string
	}{
		{"system default", "", []string{"-a", "/Applications/Ghostty.app"}},
		{"Terminal", terminalBundleID, []string{"-a", "Terminal"}},
		{"explicit system", "system", []string{"-a", "/Applications/Ghostty.app"}},
		{"chosen app", "com.mitchellh.ghostty", []string{"-a", "/Applications/Ghostty.app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := terminalOpenArgs(tc.choice, found)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("args = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := terminalOpenArgs("org.missing.term", found); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("missing app: %v", err)
	}
	found.Default = terminalBundleID
	if got, err := terminalOpenArgs("", found); err != nil || !reflect.DeepEqual(got, []string{"-a", "/System/Applications/Utilities/Terminal.app"}) {
		t.Fatalf("Terminal as system default: %q, %v", got, err)
	}
	found.Default = "org.editor"
	if got, err := terminalOpenArgs("", found); err != nil || !reflect.DeepEqual(got, []string{"-a", "Terminal"}) {
		t.Fatalf("editor as default: %q, %v", got, err)
	}
	if got, err := terminalOpenArgs("", terminalDiscovery{}); err != nil || !reflect.DeepEqual(got, []string{"-a", "Terminal"}) {
		t.Fatalf("nothing found: %q, %v", got, err)
	}
}
