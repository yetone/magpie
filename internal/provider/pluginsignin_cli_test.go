package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin whose vendor CLI isn't on this machine had its sign-in fail
// with "install it first", which a magpie in a container can't do
// (niuniu_8964 on X: agy-cli in Docker). A plugin that says how to install
// it (auth.cli) has its sign-in install it first, "Installing {name}…" as
// a moved built-in's, then open the page; one whose install fails says
// why and how to do it by hand; with the CLI there, nothing is installed.
func TestPluginSignInInstallsItsCLI(t *testing.T) {
	bun := testenv.Bun(t)
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/cliinstall/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	settled := func(id string) SignInState {
		t.Helper()
		for range 500 {
			if st, _ := SignInStatus(id); st.State != "installing" {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the sign-in is still installing")
		return SignInState{}
	}

	if got := PluginCLIMissing("cliinstall"); got != "Fake CLI" {
		t.Fatalf("PluginCLIMissing = %q", got)
	}
	os.WriteFile(filepath.Join(home, "cli-mode"), []byte("fail"), 0o600)
	st, err := StartPluginSignIn("cliinstall", 0, nil)
	if err != nil || st.State != "installing" || st.Installing != "Fake CLI" {
		t.Fatalf("StartPluginSignIn = %+v, %v", st, err)
	}
	if got := settled(st.ID); got.State != "failed" || !strings.Contains(got.Error, "the download was refused") || !strings.Contains(got.Error, "fake.invalid/install.sh") {
		t.Fatalf("a failed install: %+v", got)
	}

	os.Remove(filepath.Join(home, "cli-mode"))
	st, err = StartPluginSignIn("cliinstall", 0, nil)
	if err != nil || st.State != "installing" {
		t.Fatalf("StartPluginSignIn = %+v, %v", st, err)
	}
	if got := settled(st.ID); got.State != "waiting" || got.URL != "https://cliinstall.invalid/login" {
		t.Fatalf("after the install: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(home, "fake-cli")); err != nil {
		t.Fatal("the plugin's CLI wasn't installed")
	}

	if got := PluginCLIMissing("cliinstall"); got != "" {
		t.Fatalf("installed, PluginCLIMissing = %q", got)
	}
	st, err = StartPluginSignIn("cliinstall", 0, nil)
	if err != nil || st.State != "waiting" || st.URL != "https://cliinstall.invalid/login" {
		t.Fatalf("with the CLI there: %+v, %v", st, err)
	}
	CancelSignIn(st.ID)
}
