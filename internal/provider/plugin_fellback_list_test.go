package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// An account whose list fell back (its plugin couldn't read the vendor's
// list for it and gave its own defaults back) doesn't say what its plan
// has: it serves every model, as a built-in account whose fetch failed
// does, never only the plugin's short defaults. bitssly on X (商汤小浣熊,
// v0.1.4573): three accounts ticked, the one_time_purchase one left out
// of sn-kimi-k3-1-pro's chain, as the plugin's defaults lack the model.
func TestFellBackPluginAccountServesEveryModel(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	spec := "@magpie-community/opencode-raccoon-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{
		"raccoon":   {"type": "oauth", "access": "a1", "refresh": "r1", "expires": 4102444800000},
		"raccoon#2": {"type": "oauth", "access": "a2", "refresh": "r2", "expires": 4102444800000},
		"raccoon#3": {"type": "oauth", "access": "a3", "refresh": "r3", "expires": 4102444800000},
	})
	live := []string{"sn-glm-5-3", "sn-kimi-k3-1-pro", "sn-sensenova-6-8-flash"}
	defaults := []string{"raccoon-8c4485", "sn-glm-5-3", "sn-kimi-k3", "sn-sensenova-6-8-flash"}
	plugin.UseCached([]plugin.Provider{{ID: "raccoon", Spec: spec, Name: "商汤小浣熊",
		Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}},
		Accounts: []plugin.Account{
			{Key: "raccoon", Type: "oauth", Models: live},
			{Key: "raccoon#2", Type: "oauth", Models: defaults, FellBack: true, ListError: "HTTP 403"},
			{Key: "raccoon#3", Type: "oauth", Models: live},
		},
		Models: listedModels(t, `[{"id": "raccoon-8c4485", "name": "Raccoon-Work"}, {"id": "sn-glm-5-3", "name": "GLM-5-3"}, {"id": "sn-kimi-k3", "name": "Kimi-K3"}, {"id": "sn-kimi-k3-1-pro", "name": "Kimi-K3.1-Pro"}, {"id": "sn-sensenova-6-8-flash", "name": "SenseNova-6.8-Flash"}]`),
	}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	saveLogins(t,
		savedLogin{Agent: "plugin:raccoon", User: "free-a", Home: "raccoon", On: true},
		savedLogin{Agent: "plugin:raccoon", User: "paid-b", Home: "raccoon#2", On: true},
		savedLogin{Agent: "plugin:raccoon", User: "free-c", Home: "raccoon#3", On: true},
	)
	ps := pluginAccounts()
	i := slices.IndexFunc(ps, func(p Provider) bool { return p.ID == "raccoon" })
	if i < 0 {
		t.Fatalf("no raccoon provider in %v", ps)
	}
	out, left, barred := ps[i].AccountsFor("sn-kimi-k3-1-pro", nil)
	var users []string
	for _, a := range out {
		users = append(users, a.Account.User)
	}
	if len(out) != 3 || len(left) != 0 || len(barred) != 0 {
		t.Fatalf("sn-kimi-k3-1-pro goes to %v (left out %d, barred %d), want all three accounts", users, len(left), len(barred))
	}
	// a list the plugin really told still leaves a model out
	_, left, _ = ps[i].AccountsFor("sn-kimi-k3", nil)
	if len(left) != 2 {
		t.Fatalf("sn-kimi-k3 leaves out %d accounts, want the two whose own list lacks it", len(left))
	}
	for _, l := range pluginLogins(mustCached(t, "raccoon")) {
		if l.acct.Key != "raccoon#2" {
			continue
		}
		if got := pluginAccountListed(ps[i], l.User); got != nil {
			t.Fatalf("the fell-back account's own list is %v, want none told", got)
		}
	}
}
