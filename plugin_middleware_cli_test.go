package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
)

// magpie plugin says which plugins are gateway middleware and their hooks,
// or why one didn't load; a middleware plugin has no provider to sign in
// to, so the list had nothing under it.
func TestPluginListSaysMiddleware(t *testing.T) {
	groupsHome(t)
	t.Cleanup(plugin.Settle) // what adding the plugins told ends with the test
	dir := t.TempDir()
	good := filepath.Join(dir, "alias.middleware.js")
	bad := filepath.Join(dir, "bad.middleware.js")
	os.WriteFile(good, []byte("export const events = [\"message_start\"]\nexport function onRequest(b, ctx) {}\nexport function onEvent(e, ctx) {}\n"), 0o644)
	os.WriteFile(bad, []byte("export function onRequest( {\n"), 0o644)
	for _, spec := range []string{good, bad} {
		if _, err := plugin.Add(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	middleware.Reload()
	out, err := stdoutOf(t, func() error { return listPlugins(context.Background(), false) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gateway middleware  onRequest, onEvent") {
		t.Errorf("the middleware's hooks aren't listed:\n%s", out)
	}
	if !strings.Contains(out, "didn't load: ") || !strings.Contains(out, "SyntaxError") {
		t.Errorf("the broken middleware doesn't say why:\n%s", out)
	}
	js, _ := stdoutOf(t, func() error { return listPlugins(context.Background(), true) })
	if !strings.Contains(js, `"middleware"`) || !strings.Contains(js, `"message_start"`) {
		t.Errorf("--json has no middleware:\n%s", js)
	}
}

// magpie plugin off, on and rm take a plugin's short name, as options
// does: `magpie plugin off think-tags` said there was no such plugin.
func TestPluginOnOffRmShortName(t *testing.T) {
	groupsHome(t)
	t.Cleanup(plugin.Settle)
	dir := filepath.Join(t.TempDir(), "middleware-zz-tags")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "@magpie-community/middleware-zz-tags", "magpie": {"middleware": "./zz.middleware.js"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "zz.middleware.js"), []byte("export function onRequest(b) {}\n"), 0o644)
	if _, err := plugin.Add(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	off := func() bool { return plugin.Load().Plugins[0].Off }
	if _, err := stdoutOf(t, func() error { return pluginCmd([]string{"plugin", "off", "zz-tags"}) }); err != nil || !off() {
		t.Fatalf("off by short name: err %v, off %v", err, off())
	}
	if _, err := stdoutOf(t, func() error { return pluginCmd([]string{"plugin", "on", "zz-tags"}) }); err != nil || off() {
		t.Fatalf("on by short name: err %v, off %v", err, off())
	}
	if _, err := stdoutOf(t, func() error { return pluginCmd([]string{"plugin", "rm", "zz-tags"}) }); err != nil || len(plugin.Load().Plugins) != 0 {
		t.Fatalf("rm by short name: err %v, left %v", err, plugin.Load().Plugins)
	}
	if err := pluginCmd([]string{"plugin", "off", "nothing-here"}); err == nil {
		t.Error("a name no plugin has was taken")
	}
}
