package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newAPIAccounts is a new-api relay of two accounts, answering as
// v0.9's controller/user.go GetSelf and controller/token.go
// GetTokenUsage do: /api/user/self to an account's access token with its
// user id in New-Api-User, /api/usage/token to a key alone. Account 1
// (token tok-a) has $3 and the keys sk-a1, sk-a2; account 2 (tok-b) has
// $5 and sk-b1.
func newAPIAccounts(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+auth+" "+r.Header.Get("New-Api-User"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			switch auth + "/" + r.Header.Get("New-Api-User") {
			case "tok-a/1":
				w.Write([]byte(`{"success":true,"data":{"id":1,"quota":1500000,"used_quota":10}}`))
			case "tok-b/2":
				w.Write([]byte(`{"success":true,"data":{"id":2,"quota":2500000,"used_quota":10}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"success":false,"message":"无权进行此操作，New-Api-User 与登录用户不匹配"}`))
			}
		case "/api/usage/token":
			switch auth {
			case "Bearer sk-a1", "Bearer sk-a2":
				w.Write([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","total_granted":600000,"total_used":100000,"total_available":500000,"unlimited_quota":false}}`))
			case "Bearer sk-b1":
				w.Write([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","total_granted":1000000,"total_used":0,"total_available":1000000,"unlimited_quota":false}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"success":false,"message":"无效的令牌"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

func balanceLines() string {
	var lines []string
	for _, q := range KeyBalances(context.Background()) {
		lines = append(lines, q.Provider+"|"+q.User+"|"+q.Balance+"|"+q.Error)
	}
	return strings.Join(lines, "\n")
}

// Kylin_Cloud on X: one New API relay, keys of two accounts in one
// provider, and both showed one account's balance, the provider's access
// token and user id being that account's. The key of the other account,
// given its own, shows its own account's balance; the two keys of the
// first account still show theirs once.
func TestKeysOfTwoAccountsEachShowTheirBalance(t *testing.T) {
	probeSandbox(t)
	srv, asked := newAPIAccounts(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a1", KeyName: "A1",
		Keys:       []KeyAccount{{Name: "A2", Key: "sk-a2"}, {Name: "B1", Key: "sk-b1"}},
		Headers:    map[string]string{"New-Api-User": "1"},
		BalanceURL: srv.URL + "/api/user/self", BalanceToken: "tok-a"}); err != nil {
		t.Fatal(err)
	}
	// as it was: one card, the provider's account's, asked once
	if got := balanceLines(); got != "relay||$3.00|" {
		t.Fatalf("before: %q", got)
	}
	asked()
	if err := SetKeyBalanceAuth("relay", "B1", " tok-b ", " 2 ", false); err != nil {
		t.Fatal(err)
	}
	if got, want := balanceLines(), "relay|A1|$3.00|\nrelay|B1|$5.00|"; got != want {
		t.Fatalf("balances:\n%s\nwant\n%s", got, want)
	}
	// the first account once, for A1 and A2; the second with its own
	// user id, never the provider's
	got := asked()
	if len(got) != 2 || !contains(got, "/api/user/self tok-a 1") || !contains(got, "/api/user/self tok-b 2") {
		t.Fatalf("asked %q", got)
	}
	// the row tells the key has its own, never the token
	p, _ := Find("relay")
	for _, k := range p.KeyList() {
		if (k.BalanceAuth != nil) != (k.Name == "B1") {
			t.Fatalf("%s's balance auth = %+v", k.Name, k.BalanceAuth)
		}
		if k.BalanceAuth != nil && (k.BalanceAuth.User != "2" || !k.BalanceAuth.Token) {
			t.Fatalf("B1's balance auth = %+v", k.BalanceAuth)
		}
	}
	// the user id alone changed, the token kept
	if err := SetKeyBalanceAuth("relay", "B1", "", "3", false); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); p.KeyBalanceAuth[KeyID("sk-b1")] != (KeyBalanceAuth{Token: "tok-b", User: "3"}) {
		t.Fatalf("kept %+v", p.KeyBalanceAuth)
	}
	if err := SetKeyBalanceAuth("relay", "B1", "", "abc", false); err == nil {
		t.Fatal("a user id of letters was taken")
	}
	if err := SetKeyBalanceAuth("relay", "A2", "", "1", false); err == nil {
		t.Fatal("a user id with no token was taken")
	}
	// both keys of the second account under one token: one card for them
	if err := SetKeyBalanceAuth("relay", "B1", "", "2", false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyBalanceAuth("relay", "A2", "tok-b", "2", false); err != nil {
		t.Fatal(err)
	}
	if got, want := balanceLines(), "relay|A1|$3.00|\nrelay|A2|$5.00|"; got != want {
		t.Fatalf("balances:\n%s\nwant\n%s", got, want)
	}
	// taken off, the key is asked as the provider says: one card again
	for _, k := range []string{"A2", "B1"} {
		if err := SetKeyBalanceAuth("relay", k, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	if got := balanceLines(); got != "relay||$3.00|" {
		t.Fatalf("cleared: %q", got)
	}
	if p, _ := Find("relay"); p.KeyBalanceAuth != nil {
		t.Fatalf("left %+v", p.KeyBalanceAuth)
	}
}

// A provider asking each key's own quota (no token of its own) asks a key
// given its account's token at the panel's account query, which takes
// the token, where the key query would only refuse it; the other keys
// keep their key query. The same for a relay added by hand, whose key
// query magpie found itself.
func TestKeyTokenAsksTheAccountQuery(t *testing.T) {
	for _, named := range []bool{true, false} {
		t.Run(map[bool]string{true: "named", false: "found"}[named], func(t *testing.T) {
			probeSandbox(t)
			srv, asked := newAPIAccounts(t)
			p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a1", KeyName: "A1",
				Keys:           []KeyAccount{{Name: "B1", Key: "sk-b1"}},
				KeyBalanceAuth: map[string]KeyBalanceAuth{KeyID("sk-b1"): {Token: "tok-b", User: "2"}}}
			if named {
				p.BalanceURL, p.BalancePath = srv.URL+"/api/usage/token", "$data.total_available / 500000"
			}
			if err := Save(p); err != nil {
				t.Fatal(err)
			}
			if got, want := balanceLines(), "relay|A1|$1.00|\nrelay|B1|$5.00|"; got != want {
				t.Fatalf("balances:\n%s\nwant\n%s", got, want)
			}
			for _, a := range asked() {
				if strings.HasPrefix(a, "/api/usage/token tok-b") || strings.HasPrefix(a, "/api/user/self Bearer") {
					t.Fatalf("a key and a token crossed: %q", a)
				}
			}
		})
	}
}

// An unlimited new-api key has no quota of its own: new-api counts its
// remain_quota down past 0 and never holds it there (model/token.go), so
// its total_available is no balance. The account's is read in its place
// when the relay tells the key it (the key's "Allow querying user quota",
// data.user_quota), as the account's; else the card says why there is
// none and what to do, never "$0.00" or less.
func TestUnlimitedNewAPIKey(t *testing.T) {
	probeSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer sk-told":
			w.Write([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","total_granted":-300000,"total_used":300000,"total_available":-300000,"unlimited_quota":true,"user_quota":{"total_available":4000000,"total_used":300000}}}`))
		case "Bearer sk-kept":
			w.Write([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","total_granted":-300000,"total_used":300000,"total_available":-300000,"unlimited_quota":true}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-told", BalanceURL: srv.URL + "/api/usage/token"}
	amount, _, _, _, wide, ok, err := balanceRead(context.Background(), p)
	if err != nil || !ok || amount != "$8.00" || !wide {
		t.Fatalf("told: %q wide=%v %v %v", amount, wide, ok, err)
	}
	p.Key = "sk-kept"
	if amount, _, _, _, _, _, err := balanceRead(context.Background(), p); err == nil || !strings.Contains(err.Error(), "Allow querying user quota") {
		t.Fatalf("kept: %q %v", amount, err)
	}
	// a field the user wrote is read as they wrote it
	p.BalancePath = "$data.total_used / 500000"
	if amount, _, err := Balance(context.Background(), p); err != nil || amount != "$0.60" {
		t.Fatalf("written field: %q %v", amount, err)
	}
}

// A key's own token goes where its key goes: to its new value when the
// key is replaced, away with the key, into a copy with the keys, from a
// sync that left it out, and never into a backup without keys.
func TestKeyBalanceAuthFollowsItsKey(t *testing.T) {
	probeSandbox(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-a1",
		Keys:           []KeyAccount{{Name: "B1", Key: "sk-b1"}},
		BalanceURL:     "https://relay.example.com/api/user/self",
		KeyBalanceAuth: map[string]KeyBalanceAuth{strings.ToUpper(KeyID("sk-b1")): {Token: " tok-b ", User: "2"}, KeyID("sk-gone"): {Token: "x"}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if len(p.KeyBalanceAuth) != 1 || p.KeyBalanceAuth[KeyID("sk-b1")].Token != "tok-b" {
		t.Fatalf("saved %+v", p.KeyBalanceAuth)
	}
	if !TakesKeyBalanceAuth(*p) {
		t.Fatal("a provider with a Balance URL takes a key's token")
	}
	if _, _, err := ReplaceKey("relay", KeyID("sk-b1"), "sk-b2"); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyBalanceAuth[KeyID("sk-b2")].Token != "tok-b" || len(p.KeyBalanceAuth) != 1 {
		t.Fatalf("after replace %+v", p.KeyBalanceAuth)
	}
	// sync: a provider that came without it keeps here's
	in := *p
	in.KeyBalanceAuth = nil
	if got := KeepSecrets(in, *p, false); got.KeyBalanceAuth[KeyID("sk-b2")].Token != "tok-b" {
		t.Fatalf("sync lost it: %+v", got.KeyBalanceAuth)
	}
	// a copy with the keys has them; one with a key of its own hasn't
	id, err := AddCopy(Provider{Name: "Relay 2", Chat: p.Chat, BalanceURL: p.BalanceURL}, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := Find(id); c.KeyBalanceAuth[KeyID("sk-b2")].Token != "tok-b" {
		t.Fatalf("copy %+v", c.KeyBalanceAuth)
	}
	id, err = AddCopy(Provider{Name: "Relay 3", Chat: p.Chat, Key: "sk-c", BalanceURL: p.BalanceURL}, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := Find(id); c.KeyBalanceAuth != nil {
		t.Fatalf("own-key copy %+v", c.KeyBalanceAuth)
	}
	if err := RemoveKey("relay", KeyID("sk-b2")); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); p.KeyBalanceAuth != nil {
		t.Fatalf("after remove %+v", p.KeyBalanceAuth)
	}
}
