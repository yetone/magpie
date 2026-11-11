package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model context-cap 200k holds every model's window to 200K, off
// takes it away, and a value that is not a length is refused with the cap
// as it was (WJaunita45604 on X).
func TestModelContextCapCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"context-cap", "200k"}); err != nil || settings.Load().ContextCap != 200000 {
		t.Fatal(err, settings.Load().ContextCap)
	}
	if err := modelCmd([]string{"context-cap"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"maybe", "-5", "0k"} {
		if err := modelCmd([]string{"context-cap", bad}); err == nil || settings.Load().ContextCap != 200000 {
			t.Fatalf("took %q: %v, cap %d", bad, err, settings.Load().ContextCap)
		}
	}
	if err := modelCmd([]string{"context-cap", "off"}); err != nil || settings.Load().ContextCap != 0 {
		t.Fatal(err, settings.Load().ContextCap)
	}
}
