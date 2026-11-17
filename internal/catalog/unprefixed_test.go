package catalog

import "testing"

// unprefixedAs puts a maker's name back only where the id is the
// canonical's without it, or goes on past it to a model models.dev knows.
func TestUnprefixedAs(t *testing.T) {
	load()
	saved := normal
	t.Cleanup(func() { normal = saved })
	normal = map[string]string{"kimi-k3-1-pro": "kimi-k3.1-pro", "kimi-k3-1-pro-highspeed": "kimi-k3.1-pro-highspeed", "kimi-k3": "kimi-k3"}
	for _, tc := range []struct{ b, c, want string }{
		{"k3-1-pro", "kimi-k3-1-pro", "kimi-k3.1-pro"},
		{"k3", "kimi-k3", "kimi-k3"},
		{"k3-1-pro-highspeed", "kimi-k3-1-pro", "kimi-k3.1-pro-highspeed"},
		{"k3-256k", "kimi-k3", ""},    // kimi-k3-256k: no model of that id
		{"k3-1", "kimi-k3-1-pro", ""}, // K3.1 is not K3.1 Pro
		{"k3", "kimi-k3-1-pro", ""},   // nor K3
		{"pro", "kimi-k3-1-pro", ""},  // a version is no maker's name
		{"1-pro", "kimi-k3-1-pro", ""},
		{"m2-her", "minimax-m2-her", "minimax-m2-her"},
		{"luna", "gpt-6-luna", ""},
	} {
		if got := unprefixedAs(tc.b, tc.c); got != tc.want {
			t.Errorf("unprefixedAs(%q, %q) = %q, want %q", tc.b, tc.c, got, tc.want)
		}
	}
}
