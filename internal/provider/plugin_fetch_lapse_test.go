package provider

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's fetch hook that throws on its sign-in (the vendor turned the
// refresh away) marks the account lapsed, as a built-in's refused refresh
// marked it; the other account is left alone.
func TestPluginFetchSignInExpiredLapses(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_BASE", "http://127.0.0.1:9/v1")
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	var rows []savedLogin
	for user, refresh := range map[string]string{"revoked@fake": "r-revoked", "ok@fake": "r-ok"} {
		k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": refresh, "access": "a", "expires": 9e15, "accountId": user})
		if err != nil {
			t.Fatal(err)
		}
		keys[user] = k
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pp := mustPlugin(t)
	req, _ := http.NewRequestWithContext(ctx, "POST", pluginBase+pp.ID+"/v1/chat/completions", strings.NewReader(`{"model":"fake-claude","messages":[]}`))
	if _, err := pluginFetch(pp, keys["revoked@fake"], req); err == nil {
		t.Fatal("the fetch went through")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		lapsed := map[string]bool{}
		for _, l := range pluginLogins(mustPlugin(t)) {
			lapsed[l.User] = l.Lapsed != ""
		}
		if lapsed["revoked@fake"] && !lapsed["ok@fake"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lapsed: %v", lapsed)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
