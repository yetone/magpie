package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The provider editor's Advanced › Connect to from the CLI (quantum_lsfc on
// X: "是否可以支持cli去配置"): resolve.<host>= sets a host's connect
// addresses on a saved provider, an empty value takes the host out,
// resolve= clears them all, and a bad place, a made-up resolve= value or a
// vendor's preset is refused with nothing saved.
func TestProviderSetResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())

	if err := provider.Save(provider.Provider{ID: "corp", Name: "Corp", Chat: "https://api.corp.example/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	saved := func() map[string][]string {
		t.Helper()
		p, err := provider.Find("corp")
		if err != nil {
			t.Fatal(err)
		}
		return p.Resolve
	}
	set := func(pairs ...string) error {
		return providerCmd(append([]string{"provider", "set", "corp"}, pairs...))
	}

	if err := set("resolve.API.corp.example=10.0.0.5, dns:gw.corp.example,10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if got := saved()["api.corp.example"]; !slices.Equal(got, []string{"10.0.0.5", "dns:gw.corp.example"}) {
		t.Fatalf("saved %v", saved())
	}
	if err := set("resolve.other.corp.example=[fd00::5]:8443"); err != nil {
		t.Fatal(err)
	}
	if len(saved()) != 2 {
		t.Fatalf("a second host: %v", saved())
	}
	if err := set("resolve.other.corp.example="); err != nil {
		t.Fatal(err)
	}
	if r := saved(); len(r) != 1 || r["other.corp.example"] != nil {
		t.Fatalf("an empty value kept the host: %v", r)
	}

	for _, bad := range [][]string{
		{"resolve.api.corp.example=not an address"},
		{"resolve.10.0.0.9=10.0.0.5"},
		{"resolve=10.0.0.5"},
		{"resolve=--help"},
	} {
		if err := set(bad...); err == nil {
			t.Fatalf("%v was taken", bad)
		}
		if got := saved()["api.corp.example"]; !slices.Equal(got, []string{"10.0.0.5", "dns:gw.corp.example"}) || len(saved()) != 1 {
			t.Fatalf("%v changed what was saved: %v", bad, saved())
		}
	}

	if err := set("resolve="); err != nil {
		t.Fatal(err)
	}
	if r := saved(); len(r) != 0 {
		t.Fatalf("resolve= kept %v", r)
	}

	// a vendor's own host is never moved
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	err := providerCmd([]string{"provider", "set", "deepseek", "resolve.api.deepseek.com=10.0.0.5"})
	if err == nil || !strings.Contains(err.Error(), "connect address") {
		t.Fatalf("a preset at the vendor's host: %v", err)
	}
}
