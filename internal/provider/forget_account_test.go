package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// #694: a plugin's subscription removed from magpie was listed among the
// removed as "plugin", and nothing signed it out, so adding it back always
// brought the old account back. Removed, it is named as its row was, and
// ForgetAccount signs its accounts out of plugin-auth.json and logins.json:
// it leaves the removed list, and a sign-in later starts afresh.
func TestForgetRemovedPluginAccount(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	var rows []savedLogin
	for _, user := range []string{"a@fake", "b@fake"} {
		k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-" + user, "access": "a", "expires": 9e15, "accountId": user})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id := PluginID("fakeco")
	if _, err := Find(id); err != nil {
		t.Fatal(err)
	}
	if err := Delete(id); err != nil {
		t.Fatal(err)
	}
	removed := func() (Exclusion, bool) {
		i := slices.IndexFunc(Excluded(), func(x Exclusion) bool { return x.Provider == id })
		if i < 0 {
			return Exclusion{}, false
		}
		return Excluded()[i], true
	}
	x, ok := removed()
	if !ok {
		t.Fatalf("%s not among the removed: %+v", id, Excluded())
	}
	if x.Name != "FakeCo" {
		t.Fatalf("the removed plugin account is named %q (agent %q), want FakeCo", x.Name, x.Agent)
	}

	if err := ForgetAccount(id); err != nil {
		t.Fatal(err)
	}
	if a := plugin.Auths("fakeco"); len(a) != 0 {
		t.Fatalf("plugin-auth.json still holds %d sign-ins of fakeco", len(a))
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	ForgetAccounts()
	if x, ok := removed(); ok {
		t.Fatalf("still listed as removed after it was signed out: %+v", x)
	}
	if ls := Logins(id); len(ls) != 0 {
		t.Fatalf("its accounts are still listed: %+v", ls)
	}
	for _, l := range readLogins() {
		if l.Agent == "plugin:fakeco" {
			t.Fatalf("logins.json still keeps %+v", l)
		}
	}
	// added back now, there is nothing to bring: a new sign-in is new
	if err := ShowAccount(id); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(id); err == nil {
		t.Fatalf("%s came back with an account after it was signed out", id)
	}
}
