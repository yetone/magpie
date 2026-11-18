package ccswitchlink

import (
	"errors"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files.
func TestMain(m *testing.M) { testenv.Main(m) }

func TestIs(t *testing.T) {
	for l, want := range map[string]bool{
		"ccswitch://v1/import?resource=provider&app=claude&name=x": true,
		"CCSwitch://v1/import":     true,
		"magpie://import?preset=x": false,
		"https://ccswitch.io":      false,
		"":                         false,
	} {
		if Is(l) != want {
			t.Errorf("Is(%q) = %v", l, !want)
		}
	}
	if err := Forward("magpie://import?preset=x"); err == nil || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a magpie:// link was taken: %v", err)
	}
}
