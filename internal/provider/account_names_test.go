package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// #1515: a plugin's signed-in account (ZCode's, through
// opencode-zcode-auth) takes a name of the user's own. It is magpie's,
// in providers.json: plugin-auth.json and logins.json are not written.
func TestAccountNameOfAPluginAccount(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": "31****36@qq.com"})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "31****36@qq.com", Home: k, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id := PluginID("fakeco")
	read := func(p string) []byte { b, _ := os.ReadFile(p); return b }
	auth, logins := read(plugin.AuthPath()), read(loginsPath())
	if len(auth) == 0 || len(logins) == 0 {
		t.Fatalf("no sign-in to keep: auth %d bytes, logins %d", len(auth), len(logins))
	}

	if err := SetAccountName(id, "31****36@QQ.com", "  某某·GLM\n高级版 "); err != nil {
		t.Fatal(err)
	}
	if got := AccountNameOf(id, "31****36@qq.com"); got != "某某·GLM 高级版" {
		t.Fatalf("name %q", got)
	}
	if !bytes.Equal(read(plugin.AuthPath()), auth) || !bytes.Equal(read(loginsPath()), logins) {
		t.Fatal("naming an account wrote to its sign-in")
	}
	// on its card, as every surface reads it
	qs := withAccountNames([]SubscriptionQuota{{Provider: id, User: "31****36@qq.com"}})
	if qs[0].Alias != "某某·GLM 高级版" {
		t.Fatalf("card alias %q", qs[0].Alias)
	}
	for _, bad := range []struct{ ref, name string }{
		{"nobody@qq.com", "x"},
		{"31****36@qq.com", strings.Repeat("名", MaxAccountName+1)},
	} {
		if err := SetAccountName(id, bad.ref, bad.name); err == nil {
			t.Fatalf("%q named %q", bad.ref, bad.name)
		}
	}
	// a key provider's keys are named in their own rows
	if err := Save(Provider{ID: "zhipu-team", Name: "某某·GLM高级版", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "k", KeyName: "张三"}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountName("zhipu-team", "张三", "x"); err == nil {
		t.Fatal("a key provider took an account's name")
	}
	if err := SetAccountName(id, "31****36@qq.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := AccountNameOf(id, "31****36@qq.com"); got != "" {
		t.Fatalf("cleared name %q", got)
	}
}
