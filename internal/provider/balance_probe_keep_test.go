package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// usageRelay answers /v1/usage with a balance for sk-k and counts the asks.
func usageRelay(t *testing.T, asked *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path == "/v1/usage" && r.Header.Get("Authorization") == "Bearer sk-k" {
			w.Write([]byte(`{"remaining":5,"unit":"USD"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// restart is what a new run of magpie starts with: nothing in memory.
func restart() {
	probed.Lock()
	probed.m = nil
	probed.Unlock()
}

// What was found is kept on disk: after a restart the relay is not asked
// again to find it, and a miss is kept too.
func TestBalanceProbeSurvivesARestart(t *testing.T) {
	probeSandbox(t)
	var asked atomic.Int32
	srv := usageRelay(t, &asked)
	p := Provider{ID: "keep", Name: "keep", Chat: srv.URL + "/v1", Key: "sk-k"}
	if _, ok, _ := Balance(context.Background(), p); !ok {
		t.Fatal("not found")
	}
	probes := asked.Load()
	restart()
	if got := probedBalanceURL(p); got != srv.URL+"/v1/usage" {
		t.Fatalf("after a restart the query = %q, want it kept", got)
	}
	if asked.Load() != probes {
		t.Errorf("the relay was asked again by reading the kept query")
	}
	// a miss is kept the same way
	var none atomic.Int32
	dead := usageRelay(t, &none)
	q := Provider{ID: "miss", Name: "miss", Chat: dead.URL + "/v1", Key: "sk-other"}
	Balance(context.Background(), q)
	n := none.Load()
	restart()
	Balance(context.Background(), q)
	if none.Load() != n {
		t.Errorf("a miss was asked again after a restart: %d -> %d", n, none.Load())
	}
	if !balanceUndetected(q) {
		t.Error("a miss kept is undetected")
	}
}

// Detect again drops what was found, so the relay is asked anew; the
// switch turns the look off, and nothing is asked.
func TestBalanceProbeDetectAgainAndSwitch(t *testing.T) {
	probeSandbox(t)
	var asked atomic.Int32
	srv := usageRelay(t, &asked)
	p := Provider{ID: "again", Name: "again", Chat: srv.URL + "/v1", Key: "sk-k"}
	Balance(context.Background(), p)
	n := asked.Load()
	ForgetBalanceProbe(p)
	if got := probedBalanceURL(p); got != "" {
		t.Fatalf("still found after forgetting: %q", got)
	}
	restart() // and not brought back from the file
	if got := probedBalanceURL(p); got != "" {
		t.Fatalf("forgotten, but the file brought it back: %q", got)
	}
	if _, ok, _ := Balance(context.Background(), p); !ok {
		t.Fatal("not found again")
	}
	if asked.Load() <= n {
		t.Error("detect again did not ask the relay")
	}

	var off atomic.Int32
	srv2 := usageRelay(t, &off)
	q := Provider{ID: "off", Name: "off", Chat: srv2.URL + "/v1", Key: "sk-k", NoBalanceProbe: true}
	if amount, ok, err := Balance(context.Background(), q); ok || err != nil || amount != "" {
		t.Errorf("balance with the look off = %q %v %v", amount, ok, err)
	}
	if off.Load() != 0 {
		t.Errorf("the look is off, yet the relay was asked %d times", off.Load())
	}
	if eligible, _ := BalanceProbeState(q); !eligible {
		t.Error("the editor must still offer to turn it on")
	}
}

// A balance URL of the user's wins over what was found, and the key goes
// to the base URL's origin only, never where a redirect leads.
func TestBalanceProbeUserURLWinsAndKeyStaysHome(t *testing.T) {
	probeSandbox(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			elsewhere.Add(1)
		}
		w.Write([]byte(`{"remaining":99}`))
	}))
	defer other.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mine" {
			w.Write([]byte(`{"balance":7}`))
			return
		}
		http.Redirect(w, r, other.URL+"/v1/usage", http.StatusFound)
	}))
	defer home.Close()
	p := Provider{ID: "home", Name: "home", Chat: home.URL + "/v1", Key: "sk-k"}
	if _, ok, _ := Balance(context.Background(), p); ok {
		t.Error("a redirect to another origin gave a balance")
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the key reached another origin %d times", elsewhere.Load())
	}
	ForgetBalanceProbe(p)
	// found at /v1/usage first, then the user names their own
	var asked atomic.Int32
	srv := usageRelay(t, &asked)
	q := Provider{ID: "own", Name: "own", Chat: srv.URL + "/v1", Key: "sk-k"}
	Balance(context.Background(), q)
	q.BalanceURL, q.BalancePath = home.URL+"/mine", "$balance"
	if u, _ := BalanceQuery(q); u != home.URL+"/mine" {
		t.Errorf("query = %q, want the user's", u)
	}
	if amount, ok, _ := Balance(context.Background(), q); !ok || amount != "$7.00" {
		t.Errorf("balance = %q %v, want the user's $7.00", amount, ok)
	}
}
