package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// xuanwo on X: reading agy's usage puts the account at risk, several
// developers said. magpie asks the agy-cli plugin for no account's usage:
// not for an account row, a usage card or routing. Switch automatically,
// which the user turns on, is the one reading left (usageWithheld).
func TestAgyUsageNotAsked(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", agyCLIID)
	var asked atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte("Antigravity Pro"))
	}))
	defer vendor.Close()
	t.Setenv("FAKE_USAGE", vendor.URL)
	t.Cleanup(plugin.Settle)
	forget := func() {
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	forget()
	t.Cleanup(forget)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	const user = "agy@fake"
	k, err := plugin.Import(ctx, agyCLIID, map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": user})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:" + agyCLIID, User: user, Home: k, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if pp, ok := pluginOfAgent(agyCLIID); !ok || !pp.Usage || !pp.SignedIn {
		t.Fatalf("the agy-cli plugin: %+v, %v", pp, ok)
	}

	for _, agent := range []string{agyCLIID, "plugin:" + agyCLIID} {
		if u := LoginUsage(ctx, agent); len(u) != 0 {
			t.Errorf("%s: usage read %+v", agent, u)
		}
	}
	for _, q := range fetchSubscriptionUsage(ctx) {
		if q.Provider == agyCLIID {
			t.Errorf("a usage card for agy-cli: %+v", q)
		}
	}
	if q := loginReading(ctx, Login{Agent: "plugin:" + agyCLIID, User: user}).read; q.Error != errUsageWithheld {
		t.Errorf("a reading asked for anyway: %+v", q)
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("agy's usage was asked %d times", n)
	}

	// Switch automatically's own reading, of the account agy is on
	forget()
	q, ok := someLoginUsage(agySwitchReading(ctx), agyCLIID, user)[user]
	if !ok || q.Error != "" || q.Plan != "Antigravity Pro" || asked.Load() != 1 {
		t.Fatalf("Switch automatically's reading: %+v, %v; asked %d", q, ok, asked.Load())
	}
	// and it is its own: the account row still reads nothing
	if u := LoginUsage(ctx, agyCLIID); len(u) != 0 || asked.Load() != 1 {
		t.Fatalf("after the switch's reading: %+v; asked %d", u, asked.Load())
	}
}
