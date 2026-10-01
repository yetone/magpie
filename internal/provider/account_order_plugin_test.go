package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin provider's accounts are arranged like a built-in's: the page
// names them by the provider's id, magpie keeps them as plugin:<id>, and
// the gateway tries them in the order dragged.
func TestAccountOrderPluginAccounts(t *testing.T) {
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
	users := []string{"a@fake", "b@fake", "c@fake"}
	for _, user := range users {
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
	order := []string{"c@fake", "a@fake", "b@fake"}
	if err := SetAccountOrder(id, order); err != nil {
		t.Fatal(err)
	}
	shown, _ := loginUsers(Logins(id))
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shown, order) || !reflect.DeepEqual(loginRoutingUsers(p), order) {
		t.Fatalf("display=%v routing=%v", shown, loginRoutingUsers(p))
	}
}
