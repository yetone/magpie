package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A prepaid wallet (SuperMagic's points, #3950) tells how much is left in
// its unit and nothing it is a share of. Its window keeps that figure,
// is no text row and no share, and routing knows the account by it with
// nothing used invented; an empty wallet is used up. Through the decode
// a plugin's reply takes, as host.js hands it on.
func TestPluginWalletBalanceIsAReading(t *testing.T) {
	var u plugin.Usage
	if err := json.Unmarshal([]byte(`{"windows":[{"name":"积分","balance":6291,"unit":"积分"}]}`), &u); err != nil {
		t.Fatal(err)
	}
	q := quotaOfPlugin(SubscriptionQuota{Provider: "magicrew-plugin", User: "u"}, u)
	w := q.Windows[0]
	if w.Balance == nil || *w.Balance != 6291 || w.Unit != "积分" {
		t.Fatalf("the balance: %+v", w)
	}
	if w.Text || w.Aside || w.Used != 0 {
		t.Errorf("a wallet is a reading with no share invented: Text %v, Aside %v, Used %v", w.Text, w.Aside, w.Used)
	}
	now := time.Now()
	a, ok := routedAllowance("plugin:magicrew", "u", q, now)
	if !ok {
		t.Fatal("a wallet's account is not known to routing")
	}
	if used, _ := a.For("", now); used != 0 {
		t.Errorf("used invented: %v", used)
	}
	if left, unit, ok := a.Left(""); !ok || left != 6291 || unit != "积分" {
		t.Errorf("Left: %v %q %v", left, unit, ok)
	}

	var empty plugin.Usage
	if err := json.Unmarshal([]byte(`{"windows":[{"name":"积分","balance":0,"unit":"积分"}]}`), &empty); err != nil {
		t.Fatal(err)
	}
	e, ok := routedAllowance("plugin:magicrew", "v", quotaOfPlugin(SubscriptionQuota{Provider: "magicrew-plugin", User: "v"}, empty), now)
	if !ok {
		t.Fatal("an empty wallet is not known")
	}
	if used, _ := e.For("", now); used < SpentShare {
		t.Errorf("an empty wallet is not used up: %v", used)
	}
	if left, _, ok := e.Left(""); !ok || left != 0 {
		t.Errorf("an empty wallet's Left: %v %v", left, ok)
	}
}

// Left is the least balance of the windows that count the model, and
// none when no window told one.
func TestAllowanceLeftIsTheLeastBalance(t *testing.T) {
	b := func(f float64) *float64 { return &f }
	now := time.Now()
	a := allowanceOf([]QuotaWindow{
		{Name: "Wallet", Balance: b(800), Unit: "pts"},
		{Name: "Opus wallet", Balance: b(30), Unit: "pts", Model: "opus"},
		{Name: "Daily", Used: 20, ResetSecs: 3600},
	}, now)
	if left, _, _ := a.Left("sonnet"); left != 800 {
		t.Errorf("sonnet's: %v", left)
	}
	if left, _, _ := a.Left("opus"); left != 30 {
		t.Errorf("opus's: %v", left)
	}
	if _, _, ok := allowanceOf([]QuotaWindow{{Name: "Daily", Used: 20}}, now).Left(""); ok {
		t.Error("no balance told, yet one is left")
	}
}

// Through the plugin host: a balance the plugin sends reaches magpie as
// that figure; one that isn't a number is not said.
func TestPluginHostPassesABalanceOn(t *testing.T) {
	windows := filepath.Join(t.TempDir(), "windows.json")
	if err := os.WriteFile(windows, []byte(`{
		"a@fake": [{"name": "积分", "balance": 6291, "unit": "积分"}],
		"full@fake": [{"name": "积分", "balance": "lots", "unit": "积分", "display": "lots"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	u := fakePluginUsage(t, windows)
	if w := u["a@fake"].Windows; len(w) != 1 || w[0].Balance == nil || *w[0].Balance != 6291 || w[0].Text {
		t.Errorf("a balance: %+v", w)
	}
	if w := u["full@fake"].Windows; len(w) != 1 || w[0].Balance != nil || !w[0].Text {
		t.Errorf("not a number: %+v", w)
	}
}

// A window used up that says credits (a Kiro month spent with a trial
// left) reaches magpie as one routing counts, the account on its credits:
// not set aside, though the plugin sends aside too for a magpie before
// this. One without credits set aside stays aside.
func TestPluginHostPassesCreditsOn(t *testing.T) {
	windows := filepath.Join(t.TempDir(), "windows.json")
	if err := os.WriteFile(windows, []byte(`{
		"a@fake": [{"name": "Credits", "used": 100, "amount": 916, "limit": 916, "aside": true, "credits": true}, {"name": "Trial credits", "used": 80, "amount": 400, "limit": 500, "aside": true}],
		"full@fake": [{"name": "Credits", "used": 100, "amount": 916, "limit": 916, "aside": true}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	u := fakePluginUsage(t, windows)
	now := time.Now()
	w := u["a@fake"].Windows
	if len(w) != 2 || !w[0].Credits || w[0].Aside || w[1].Credits || !w[1].Aside {
		t.Fatalf("credits: %+v", w)
	}
	if a, ok := routedAllowance("plugin:fakeco", "a@fake", u["a@fake"], now); !ok || !a.OnCredits() {
		t.Errorf("the account is not on its credits: %+v", a)
	}
	if w := u["full@fake"].Windows; len(w) != 1 || w[0].Credits || !w[0].Aside {
		t.Errorf("aside alone: %+v", w)
	}
}

// fakePluginUsage signs in the fake plugin's a@fake and full@fake with
// the windows file it reads (FAKE_WINDOWS) and reads their usage.
func fakePluginUsage(t *testing.T, windows string) map[string]SubscriptionQuota {
	t.Helper()
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_WINDOWS", windows)
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
	for _, team := range []string{"a", "full"} {
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	return LoginUsage(ctx, "plugin:fakeco")
}
