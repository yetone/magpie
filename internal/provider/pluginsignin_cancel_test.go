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

// #5016, through the window's Cancel: a plugin sign-in cancelled while the
// plugin waits for its vendor's desktop app (AStudio's session file,
// plugins#658) saves nothing when the file comes after; and one whose
// authorize fails says the plugin's reason, not "s.a.callback is not a
// function".
func TestCancelledPluginSignInSavesNothing(t *testing.T) {
	bun := testenv.Bun(t)
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/signinend/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := StartPluginSignIn("signinend", 2, nil); err == nil || !strings.Contains(err.Error(), "this AStudio account is banned") {
		t.Fatalf("a failed authorize: %v", err)
	}

	st, err := StartPluginSignIn("signinend", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	marked := func(name string) bool {
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				return true
			}
		}
		return false
	}
	if !marked("waiting") {
		t.Fatal("the plugin never began waiting")
	}
	CancelSignIn(st.ID)
	if got, _ := SignInStatus(st.ID); got.State != "canceled" {
		t.Fatalf("after Cancel: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(home, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !marked("returned") {
		t.Fatal("the plugin's wait never came true")
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if plugin.SignedIn("signinend") {
		b, _ := os.ReadFile(plugin.AuthPath())
		t.Fatalf("a cancelled sign-in was saved: %s", b)
	}
	if got, _ := SignInStatus(st.ID); got.State != "canceled" {
		t.Fatalf("after the plugin answered: %+v", got)
	}
}
