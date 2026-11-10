package main

import (
	"testing"

	"github.com/yetone/magpie/internal/lazyre"
)

// TestAllLazyPatternsCompile compiles every regexp the binary's packages
// compile on first use (internal/lazyre), so a bad one fails here and not
// on a user's machine.
func TestAllLazyPatternsCompile(t *testing.T) {
	if n := lazyre.MustAll(); n < 500 {
		t.Fatalf("only %d lazy patterns registered; the binary's packages hold more", n)
	}
}
