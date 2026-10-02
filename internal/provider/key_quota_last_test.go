package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// A Command Code key's plan and balance, read once, still show when
// billing/credits answers 503 for a while ("Couldn't verify your credit
// balance just now") — chalice on Discord: the key's card said only
// 暂时无法获取用量 — as a subscription's card does; a key that never
// answered has no plan card and its balance says why.
func TestKeyQuotasKeepLast(t *testing.T) {
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
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		ForgetBalances()
		forgetPlanQuotas()
	}
	restart()
	t.Cleanup(restart)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Host")+r.URL.Path != "api.commandcode.ai/alpha/billing/credits" {
			t.Errorf("asked %s%s", r.Header.Get("X-Host"), r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if down.Load() || r.Header.Get("Authorization") != "Bearer cmd-a" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"Couldn't verify your credit balance just now"}}`))
			return
		}
		w.Write([]byte(`{"credits":{"monthlyCredits":41.2,"purchasedCredits":5,"freeCredits":0},
			"windowLimits":{"limited":true,"fiveHour":{"used":3,"cap":10,"resetAt":4102444800000},"weekly":{"used":12,"cap":40,"resetAt":4102444800000}}}`))
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	for _, p := range []Provider{
		{ID: "commandcode", Name: "commandCode", Preset: "commandcode", Key: "cmd-a",
			Chat: "https://api.commandcode.ai/provider/v1", Responses: "https://api.commandcode.ai/provider/v1", Anthropic: "https://api.commandcode.ai/provider"},
		{ID: "cmd-new", Name: "Never read", Preset: "commandcode", Key: "cmd-b", Chat: "https://api.commandcode.ai/provider/v1"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (plans, bals map[string]SubscriptionQuota) {
		ForgetBalances()
		forgetPlanQuotas()
		plans, bals = map[string]SubscriptionQuota{}, map[string]SubscriptionQuota{}
		for _, q := range PlanQuotas(context.Background()) {
			plans[q.Provider] = q
		}
		for _, q := range KeyBalances(context.Background()) {
			bals[q.Provider] = q
		}
		return
	}

	plans, bals := read()
	if q := plans["commandcode"]; len(q.Windows) != 2 || q.Windows[0].Used != 30 || q.AsOf != nil {
		t.Fatalf("plan read: %+v", q)
	}
	if q := bals["commandcode"]; q.Balance != "$46.20" || q.AsOf != nil {
		t.Fatalf("balance read: %+v", q)
	}

	down.Store(true)
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false // read again from disk, as after a restart
	lastQuotas.Unlock()
	plans, bals = read()
	if q := plans["commandcode"]; q.Error != "" || q.AsOf == nil || len(q.Windows) != 2 || q.Windows[1].Used != 30 || q.Name != "commandCode" {
		t.Fatalf("plan while down: %+v", q)
	}
	if q := bals["commandcode"]; q.Error != "" || q.AsOf == nil || q.Balance != "$46.20" || q.Name != "commandCode" {
		t.Fatalf("balance while down: %+v", q)
	}
	// a key never read: no plan card, and its balance says why
	if q, ok := plans["cmd-new"]; ok {
		t.Fatalf("plan of a key never read: %+v", q)
	}
	if q := bals["cmd-new"]; q.Error == "" || q.AsOf != nil {
		t.Fatalf("balance of a key never read: %+v", q)
	}
}
