package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// linkaww on Discord, a magpie in Docker added as a Remote magpie: its
// Codex card on the computer reading it told no whole for the week. The
// whole is reckoned from the calls routed through the account, which only
// the magpie holding it has; the computer reading the card leaves a card
// from another magpie alone (holdsBounds). So the cards a remote magpie is
// asked for carry each window's whole as reckoned there, and the reading
// side keeps them.
func TestRemoteMagpieCardsCarryTheirWholes(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetBalances()
	t.Cleanup(provider.ForgetBalances)
	was := provider.ReckonHolds
	t.Cleanup(func() { provider.ReckonHolds = was })
	// the reckoning itself is usage's (window_holds_test.go): here, each
	// card is marked as reckoned
	provider.ReckonHolds = func(qs []provider.SubscriptionQuota, now time.Time) []provider.SubscriptionQuota {
		out := make([]provider.SubscriptionQuota, len(qs))
		for i, q := range qs {
			q.Windows = append(append([]provider.QuotaWindow(nil), q.Windows...), provider.QuotaWindow{Name: "Weekly", Used: 2, HoldsWhy: provider.HoldsLow})
			out[i] = q
		}
		return out
	}
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"total_available":6000000}}`))
	}))
	defer vendor.Close()
	if err := provider.Save(provider.Provider{ID: "bal", Name: "Bal", Key: "sk-bal", Chat: vendor.URL + "/v1",
		BalanceURL: vendor.URL + "/b", BalancePath: "$data.total_available / 500000"}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: gw.URL}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	provider.RefreshUsage(ctx, "office", "")
	var bal *provider.SubscriptionQuota
	for _, q := range provider.RemoteCards(ctx) {
		if q.Provider == "office/bal" {
			bal = &q
		}
	}
	if bal == nil {
		t.Fatal("no office/bal card")
	}
	if n := len(bal.Windows); n == 0 || bal.Windows[n-1].HoldsWhy != provider.HoldsLow {
		t.Fatalf("office/bal's windows %+v: the remote's whole isn't on them", bal.Windows)
	}
}
