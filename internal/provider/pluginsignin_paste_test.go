package provider

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin's browser sign-in that comes back to a port of the plugin's on
// this machine can be finished from the address the browser ended on, for
// a magpie on a server or in Docker whose browser is elsewhere (Chicring):
// only this sign-in's port is taken, a page that sends the browser on is
// the next page to open, a wrong code fails, the right one signs in.
func TestPluginSignInTakesAPastedCallback(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_LOOPBACK", "1")
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
	in := map[string]string{"where": "work", "team": "blue"}
	start := func() (SignInState, string) {
		t.Helper()
		st, err := StartPluginSignIn("fakeco", 1, in)
		if err != nil {
			t.Fatal(err)
		}
		if !st.PasteCallback || st.PasteCode {
			t.Fatalf("a sign-in that comes back to a port here takes no pasted address: %+v", st)
		}
		u, _ := url.Parse(st.URL)
		return st, u.Query().Get("redirect_uri") + "?state=" + u.Query().Get("state")
	}

	st, back := start()
	if err := SubmitSignInCallback(st.ID, "http://localhost:1/callback?state=x&code=good"); err == nil {
		t.Fatal("another port's address was taken")
	}
	if err := SubmitSignInCallback(st.ID, "https://example.com/?code=good"); err == nil {
		t.Fatal("an address elsewhere was taken")
	}
	// Kiro's page sends an AWS sign-in on to AWS, which comes back here
	next := strings.Replace(back, "/callback", "/next", 1)
	if err := SubmitSignInCallback(st.ID, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := SignInStatus(st.ID); got.State != "waiting" || !strings.HasPrefix(got.URL, "https://aws.invalid/") {
		t.Fatalf("after a page that sends the browser on: %+v", got)
	}
	if err := SubmitSignInCallback(st.ID, back+"&code=bad"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refused code: %v", err)
	}
	if got, _ := SignInStatus(st.ID); got.State != "failed" {
		t.Fatalf("after a refused code: %+v", got)
	}

	st, back = start()
	// pasted as the browser shows it: localhost, which in a container may
	// not be where the plugin listens
	back = strings.Replace(back, "127.0.0.1", "localhost", 1)
	if err := SubmitSignInCallback(st.ID, "  "+back+"&code=good\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := SignInStatus(st.ID); got.State != "done" || got.User != "blue@fake" {
		t.Fatalf("after the pasted address: %+v", got)
	}
}

// The ports a sign-in page names as where it comes back to, as the
// community plugins' pages name them.
func TestLoopbackPorts(t *testing.T) {
	for page, want := range map[string]string{
		"https://app.devin.ai/auth/cli/continue?redirect_uri=http%3A%2F%2F127.0.0.1%3A5123%2Fcallback&state=s": "5123",
		"https://app.kiro.dev/signin?state=s&redirect_uri=http%3A%2F%2Flocalhost%3A3128&redirect_from=KiroIDE": "3128",
		"https://www.trae.ai/authorization?auth_callback_url=http%3A%2F%2F127.0.0.1%3A6001%2Fauthorize":        "6001",
		"https://zed.dev/native_app_signin?native_app_port=7002&native_app_public_key=k":                       "7002",
		// CodeArts' international station: a login page first, the
		// authorize page (and its port) in service= (#1090)
		"https://auth.huaweicloud.com/authui/login?site=intl&locale=en-us&service=" + url.QueryEscape("https://codearts.ap-southeast-1.huaweicloud.com/portal/authorize?theme=t&client_id=c&port=37093&code_challenge=x"): "37093",
		"https://codearts.huaweicloud.com/portal/authorize?theme=t&client_id=c&port=37094&code_challenge=x":                                                                                                               "37094",
		"https://zcode.z.ai/x?redirect_uri=https%3A%2F%2Fzcode.z.ai%2Fapp%2Foauth%2Flogin":                                                                                                                                "",
		"https://github.com/login/device": "",
	} {
		if got := strings.Join(loopbackPorts(page), ","); got != want {
			t.Errorf("loopbackPorts(%s) = %q, want %q", page, got, want)
		}
	}
	if PluginPastesCallback("commandcode-plan", "https://commandcode.ai/studio/auth/cli?callback=http%3A%2F%2F127.0.0.1%3A4000%2Fcallback") {
		t.Error("Command Code's page posts its key: there is no address to paste")
	}
}

// Command Code's page posts its key to the plugin's port from script, so
// a magpie on a server or in Docker has no address to paste. Its sign-in
// takes a key made on Command Code's keys page in place, through the
// plugin's API key way, as the built-in's did (#2468): an address is
// refused, a key signs in, and the plugin keeps it.
func TestPluginSignInTakesAPastedKey(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_LOOPBACK", "1")
	t.Setenv("FAKE_ID", CommandCodePlanID)
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
	st, err := StartPluginSignIn(CommandCodePlanID, 1, map[string]string{"where": "home"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.PasteKey || st.KeysURL != cmdKeysURL || st.PasteCallback {
		t.Fatalf("Command Code's browser sign-in takes a pasted key: %+v", st)
	}
	if err := SubmitSignInCallback(st.ID, "http://127.0.0.1:1/callback?code=x"); err == nil {
		t.Fatal("an address was taken as a key")
	}
	if got, _ := SignInStatus(st.ID); got.State != "waiting" {
		t.Fatalf("after a refused paste: %+v", got)
	}
	if err := SubmitSignInCallback(st.ID, "  user_cc_key1\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := SignInStatus(st.ID); got.State != "done" {
		t.Fatalf("after the pasted key: %+v", got)
	}
	auths := plugin.Auths(CommandCodePlanID)
	var kept bool
	for _, a := range auths {
		kept = kept || a["type"] == "api" && a["key"] == "user_cc_key1"
	}
	if !kept {
		t.Fatalf("the plugin didn't keep the key: %v", auths)
	}
}
