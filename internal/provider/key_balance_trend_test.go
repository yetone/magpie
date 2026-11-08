package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// A key's balance card carries its trend: the Usage page's "Balance over
// time" is drawn from it. f8525323d noted the history on a copy of the
// cards, so every key's card went out with none (nf1qaz2w on Discord).
func TestKeyBalancesCarryTheirTrend(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"total_available":1000000}}`))
	}))
	defer srv.Close()
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-one", KeyName: "main",
		BalanceURL: srv.URL + "/api/usage/token", BalancePath: "$data.total_available / 500000"}); err != nil {
		t.Fatal(err)
	}
	// read an hour ago at $3
	before := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	writeBalanceHist(balanceHist{quotaHistKey("relay", ""): {{At: before, Amount: 3}}})
	got := KeyBalances(context.Background())
	if len(got) != 1 || got[0].Balance != "$2.00" {
		t.Fatalf("cards %+v", got)
	}
	tr := got[0].BalanceTrend
	if tr == nil || len(tr.Points) != 2 || tr.Points[0].Amount != 3 || tr.Points[1].Amount != 2 {
		t.Fatalf("the key's card has trend %+v, want $3 an hour ago then $2", tr)
	}
}
