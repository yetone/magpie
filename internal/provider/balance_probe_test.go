package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

func probeSandbox(t *testing.T) {
	t.Helper()
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
	probeLocal = true
	probed.Lock()
	probed.m = nil
	probed.Unlock()
	t.Cleanup(func() {
		keyBalanceCache.data = nil
		probeLocal = false
		probed.Lock()
		probed.m = nil
		probed.Unlock()
	})
}

// A relay added by hand with no Balance URL (小鱼 of yylx.io, 11-05): its
// balance is read where a sub2api relay tells a key it, its card shows it,
// and the query is asked once, not on every read.
func TestHandAddedRelayFindsItsBalance(t *testing.T) {
	probeSandbox(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/usage" {
			asked.Add(1)
			if r.Header.Get("Authorization") != "Bearer sk-relay" {
				http.Error(w, `{"error":"invalid key"}`, http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"remaining":10472.14,"unit":"USD"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := Provider{ID: "yylx", Name: "yylx", Chat: srv.URL + "/v1", Anthropic: srv.URL, Key: "sk-relay"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	amount, ok, err := Balance(context.Background(), p)
	if err != nil || !ok || amount != "$10472.14" {
		t.Fatalf("balance = %q %v %v, want $10472.14", amount, ok, err)
	}
	if u, field := BalanceQuery(p); u != srv.URL+"/v1/usage" || field != "$remaining" {
		t.Errorf("query = %q %q", u, field)
	}
	cards := KeyBalances(context.Background())
	if len(cards) != 1 || cards[0].Provider != "yylx" || cards[0].Balance != "$10472.14" {
		t.Fatalf("cards = %+v", cards)
	}
	if n := asked.Load(); n != 3 {
		t.Errorf("/v1/usage asked %d times, want 3 (the probe once, then each read)", n)
	}
	// the user's config is left as it was
	if q, _ := Find("yylx"); q.BalanceURL != "" {
		t.Errorf("Balance URL written: %q", q.BalanceURL)
	}
}

// A new-api relay answers /v1/usage with 404: its key query is found next.
func TestHandAddedNewAPIRelay(t *testing.T) {
	probeSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/usage/token" && r.Header.Get("Authorization") == "Bearer sk-n" {
			w.Write([]byte(`{"code":true,"data":{"total_available":1000000}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := Provider{ID: "na", Name: "na", Chat: srv.URL + "/v1", Key: "sk-n"}
	amount, ok, err := Balance(context.Background(), p)
	if err != nil || !ok || amount != "$2.00" {
		t.Fatalf("balance = %q %v %v, want $2.00", amount, ok, err)
	}
}

// A relay that tells no balance at either query has no card, and is not
// asked again on the next read.
func TestHandAddedRelayWithNoBalance(t *testing.T) {
	probeSandbox(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		// a page that answers 200 to anything is not a balance
		w.Write([]byte(`<html>ok</html>`))
	}))
	defer srv.Close()
	p := Provider{ID: "none", Name: "none", Chat: srv.URL + "/v1", Key: "sk-x"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if amount, ok, err := Balance(context.Background(), p); ok || err != nil || amount != "" {
			t.Fatalf("balance = %q %v %v, want none", amount, ok, err)
		}
		if cards := KeyBalances(context.Background()); len(cards) != 0 {
			t.Fatalf("cards = %+v, want none", cards)
		}
		keyBalanceCache.data = nil
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("asked %d times, want 2 (each query once)", n)
	}
}

// What is never looked at: a provider with its own Balance URL or a
// preset, a host magpie knows, a model vendor's own API, this computer.
func TestBalanceProbeLeavesOthersAlone(t *testing.T) {
	for _, p := range []Provider{
		{Chat: "https://relay.example.com/v1", Key: "k", BalanceURL: "https://relay.example.com/x"},
		{Chat: "https://relay.example.com/v1", Key: "k", Preset: "deepseek"},
		{Chat: "https://opencode.ai/inference/custom/acme/v1", Key: "k"},
		{Chat: "https://opencode.ai/zen/go/v1", Key: "k", Preset: "opencode-go"},
		{Chat: "https://relay.example.com/v1"},
		{Chat: "https://api.deepseek.com/v1", Key: "k"},
		{Chat: "https://api.openai.com/v1", Key: "k"},
		{Anthropic: "https://api.anthropic.com", Key: "k"},
		{Chat: "http://127.0.0.1:11434/v1", Key: "k"},
		{Chat: "http://localhost:1234/v1", Key: "k"},
	} {
		if probesBalance(p) {
			t.Errorf("looked at: %+v", p)
		}
	}
	if !probesBalance(Provider{Chat: "https://app.yylx.io/v1", Key: "k"}) {
		t.Error("a relay added by hand is not looked at")
	}
	if !probesBalance(Provider{Chat: "https://app.yylx.io/v1", Key: "k", Preset: "yylx"}) {
		t.Error("a relay's preset is not looked at")
	}
	if got := strings.Join(probeOrigins(Provider{Chat: "https://R.example.com/v1", Responses: "https://r.example.com/v1", Anthropic: "https://r.example.com"}), ","); got != "https://r.example.com" {
		t.Errorf("origins = %q", got)
	}
}

// A query found is looked for again a day on: a relay that moved to other
// software is read where it now tells the balance, not at the old query.
func TestFoundBalanceLookedForAgain(t *testing.T) {
	probeSandbox(t)
	var moved atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/usage" && !moved.Load():
			w.Write([]byte(`{"remaining":5}`))
		case r.URL.Path == "/api/usage/token" && moved.Load():
			w.Write([]byte(`{"data":{"total_available":1500000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "mv", Name: "mv", Chat: srv.URL + "/v1", Key: "sk-m"}
	if amount, ok, _ := Balance(context.Background(), p); !ok || amount != "$5.00" {
		t.Fatalf("balance = %q %v, want $5.00", amount, ok)
	}
	if u, field, looked := BalanceFound(p); !looked || u != srv.URL+"/v1/usage" || field != "$remaining" {
		t.Fatalf("found = %q %q %v", u, field, looked)
	}
	moved.Store(true)
	// within the day the query found is kept
	Balance(context.Background(), p)
	if u, _, _ := BalanceFound(p); u != srv.URL+"/v1/usage" {
		t.Fatalf("looked for again within the day: %q", u)
	}
	ageProbes(t, balanceProbeHit)
	if amount, ok, _ := Balance(context.Background(), p); !ok || amount != "$3.00" {
		t.Fatalf("balance after a day = %q %v, want $3.00", amount, ok)
	}
	if u, _, _ := BalanceFound(p); u != srv.URL+"/api/usage/token" {
		t.Errorf("found = %q", u)
	}
}

// A query found that turns the key away when looked for again stays the
// query, so the card says the relay refused the key instead of going.
func TestFoundBalanceKeptWhenTheKeyIsRefused(t *testing.T) {
	probeSandbox(t)
	var refuse atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		if refuse.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid token"}}`))
			return
		}
		w.Write([]byte(`{"remaining":5}`))
	}))
	defer srv.Close()
	p := Provider{ID: "rf", Name: "rf", Chat: srv.URL + "/v1", Key: "sk-r"}
	if _, ok, _ := Balance(context.Background(), p); !ok {
		t.Fatal("not found")
	}
	refuse.Store(true)
	ageProbes(t, balanceProbeHit)
	_, ok, err := Balance(context.Background(), p)
	if !ok || err == nil {
		t.Fatalf("balance = %v %v, want the relay's refusal", ok, err)
	}
	if u, _, _ := BalanceFound(p); u != srv.URL+"/v1/usage" {
		t.Errorf("found = %q, want the query kept", u)
	}
}

// BalanceFound tells the editor nothing for a provider it never looks for.
func TestBalanceFoundOnlyForRelaysByHand(t *testing.T) {
	probeSandbox(t)
	if _, _, looked := BalanceFound(Provider{Chat: "https://relay.example.com/v1", Key: "k", BalanceURL: "https://relay.example.com/x"}); looked {
		t.Error("looked with a Balance URL")
	}
	if u, _, looked := BalanceFound(Provider{Chat: "https://relay.example.com/v1", Key: "k"}); !looked || u != "" {
		t.Errorf("found = %q %v, want looked, none yet", u, looked)
	}
}

// ageProbes moves every probe kept back by d, as if d had gone by.
func ageProbes(t *testing.T, d time.Duration) {
	t.Helper()
	probed.Lock()
	defer probed.Unlock()
	for k, f := range probed.m {
		f.until = f.until.Add(-d)
		probed.m[k] = f
	}
}

// TestHostnameOfKept: the provider list asks each provider's base URL's
// hostname on every poll; it is parsed once, then answered without
// garbage, and an unparsable one is "".
func TestHostnameOfKept(t *testing.T) {
	const raw = "https://api.example.com:8443/v1"
	if h := hostnameOf(raw); h != "api.example.com" {
		t.Fatalf("hostnameOf(%q) = %q", raw, h)
	}
	if n := testing.AllocsPerRun(100, func() { hostnameOf(raw) }); n != 0 {
		t.Errorf("a hostname parsed before made %v allocations, want 0", n)
	}
	for range 2 {
		if h := hostnameOf("http://[::1"); h != "" {
			t.Errorf("an unparsable URL's hostname = %q", h)
		}
	}
}

// hostOf shares hostnameOf's parsed URLs: the provider list asks it of
// every base URL on each poll too.
func TestHostOfKept(t *testing.T) {
	const raw = "https://API.Example.com:8443/v1"
	if h := hostOf(raw); h != "api.example.com:8443" {
		t.Fatalf("hostOf(%q) = %q", raw, h)
	}
	if n := testing.AllocsPerRun(100, func() { hostOf(raw) }); n != 0 {
		t.Errorf("a host parsed before made %v allocations, want 0", n)
	}
	if h := hostOf("http://[::1"); h != "" {
		t.Errorf("an unparsable URL's host = %q", h)
	}
}
