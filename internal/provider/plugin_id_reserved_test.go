package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// An installed plugin's provider id is kept for it signed in or not, as a
// built-in subscription's is: a provider of the user's own saved on it, or
// renamed onto it, would hide the plugin's subscription once signed in.
func TestPluginIDReservedSignedOut(t *testing.T) {
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
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("fakeco"); err == nil {
		t.Fatal("fakeco is listed before it is signed in")
	}
	// as the built-in codex, signed out, is kept
	for _, id := range []string{"codex", "fakeco"} {
		if err := Save(Provider{ID: id, Name: id, Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"m"}}); err == nil {
			t.Fatalf("a provider of a key was saved on %s", id)
		}
		if got := freeID(id); got != id+"-2" {
			t.Fatalf("freeID(%s) = %s", id, got)
		}
	}
	if err := Save(Provider{ID: "mine", Name: "mine", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex", "fakeco"} {
		if err := Rename("mine", id); err == nil {
			t.Fatalf("a provider was renamed onto %s", id)
		}
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k-123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("fakeco"); err != nil || !p.IsPlugin() {
		t.Fatalf("Find(fakeco) = %+v, %v", p, err)
	}
}
