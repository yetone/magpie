package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// errors= sets whether a keyed provider's errors go to the agent as the
// vendor sent them (ne9ko9 on Discord); a signed-in account takes none.
func TestProviderSetErrors(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://x/v1"}
	for _, c := range []struct {
		v    string
		want bool
	}{{"pass", true}, {"magpie", false}, {"pass-through", true}, {"default", false}} {
		if err := applyPairs(&p, []string{"errors=" + c.v}); err != nil || p.PassErrors != c.want {
			t.Fatalf("errors=%s: %v, got %v", c.v, err, p.PassErrors)
		}
	}
	p.PassErrors = true
	if err := applyPairs(&p, []string{"errors=loud"}); err == nil || !p.PassErrors {
		t.Fatalf("errors=loud taken: %v", err)
	}
	a := provider.Provider{ID: "codex", Name: "Codex", Account: &provider.Account{}, Chat: "https://x/v1"}
	if err := applyPairs(&a, []string{"errors=pass"}); err == nil || a.PassErrors {
		t.Fatalf("an account took errors=pass: %v", err)
	}
}
