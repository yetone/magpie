package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// While the host hasn't listed the providers (offline, or a plugin that
// fails), Cached reads plugin-providers.json once for each version of the
// file: it read and decoded the whole file on every call, a few hundred KB
// for each look at a plugin's model from the window's drift poll.
func TestCachedReadsSavedProvidersOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	l, _ := json.Marshal(List{Plugins: []Entry{{Spec: "one"}}})
	if err := os.WriteFile(listPath(), l, 0o600); err != nil {
		t.Fatal(err)
	}
	var ps []Provider
	for i := range 300 {
		spec := "other"
		if i == 7 {
			spec = "one"
		}
		ps = append(ps, Provider{ID: fmt.Sprintf("p%d", i), Spec: spec, Name: fmt.Sprintf("P %d", i),
			Methods: []Method{{Type: "oauth", Label: "Sign in"}}, Models: []Model{{ID: "m1", Name: "M one"}, {ID: "m2", Name: "M two"}}})
	}
	b, _ := json.Marshal(ps)
	if err := os.WriteFile(providersPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	provMu.Lock()
	was := [4]any{provCache, provGood, provTried, provBusy}
	provCache, provGood, provTried = nil, false, true // no refresh starts
	provMu.Unlock()
	t.Cleanup(func() {
		provMu.Lock()
		provCache, provGood, provTried, provBusy = was[0].([]Provider), was[1].(bool), was[2].(bool), was[3].(bool)
		provMu.Unlock()
	})

	got := Cached()
	if len(got) != 1 || got[0].ID != "p7" || len(got[0].Models) != 2 {
		t.Fatalf("Cached = %+v, want p7 from the saved file", got)
	}
	got[0].Name = "changed"
	if again := Cached(); again[0].Name != "P 7" {
		t.Fatalf("a change to what Cached gave reached the next: %q", again[0].Name)
	}
	// 300 providers decoded is thousands; one provider given is a few
	n := testing.AllocsPerRun(20, func() { Cached() })
	for range 4 {
		n = min(n, testing.AllocsPerRun(20, func() { Cached() }))
	}
	if n > 100 {
		t.Fatalf("Cached of a 300-provider saved file made %v allocations, want at most 100", n)
	}

	// a new version of the file is read
	ps[7].Name = "P seven"
	b, _ = json.Marshal(ps)
	if err := os.WriteFile(providersPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Cached(); len(got) != 1 || got[0].Name != "P seven" {
		t.Fatalf("after the file changed Cached = %+v, want the new name", got)
	}
}
