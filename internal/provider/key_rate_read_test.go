package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sub2api's own replies to GET /v1/sub2api/billing, as its handler writes
// them (backend/internal/handler/gateway_key_billing.go, schema 1): a key
// on a group of its own, one the user has a rate of their own on, with a
// peak window.
const (
	sub2APIBillingGroup = `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.08,"resolved_rate_multiplier":0.08,"peak_rate_enabled":false,"effective_rate_multiplier":0.08,"observed_at":"2026-11-20T03:00:00Z"}`
	sub2APIBillingUser  = `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":1.2,"user_rate_multiplier":0.8,"resolved_rate_multiplier":0.8,"peak_rate_enabled":true,"peak_start":"09:00","peak_end":"18:00","peak_rate_multiplier":1.5,"applied_peak_multiplier":1,"effective_rate_multiplier":0.8,"timezone":"Asia/Shanghai","observed_at":"2026-11-20T03:00:00Z"}`
)

func TestParseKeyBilling(t *testing.T) {
	for in, want := range map[string]float64{
		sub2APIBillingGroup: 0.08,
		sub2APIBillingUser:  0.8,
		`{"object":"sub2api.key_billing","group_rate_multiplier":2}`:                              2,
		`{"object":"sub2api.key_billing","group_rate_multiplier":0,"resolved_rate_multiplier":0}`: 0,
	} {
		got, err := parseKeyBilling([]byte(in))
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{
		``, `<html>`, `{}`, `{"error":{"code":"not_found"}}`,
		`{"object":"balance","resolved_rate_multiplier":1}`,
		`{"object":"sub2api.key_billing"}`,
		`{"object":"sub2api.key_billing","resolved_rate_multiplier":-1}`,
	} {
		if r, err := parseKeyBilling([]byte(in)); err == nil {
			t.Errorf("%q read as %v", in, r)
		}
	}
}

// relayKeys is a fake sub2api relay: each key's reply by its value, and
// the keys it was asked with.
type relayKeys struct {
	sync.Mutex
	reply map[string]func(w http.ResponseWriter)
	asked []string
}

func (f *relayKeys) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != sub2APIKeyBilling {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.Lock()
		f.asked = append(f.asked, key)
		do := f.reply[key]
		f.Unlock()
		if do == nil {
			http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
			return
		}
		do(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *relayKeys) set(key string, do func(w http.ResponseWriter)) {
	f.Lock()
	defer f.Unlock()
	f.reply[key] = do
}

func (f *relayKeys) count() int {
	f.Lock()
	defer f.Unlock()
	return len(f.asked)
}

func billing(rate string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":` + rate + `,"resolved_rate_multiplier":` + rate + `,"peak_rate_enabled":false,"effective_rate_multiplier":` + rate + `,"observed_at":"2026-11-20T03:00:00Z"}`))
	}
}

// keyRateHome is a sandbox home with the models.dev cache of priceCatalog
// and a relay on loopback that may be asked.
func keyRateHome(t *testing.T) {
	t.Helper()
	priceHome(t)
	priceCatalog(t)
	probeLocal = true
	t.Cleanup(func() { probeLocal = false })
}

// @YIXIE233 on X: a sub2api relay reports each key's group rate, and the
// user says once what a relay dollar cost (¥7 for $100 is 0.07). Each key
// then bills at group rate × top-up rate, in the cost of a call and in
// routing by price; a key the user gave a rate of their own keeps it.
func TestKeyGroupRateTimesTopUpRate(t *testing.T) {
	keyRateHome(t)
	f := &relayKeys{reply: map[string]func(w http.ResponseWriter){"sk-cheap": billing("1"), "sk-pro": billing("2.5"), "sk-own": billing("4")}}
	srv := f.serve(t)
	own := keyID("sk-own")
	if err := Save(Provider{ID: "openai", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-cheap", Models: []string{"sol"}, PriceRate: 0.9, TopUpRate: 0.07,
		Keys:          []KeyAccount{{Name: "Pro", Key: "sk-pro"}, {Name: "Own", Key: "sk-own"}},
		KeyPriceRates: map[string]float64{own: 0.3}}); err != nil {
		t.Fatal(err)
	}
	readKeyRatesDue(t.Context())
	if n := f.count(); n != 3 {
		t.Fatalf("asked %d times, want each key once", n)
	}
	s := settings.Load()
	for _, c := range []struct {
		key  string
		want float64
	}{
		{keyID("sk-cheap"), 2 * 0.07}, // $2 list × 1 × 0.07
		{keyID("sk-pro"), 2 * 0.175},  // × 2.5 × 0.07
		{own, 2 * 0.3},                // the user's own rate stands
		{"", 2 * 0.9},                 // a call whose key isn't known: the provider's
		{"0000000000", 2 * 0.9},
	} {
		pr, ok, _ := BaseKeyPriceIn(s, "openai", c.key, "sol")
		if !ok || !near(pr.Input, c.want) {
			t.Errorf("key %q: $%v in, want $%v (%v)", c.key, pr.Input, c.want, ok)
		}
	}
	p, _ := Find("openai")
	q := *p
	q.Key = "sk-pro"
	if pr, ok := q.PriceAt(s, "sol", time.Time{}); !ok || !near(pr.Input, 0.35) {
		t.Fatalf("PriceAt with Pro: %+v %v", pr, ok)
	}
	if r, own := p.KeyPriceRate(keyID("sk-pro")); own || !near(r, 0.175) {
		t.Fatalf("KeyPriceRate: %v %v", r, own)
	}
	// not due again: nothing is asked a second time
	readKeyRatesDue(t.Context())
	if n := f.count(); n != 3 {
		t.Fatalf("asked again before it was due: %d", n)
	}
	// the reading is kept in the file, read by a magpie started after
	b, err := os.ReadFile(keyRatesPath())
	if err != nil || !strings.Contains(string(b), `"rate":2.5`) || strings.Contains(string(b), "sk-pro") {
		t.Fatalf("key-rates.json: %s %v", b, err)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

// A read that fails is unknown, never 1×: a key never read bills at the
// provider's PriceRate, and one read before keeps that reading while the
// relay is down, its error kept with the key masked.
func TestKeyGroupRateFailedReadIsUnknown(t *testing.T) {
	keyRateHome(t)
	f := &relayKeys{reply: map[string]func(w http.ResponseWriter){
		"sk-down": func(w http.ResponseWriter) { http.Error(w, "unavailable", http.StatusInternalServerError) },
		"sk-good": billing("2"),
	}}
	srv := f.serve(t)
	if err := Save(Provider{ID: "openai", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-down", Models: []string{"sol"}, PriceRate: 0.5, TopUpRate: 0.1,
		Keys: []KeyAccount{{Name: "Good", Key: "sk-good"}, {Name: "Gone", Key: "sk-gone"}}}); err != nil {
		t.Fatal(err)
	}
	readKeyRatesDue(t.Context())
	s := settings.Load()
	price := func(k string) float64 {
		t.Helper()
		pr, ok, _ := BaseKeyPriceIn(s, "openai", keyID(k), "sol")
		if !ok {
			t.Fatalf("%s: no price", k)
		}
		return pr.Input
	}
	if got := price("sk-down"); !near(got, 1) {
		t.Fatalf("a 500: $%v, want the provider's $1", got)
	}
	if got := price("sk-gone"); !near(got, 1) {
		t.Fatalf("a 401: $%v, want the provider's $1", got)
	}
	if got := price("sk-good"); !near(got, 0.4) {
		t.Fatalf("read: $%v, want $0.4", got)
	}
	// the relay goes down; the rate read before stands, tried again soon
	f.set("sk-good", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) })
	keyRates.Lock()
	m := keyRatesLocked()
	r := m[keyRateKey("openai", keyID("sk-good"))]
	r.At = time.Now().Add(-keyRateEvery - time.Minute)
	m[keyRateKey("openai", keyID("sk-good"))] = r
	keyRates.Unlock()
	readKeyRatesDue(t.Context())
	if got := price("sk-good"); !near(got, 0.4) {
		t.Fatalf("a failed reread lost the rate: $%v", got)
	}
	keyRates.Lock()
	r = keyRatesLocked()[keyRateKey("openai", keyID("sk-good"))]
	keyRates.Unlock()
	if r.Err == "" || strings.Contains(r.Err, "sk-good") {
		t.Fatalf("its error: %q", r.Err)
	}
}

// A group billed at 0 (a free group) is free, not the provider's rate.
func TestKeyGroupRateZero(t *testing.T) {
	keyRateHome(t)
	f := &relayKeys{reply: map[string]func(w http.ResponseWriter){"sk-free": billing("0")}}
	srv := f.serve(t)
	if err := Save(Provider{ID: "openai", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-free", Models: []string{"sol"}, PriceRate: 0.5, TopUpRate: 0.07}); err != nil {
		t.Fatal(err)
	}
	readKeyRatesDue(t.Context())
	pr, ok, _ := BaseKeyPriceIn(settings.Load(), "openai", keyID("sk-free"), "sol")
	if !ok || pr.Input != 0 {
		t.Fatalf("a free group: %+v %v", pr, ok)
	}
	p, _ := Find("openai")
	if pr, ok := p.PriceAt(settings.Load(), "sol", time.Time{}); !ok || pr.Input != 0 {
		t.Fatalf("PriceAt: %+v %v", pr, ok)
	}
}

// Nothing is read for a provider without a top-up rate, and a relay on
// this computer is never asked.
func TestKeyGroupRateOnlyWhenAsked(t *testing.T) {
	keyRateHome(t)
	f := &relayKeys{reply: map[string]func(w http.ResponseWriter){"sk-a": billing("3")}}
	srv := f.serve(t)
	if err := Save(Provider{ID: "openai", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a", Models: []string{"sol"}, PriceRate: 0.5}); err != nil {
		t.Fatal(err)
	}
	readKeyRatesDue(t.Context())
	if n := f.count(); n != 0 {
		t.Fatalf("no top-up rate, asked %d times", n)
	}
	p, _ := Find("openai")
	p.TopUpRate = 0.07
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	probeLocal = false
	readKeyRatesDue(t.Context())
	if n := f.count(); n != 0 {
		t.Fatalf("a relay on loopback asked %d times", n)
	}
	if pr, ok, _ := BaseKeyPriceIn(settings.Load(), "openai", keyID("sk-a"), "sol"); !ok || !near(pr.Input, 1) {
		t.Fatalf("not read: %+v %v", pr, ok)
	}
}

func TestTopUpRateOK(t *testing.T) {
	for _, r := range []float64{0, 0.07, 0.0714, 1, 1000} {
		if e := TopUpRateOK(r); e != "" {
			t.Errorf("%v: %s", r, e)
		}
	}
	for _, r := range []float64{-0.1, 1000.5, 0.07142} {
		if TopUpRateOK(r) == "" {
			t.Errorf("%v taken", r)
		}
	}
	// a rate kept wrong in the file is dropped on Save
	priceHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-a", TopUpRate: 0.07142}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); p.TopUpRate != 0 {
		t.Fatalf("kept %v", p.TopUpRate)
	}
}

// KeepKeyRatesRead ends with its context.
func TestKeepKeyRatesReadEnds(t *testing.T) {
	priceHome(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { KeepKeyRatesRead(ctx); close(done) }()
	ReadKeyRatesSoon()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("still running")
	}
}
