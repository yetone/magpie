package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/lazyre"
)

// TestAllCompile compiles every pattern this package compiles on first
// use, so a bad one fails here and not on a user's request.
func TestAllCompile(t *testing.T) {
	if n := lazyre.MustAll(); n == 0 {
		t.Fatal("no lazy patterns registered")
	}
}
