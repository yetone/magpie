package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's sign-in is followed like a built-in one: the page, then the
// code pasted back (a wrong one fails the sign-in, which starts again);
// done, its provider is there, and removing it signs out. A key signs in
// at once.
func TestPluginSignIn(t *testing.T) {
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

	in := map[string]string{"where": "work", "team": "blue"}
	wait := func(id string) SignInState {
		t.Helper()
		for range 100 {
			if st, _ := SignInStatus(id); st.State != "waiting" {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the sign-in is still waiting")
		return SignInState{}
	}
	st, err := StartPluginSignIn("fakeco", 1, in)
	if err != nil || st.Agent != "fakeco" || st.State != "waiting" || !st.PasteCode || !strings.Contains(st.URL, "where=work") || st.Instructions != "Paste the code" {
		t.Fatalf("StartPluginSignIn = %+v, %v", st, err)
	}
	if err := SubmitSignInCallback(st.ID, " "); err == nil {
		t.Fatal("an empty code was taken")
	}
	if err := SubmitSignInCallback(st.ID, "bad"); err == nil {
		t.Fatal("a wrong code was taken")
	}
	if got := wait(st.ID); got.State != "failed" {
		t.Fatalf("after a wrong code: %+v", got)
	}
	if err := SubmitSignInCallback(st.ID, "good"); err == nil {
		t.Fatal("a failed sign-in took another code")
	}

	st, _ = StartPluginSignIn("fakeco", 1, in)
	if err := SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if got := wait(st.ID); got.State != "done" || got.User != "blue@fake" {
		t.Fatalf("after the code: %+v", got)
	}
	if p, err := Find("fakeco"); err != nil || !p.IsPlugin() {
		t.Fatalf("Find = %+v, %v", p, err)
	}
	// removed, it is hidden as a built-in is, still signed in
	if err := Delete("fakeco"); err != nil {
		t.Fatal(err)
	}
	if !plugin.SignedIn("fakeco") {
		t.Fatal("removing the provider signed out")
	}
	if _, err := Find("fakeco"); err == nil {
		t.Fatal("fakeco is a provider after it was removed")
	}
	if h := Hidden(); len(h) != 1 || h[0].ID != "fakeco" {
		t.Fatalf("Hidden = %+v", h)
	}

	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "  "); err == nil {
		t.Fatal("an empty key was taken")
	}
	if id, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k1"); err != nil || id != "fakeco" {
		t.Fatalf("PluginAPIKey = %q, %v", id, err)
	}
	if _, err := Find("fakeco"); err != nil {
		t.Fatal(err)
	}
}

// A plugin's provider takes more than one account, as a built-in
// subscription does: each signed in beside the others, one first and the
// rest on behind it or off, any put first, and removing one signs it out.
func TestPluginAccounts(t *testing.T) {
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
	signIn := func(team string) {
		t.Helper()
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
		if got, _ := SignInStatus(st.ID); got.State != "done" || got.User != team+"@fake" {
			t.Fatalf("signing in %s: %+v", team, got)
		}
	}
	users := func() []string {
		var out []string
		for _, l := range Logins("fakeco") {
			u := l.User
			if l.Active {
				u = "*" + u
			}
			if !l.On {
				u += " (off)"
			}
			out = append(out, u)
		}
		return out
	}
	first := func() string {
		t.Helper()
		p, err := Find("fakeco")
		if err != nil {
			t.Fatal(err)
		}
		return p.Account.User
	}
	also := func() []string {
		p, _ := Find("fakeco")
		var out []string
		for _, q := range p.AlsoOn() {
			out = append(out, q.Account.User+"@"+q.Account.pluginKey)
		}
		return out
	}

	signIn("blue")
	signIn("red")
	signIn("red") // the same account again: not listed twice
	if got := strings.Join(users(), ","); got != "*blue@fake,red@fake" {
		t.Fatalf("accounts %s", got)
	}
	if first() != "blue@fake" {
		t.Fatalf("first is %s", first())
	}
	if a := also(); len(a) != 1 || !strings.HasPrefix(a[0], "red@fake@fakeco#") {
		t.Fatalf("also on %v", a)
	}
	if err := SetLoginOn("fakeco", "red@fake", false); err != nil {
		t.Fatal(err)
	}
	if a := also(); len(a) != 0 {
		t.Fatalf("red is off but %v", a)
	}
	if err := SetLoginOn("fakeco", "blue@fake", false); err == nil {
		t.Fatal("the first account was turned off")
	}
	if err := SwitchLogin("fakeco", "red@fake"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(users(), ","); got != "*red@fake,blue@fake" || first() != "red@fake" {
		t.Fatalf("after putting red first: %s, first %s", got, first())
	}
	if a := also(); len(a) != 1 || a[0] != "blue@fake@fakeco" {
		t.Fatalf("also on %v", a)
	}
	if err := ForgetLogin("fakeco", "red@fake"); err == nil {
		t.Fatal("the first account was removed")
	}
	if err := ForgetLogin("fakeco", "blue@fake"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(users(), ","); got != "*red@fake" {
		t.Fatalf("after removing blue: %s", got)
	}
	for _, pp := range plugin.Cached() {
		if len(pp.Accounts) != 1 || !strings.HasPrefix(pp.Accounts[0].Key, "fakeco#") {
			t.Fatalf("blue is still signed in: %+v", pp.Accounts)
		}
	}

	// two keys with nothing to tell them by are told apart by their slot
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k2"); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "sk-long-key-abcd"); err != nil {
		t.Fatal(err)
	}
	us := users()
	if len(us) != 4 || us[1] == us[2] || !strings.HasPrefix(us[1], "API key") || us[3] != "API key …abcd" {
		t.Fatalf("keys listed as %v", us)
	}

	// removing the provider hides it, as a built-in's, every account kept;
	// shown again, they are all there
	if err := Delete("fakeco"); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("fakeco"); err == nil || !plugin.SignedIn("fakeco") {
		t.Fatalf("removed: %v, signed in %v", err, plugin.SignedIn("fakeco"))
	}
	if err := ShowAccount("fakeco"); err != nil {
		t.Fatal(err)
	}
	if got := users(); !slices.Equal(got, us) {
		t.Fatalf("shown again with %v, was %v", got, us)
	}
}
