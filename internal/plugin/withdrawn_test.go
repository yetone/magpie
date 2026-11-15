package plugin

import (
	"context"
	"strings"
	"testing"
)

// A withdrawn package (Antigravity's, owner 11-15) is given to nobody new:
// Add turns it away before anything is installed, however its package is
// written, and says what to use instead. A user who has it listed
// keeps it (an update, or an add over it), a move of the user's own
// accounts may install it, and WithdrawnKept names it for the page's
// notice only while it is listed.
func TestWithdrawnGivenToNobodyNew(t *testing.T) {
	storeSandbox(t)
	const pkg = "@magpie-community/opencode-antigravity-auth"
	ctx := context.Background()
	for _, spec := range []string{pkg, pkg + "@0.1.18", "npm:" + pkg} {
		_, err := Add(ctx, spec)
		if err == nil || !strings.Contains(err.Error(), "@magpie-community/opencode-agy-cli-auth") {
			t.Fatalf("Add(%q) = %v, want it turned away pointing to agy-cli", spec, err)
		}
	}
	if len(Load().Plugins) != 0 {
		t.Fatalf("a turned-away add listed %v", Load().Plugins)
	}
	if got := WithdrawnKept(); len(got) != 0 {
		t.Fatalf("WithdrawnKept with nothing listed = %v", got)
	}
	if err := RefuseWithdrawn(KeepWithdrawn(ctx), pkg); err != nil {
		t.Fatalf("a move of the user's own accounts was turned away: %v", err)
	}
	if err := RefuseWithdrawn(ctx, "@magpie-community/opencode-agy-cli-auth"); err != nil {
		t.Fatalf("agy-cli was turned away: %v", err)
	}

	writeList(t, `{"plugins":[{"spec":"`+pkg+`@latest","off":true}]}`)
	if err := RefuseWithdrawn(ctx, pkg); err != nil {
		t.Fatalf("a user who has it (turned off) was turned away: %v", err)
	}
	got := WithdrawnKept()
	if len(got) != 1 || got[0].ID != "antigravity" || got[0].Instead != "Antigravity (local agy CLI)" {
		t.Fatalf("WithdrawnKept = %+v", got)
	}
}
