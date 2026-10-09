package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A remote magpie's quotas (莫 on Discord), between a gateway and a
// magpie that has it as its provider: the cards it is shown are the
// gateway's as last read, and asking for them asks no vendor, however
// often; a card's refresh reads it there once, and again no sooner than
// remoteRefreshGap; a card the gateway has from a third magpie isn't read
// for it; another machine without the gateway's key gets nothing.
func TestRemoteMagpieQuotas(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetBalances()
	t.Cleanup(provider.ForgetBalances)
	var reads atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
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

	// nothing read on the gateway yet: nothing is read for the remote
	cs := provider.RemoteCards(ctx)
	if len(cs) != 1 || cs[0].Provider != "office" || !strings.HasPrefix(cs[0].Error, "nothing read") {
		t.Fatalf("before any read: %+v", cs)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("the remote's ask read the vendor %d times", n)
	}
	// its card's refresh has the gateway read every card
	provider.RefreshUsage(ctx, "office", "")
	if n := reads.Load(); n != 1 {
		t.Fatalf("refresh read the vendor %d times, want 1", n)
	}
	var bal *provider.SubscriptionQuota
	for _, q := range provider.RemoteCards(ctx) {
		if q.Provider == "office/bal" {
			bal = &q
		}
	}
	if bal == nil || bal.Balance != "$12.00" || bal.Name != "Bal · Office" || bal.Kind != "balance" {
		t.Fatalf("office/bal: %+v", bal)
	}
	// asked again and again, the gateway answers from what it has
	for range 5 {
		r, _ := http.NewRequest("GET", gw.URL+provider.RemoteCardsPath, nil)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("asking for the cards read the vendor: %d", n)
	}
	// the card's own refresh, pressed twice at once: read once
	provider.RefreshUsage(ctx, "office/bal", "")
	provider.RefreshUsage(ctx, "office/bal", "")
	if n := reads.Load(); n != 2 {
		t.Fatalf("two refreshes read the vendor %d times in all, want 2", n)
	}
	post := func(from, q, key string) int {
		r := httptest.NewRequest("POST", provider.RemoteRefreshPath+q, nil)
		r.RemoteAddr = from
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		return w.Code
	}
	if c := post("127.0.0.1:5000", "?provider=home/codex", ""); c != http.StatusBadRequest {
		t.Errorf("a third magpie's card: %d", c)
	}
	if c := post("127.0.0.1:5000", "?provider=office", ""); c != http.StatusBadRequest {
		t.Errorf("a remote magpie's own card: %d", c)
	}
	if c := post("192.168.1.9:5000", "?provider=bal", ""); c == http.StatusOK {
		t.Error("another machine without a key had a card read")
	}
	for _, path := range []string{provider.RemoteCardsPath} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.168.1.9:5000"
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		if w.Code == http.StatusOK {
			t.Errorf("another machine without a key got %s", path)
		}
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("refused requests read the vendor: %d", n)
	}
}

// #1313 (jorben): a magpie in a container serves only other magpies, so
// nobody opens its Usage page, while its Codex account's usage is read
// behind the requests (the account switching, CodexUsedUp). A GUI on
// another computer with that magpie as its provider is shown the
// reading on the account's card, through the gateway, and asking for the
// cards asks ChatGPT nothing.
func TestRemoteMagpieShowsBackgroundReadings(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	home := os.Getenv("HOME")
	jwt := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	// an account no other test reads, so no reading of it is cached
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      jwt(map[string]any{"email": "jorben-1313@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1313"}}),
		"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r", "account_id": "acct-1313"}}), 0o600)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	var asked atomic.Int32
	chatgpt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asked.Add(1)
		w.Write([]byte(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,
			"primary_window":{"used_percent":37,"limit_window_seconds":18000,"reset_after_seconds":3600},
			"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_after_seconds":86400}}}`))
	}))
	defer chatgpt.Close()
	was := provider.CodexBase
	provider.CodexBase = chatgpt.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: gw.URL}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// read behind the requests, as the account switching reads it
	if provider.CodexUsedUp(ctx) {
		t.Fatal("held at 37%")
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("the background read asked ChatGPT %d times", n)
	}
	var codex *provider.SubscriptionQuota
	cs := provider.RemoteCards(ctx)
	for i, q := range cs {
		if q.Provider == "office/codex" && q.User == "jorben-1313@example.com" {
			codex = &cs[i]
		}
	}
	if codex == nil || len(codex.Windows) == 0 || codex.Windows[0].Used != 37 || codex.Error != "" || codex.Kind != "subscription" {
		t.Fatalf("the remote's Codex card: %+v (all: %+v)", codex, cs)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("asking for the cards asked ChatGPT: %d", n)
	}
}
