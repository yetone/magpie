package agent

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group with max ticked offers max in omp (whqtian on Discord:
// only xhigh was listed though max was picked, while Pi had it). omp has
// taken max in models.yml since 16.4.0; only an older one, or one whose
// version isn't known, has max stand in as xhigh. A group that names both
// had no way to max at all, as the gateway keeps an xhigh asked for.
func TestOmpGroupOffersMax(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"sol", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/sol", []string{"low", "medium", "high", "xhigh", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/flash", []string{"low", "high", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "mix", Members: []string{"a/sol"}, Levels: []string{"high", "xhigh", "max"}}); err != nil {
		t.Fatal(err)
	}
	efforts := func(version, id string) string {
		was := ompVersion
		ompVersion = func() string { return version }
		defer func() { ompVersion = was }()
		for _, m := range ompProvider().Models {
			if m.ID == id && m.Thinking != nil {
				return fmt.Sprint(m.Thinking.Efforts)
			}
		}
		return "none"
	}
	for _, c := range []struct{ version, id, want string }{
		{"18.4.8", "group/mix", "[high xhigh max]"},
		{"16.4.0", "group/mix", "[high xhigh max]"},
		{"18.4.8", "a/flash", "[low high max]"},
		{"16.3.5", "group/mix", "[high xhigh]"},
		{"16.3.5", "a/flash", "[low high xhigh]"},
		{"", "group/mix", "[high xhigh]"},
	} {
		if got := efforts(c.version, c.id); got != c.want {
			t.Errorf("omp %q, %s: efforts %s, want %s", c.version, c.id, got, c.want)
		}
	}
}
