package provider

import "testing"

// errors=pass is a setting of a provider the user keys; a subscription's
// account or a plugin's isn't asked once, and has none.
func TestPassesErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Provider
		want bool
	}{
		{"keyed", Provider{ID: "relay", Key: "k", Chat: "https://x/v1", PassErrors: true}, true},
		{"keyed, left to magpie", Provider{ID: "relay", Key: "k", Chat: "https://x/v1"}, false},
		{"no endpoint", Provider{ID: "relay", PassErrors: true}, false},
		{"an account", Provider{ID: "codex", Account: &Account{}, Chat: "https://x/v1", PassErrors: true}, false},
	} {
		if got := c.p.PassesErrors(); got != c.want {
			t.Errorf("%s: PassesErrors = %v, want %v", c.name, got, c.want)
		}
	}
}
