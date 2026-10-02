package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// A balance field with several amounts, or a percent, comes to the card
// each amount apart, its label apart from its figure and a percent's
// share for a meter (#420); one amount alone is the figure as it was.
func TestBalanceParts(t *testing.T) {
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
		w.Write([]byte(`{"data":{"total_available":1000000,"today":2.5,"used":45,"granted":50}}`))
	}))
	defer srv.Close()
	for _, p := range []Provider{
		{ID: "several", Name: "Several", Key: "sk-1", BalanceURL: srv.URL + "/b", BalancePath: "Balance: $data.total_available / 500000; Today: $data.today; Used: data.used / data.granted %"},
		{ID: "share", Name: "Share", Key: "sk-2", BalanceURL: srv.URL + "/b", BalancePath: "data.used / data.granted %"},
		{ID: "one", Name: "One", Key: "sk-3", BalanceURL: srv.URL + "/b", BalancePath: "$data.total_available / 500000"},
	} {
		p.Chat = srv.URL + "/v1"
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range KeyBalances(context.Background()) {
		got[q.Provider] = q
	}
	parts := func(id string) string {
		b, _ := json.Marshal(got[id].BalanceParts)
		return string(b)
	}
	if q := got["several"]; q.Balance != "Balance $2.00 · Today $2.50 · Used 90%" {
		t.Errorf("several: the line as it was, got %q", q.Balance)
	}
	if want := `[{"label":"Balance","text":"$2.00"},{"label":"Today","text":"$2.50"},{"label":"Used","text":"90%","percent":90}]`; parts("several") != want {
		t.Errorf("several: %s\nwant %s", parts("several"), want)
	}
	if want := `[{"text":"90%","percent":90}]`; parts("share") != want {
		t.Errorf("share: %s\nwant %s", parts("share"), want)
	}
	if q := got["one"]; q.Balance != "$2.00" || q.BalanceParts != nil {
		t.Errorf("one: %q %s, want the figure alone", q.Balance, parts("one"))
	}
	// when each was read, for the card to say, which the minute the
	// balances are kept for leaves as it was
	for id, q := range got {
		if q.ReadAt == nil || time.Since(*q.ReadAt) > time.Minute {
			t.Errorf("%s: read at %v", id, q.ReadAt)
		}
	}
	first := *got["one"].ReadAt
	for _, q := range KeyBalances(context.Background()) {
		if q.Provider == "one" && !q.ReadAt.Equal(first) {
			t.Errorf("one: kept, it says read at %v, not %v", q.ReadAt, first)
		}
	}
}
