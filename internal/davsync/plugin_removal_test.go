package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// pluginsHere is the specs in this computer's plugins.json.
func pluginsHere() []string {
	var out []string
	for _, e := range plugin.Load().Plugins {
		out = append(out, e.Spec)
	}
	return out
}

func setPlugins(t *testing.T, specs ...string) {
	t.Helper()
	var l plugin.List
	l.Plugins = []plugin.Entry{}
	for _, s := range specs {
		l.Plugins = append(l.Plugins, plugin.Entry{Spec: s})
	}
	b, _ := json.Marshal(l)
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// dolacmeo on X shares one WebDAV config across computers: a plugin removed
// on one came back from another, which still held it and sent it up again.
// A removal is now one too: a plugin both computers had at the last sync and
// one side no longer has is removed on the other, however the part that
// carries it came out; a plugin added meanwhile stays.
func TestSyncPluginRemovalSpreads(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	oldAdd, oldRemove := addPlugins, removePlugin
	t.Cleanup(func() { addPlugins, removePlugin = oldAdd, oldRemove })
	var removed []string
	addPlugins = func(es []plugin.Entry) ([]string, map[string]string) {
		here := pluginsHere()
		for _, e := range es {
			if !slices.Contains(here, e.Spec) {
				here = append(here, e.Spec)
			}
		}
		setPlugins(t, here...)
		return nil, nil
	}
	removePlugin = func(name string) error {
		removed = append(removed, name)
		setPlugins(t, slices.DeleteFunc(pluginsHere(), func(s string) bool { return plugin.Name(s) == name })...)
		return nil
	}
	const x, y, z = "@magpie-community/opencode-x-auth", "@magpie-community/opencode-y-auth", "@magpie-community/opencode-z-auth"
	a, b := newComputer(t), newComputer(t)

	a.use(t)
	setPlugins(t, x, y)
	Configure(cfg)
	now(t)
	b.use(t)
	Configure(cfg)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{x, y}) {
		t.Fatalf("b has %v after joining, want both", got)
	}
	a.use(t)
	now(t) // a sees b's join: both hold both

	// a removes x and syncs; b follows it, and doesn't send x back
	setPlugins(t, y)
	now(t)
	b.use(t)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{y}) || !slices.Equal(removed, []string{plugin.Name(x)}) {
		t.Fatalf("b has %v, removed %v after a removed x; want only y, x removed", got, removed)
	}
	now(t)
	a.use(t)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{y}) {
		t.Fatalf("a has %v after b's sync, want x not back", got)
	}

	// both change: b adds z (and edits providers) while a removes y; the
	// removal still reaches b, whichever side's providers part wins
	b.use(t)
	setPlugins(t, y, z)
	if err := os.WriteFile(provider.Path(), []byte(`{"providers":[{"id":"acme","name":"Acme","chat":"https://acme.example.invalid/v1","key":"sk-acme"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.use(t)
	setPlugins(t)
	now(t)
	b.use(t)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{z}) {
		t.Fatalf("b has %v, want y gone and its own z kept", got)
	}
	a.use(t)
	now(t)
	if got := pluginsHere(); slices.Contains(got, y) {
		t.Fatalf("a has %v, want y not back", got)
	}
}
