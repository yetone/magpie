package provider

import (
	"path/filepath"
	"testing"
)

// A key+URL provider passes the agent's User-Agent on by default (#1851,
// nb5p on Discord), and goes as magpie only when set to: a signed-in
// account, a plugin's, and one that goes as a client of its vendor's own
// (OpenCode, Cline, Kilo, another magpie) keep the identity their signing
// gives.
func TestPassesUserAgent(t *testing.T) {
	for _, tt := range []struct {
		name string
		p    Provider
		want bool
	}{
		{"key", Provider{Chat: "https://api.example.com/v1"}, true},
		{"key, as magpie", Provider{MagpieUserAgent: true, Chat: "https://api.example.com/v1"}, false},
		{"an account", Provider{Chat: "https://api.z.ai/api/coding/paas/v4", Account: &Account{Agent: "zcode"}}, false},
		{"a plugin's", Provider{Chat: "https://x.example/v1", Account: &Account{Agent: "plugin:x"}}, false},
		{"opencode", Provider{Chat: "https://opencode.ai/zen/v1"}, false},
		{"cline", Provider{Preset: "clinepass", Chat: "https://api.cline.bot/api/v1"}, false},
		{"kilo", Provider{Preset: "kilo", Chat: "https://api.kilo.ai/api/gateway"}, false},
		{"another magpie", Provider{Preset: RemoteMagpiePreset, Chat: "http://10.0.0.2:3425/v1"}, false},
	} {
		if got := tt.p.PassesUserAgent(); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A copy of a provider set to go as magpie goes as magpie too, the form not
// carrying it.
func TestAddCopyMagpieUserAgent(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := Save(Provider{ID: "plan", Name: "Plan", Chat: "https://plan.example/v1", Key: "sk-1", MagpieUserAgent: true}); err != nil {
		t.Fatal(err)
	}
	id, err := AddCopy(Provider{Name: "Plan copy", Chat: "https://plan.example/v1", Models: []string{"m"}}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MagpieUserAgent {
		t.Fatal("the copy sends the agent's User-Agent")
	}
}
